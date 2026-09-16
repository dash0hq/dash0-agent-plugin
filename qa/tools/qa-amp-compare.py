#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
"""Line a recorded Amp run up against the spans Dash0 actually stored.

The expectation is computed from Amp's own --stream-json, which the plugin
never sees, so agreement is two independent channels agreeing rather than one
channel copied twice. `amp threads export` is deliberately not used for the
expectation: the plugin reads it to build usage, so it would prove a faithful
copy instead of a correct measurement. It is read only to explain a usage
mismatch after one has already been found.

Exit codes: 0 agreement, 1 a real mismatch, 2 the comparison could not be made.

Usage: qa/tools/qa-amp-compare.py qa/runs/<run-id>
"""

from __future__ import annotations

import importlib.util
import json
import os
import pathlib
import sys
from collections import Counter

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, "..", ".."))


def load_compare():
    """Import qa-compare.py for its config loader, span query and name rules.

    Imported rather than reimplemented, for the same reason qa-attrs.py does it:
    the auth token is handled in exactly one place. qa-compare.py strips it from
    any command it prints, and a second copy of that logic is a second chance to
    print a credential. It also owns mcp_tool_name, which mirrors
    NormalizeMCPToolName in internal/pipeline — a rule that should not have a
    third copy.
    """
    spec = importlib.util.spec_from_file_location(
        "qa_compare", os.path.join(HERE, "qa-compare.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


compare = load_compare()


def load_stream(path: pathlib.Path) -> list[list[dict]]:
    """Split the stream into one list of events per `amp -x` invocation.

    Each invocation opens with a `system` event and closes with `result`, and a
    resumed turn appends to the same file, so the boundary is the `system`.
    """
    turns: list[list[dict]] = []
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("type") == "system" or not turns:
            turns.append([])
        turns[-1].append(event)
    return turns


def bail(message: str) -> None:
    """Exit 2: the comparison could not be made, which is not a mismatch.

    A caller has to be able to tell "the run or the config is incomplete" from
    "the product disagrees with the stream". sys.exit(str) exits 1, which is
    the mismatch code, so every give-up path goes through here instead.
    """
    print(message, file=sys.stderr)
    raise SystemExit(2)


def expectation(turns: list[list[dict]]) -> dict:
    tools: Counter[str] = Counter()
    model_calls = 0
    output_tokens = 0
    for events in turns:
        for event in events:
            if event.get("type") != "assistant":
                continue
            model_calls += 1
            message = event.get("message") or {}
            for block in message.get("content") or []:
                if block.get("type") == "tool_use" and block.get("name"):
                    tools[compare.mcp_tool_name(block["name"])] += 1
            usage = message.get("usage") or {}
            output_tokens += int(usage.get("output_tokens") or 0)
    return {"turns": len(turns), "tools": tools,
            "model_calls": model_calls, "output_tokens": output_tokens}




def main() -> int:
    if len(sys.argv) != 2:
        bail(__doc__)
    run_dir = pathlib.Path(sys.argv[1])

    for name in ("stream.jsonl", "thread-id", "meta.json"):
        if not (run_dir / name).exists():
            bail(f"{run_dir / name} does not exist; the run is incomplete")
    stream = run_dir / "stream.jsonl"
    thread = (run_dir / "thread-id").read_text().strip()
    meta = json.loads((run_dir / "meta.json").read_text())

    config, config_error = compare.load_config(ROOT)
    if config_error:
        bail(config_error)
    # The amp arm reads ampDataset, not dataset: it deliberately stays out of
    # the shared one. See '### Amp writes to its own dataset' in qa/setup.md.
    dataset = config.get("ampDataset")
    if not dataset:
        bail("qa/config.local.json is missing: ampDataset")

    want = expectation(load_stream(stream))
    # The helper is detached and polls for up to twenty seconds, so a turn's
    # spans can land well after `amp` exited. The window is generous for that.
    spans, query_error = compare.query_dash0(config, thread, dataset,
                                             "now-2h", "now", 100)
    if query_error:
        bail(query_error)
    if not spans:
        bail(
            f"no spans for {thread} in {dataset}. Either the export never "
            "happened, it has not landed yet (the helper polls for up to "
            "twenty seconds after the turn), or it went to another dataset: "
            "check that the run set AMP_PLUGIN_OPTION_DATASET, not DASH0_DATASET."
        )

    names = Counter(str(s["name"]) for s in spans)
    chat = sum(count for name, count in names.items() if name.startswith("chat"))
    got_tools = Counter({name.removeprefix("execute_tool "): count
                         for name, count in names.items()
                         if name.startswith("execute_tool ")})

    # With usage on, each extra model call adds a chat child, so the turn row is
    # a lower bound rather than an equality.
    turns_ok = (chat >= want["turns"] if meta.get("export_usage")
                else chat == want["turns"])
    rows = [
        ("turns -> chat spans", want["turns"], chat, turns_ok),
        ("tool calls", sum(want["tools"].values()), sum(got_tools.values()),
         sum(want["tools"].values()) == sum(got_tools.values())),
        ("tool names", dict(want["tools"]), dict(got_tools),
         want["tools"] == got_tools),
    ]

    print(f"run     : {run_dir}")
    print(f"thread  : {thread}")
    print(f"dataset : {dataset}")
    print(f"usage   : {'on' if meta.get('export_usage') else 'off (opt-in)'}")
    print()
    width = max(len(r[0]) for r in rows)
    failures = 0
    for label, wanted, got, ok in rows:
        failures += not ok
        print(f"{label:<{width}}  stream={wanted!s:<24} dash0={got!s:<24} "
              f"{'ok' if ok else 'MISMATCH'}")

    if meta.get("export_usage"):
        status = {s["attrs"].get("dash0.amp.usage.status") for s in spans}
        status.discard(None)
        print(f"\nusage status: {', '.join(sorted(status)) or '(absent)'}")
        got_out = sum(int(s["attrs"].get("gen_ai.usage.output_tokens") or 0)
                      for s in spans)
        print(f"output tokens  stream={want['output_tokens']} dash0={got_out}")
        if status == {"matched"}:
            # Every model call resolved a usage record, so the exporter's
            # per-call attribution must add up to exactly what the stream
            # reported per assistant message. This is the strongest check here:
            # the two channels are independent, and it is only available once
            # nothing is partial.
            if got_out != want["output_tokens"]:
                failures += 1
                print("  MISMATCH: every turn matched, so these must be equal.")
            else:
                print("  ok — every model call is accounted for.")
        else:
            print("  'partial' means some model call's usage never persisted, so "
                  "these are not expected to agree. See "
                  "findings/amp-answering-model-call-is-never-attributed.md.")

    print()
    print("AGREEMENT" if not failures else f"{failures} MISMATCH(ES)")
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
