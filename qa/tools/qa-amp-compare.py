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
import time
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

# Measured 2026-10-12: a batch the plugin sent and Dash0 answered 200 took
# between 2.5 and 5.5 minutes to become queryable, while four other runs the
# same hour were readable within 30s. The old single read reported "no spans"
# for that run, which reads as total telemetry loss rather than as backend lag,
# and it is almost certainly the "delivery failure that was not explained" in
# amp/README.md. A spec cannot run unattended on a read that is right only when
# the backend is quick, so this waits instead.
SETTLE_SECONDS = 360
POLL_SECONDS = 15


def wait_for_spans(config, thread, dataset, want_turns):
    """Every span Dash0 holds for this thread, once it holds them all.

    Returns as soon as the turn roots are all there, so a quick run pays
    nothing. A run that never completes pays the whole deadline once.
    """
    deadline = time.monotonic() + SETTLE_SECONDS
    spans, error = [], None
    while True:
        spans, error = compare.query_dash0(config, thread, dataset,
                                           "now-2h", "now", 100)
        if error:
            return None, error
        roots = [s for s in spans or []
                 if str(s["name"]).startswith("chat")
                 and s["attrs"].get("dash0.amp.timing") != "observation"]
        if len(roots) >= want_turns or time.monotonic() >= deadline:
            return spans, None
        print(f"  waiting for spans: {len(roots)}/{want_turns} turn roots",
              file=sys.stderr)
        time.sleep(POLL_SECONDS)


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


# Amp reaches an MCP tool by running code inside `code_exec`, after finding it
# with `tool_search`. Neither fires a plugin hook — the hooks fire for the MCP
# tool called inside — so the stream names the outer layer and the spans name
# the inner one, and comparing them as one multiset reports a mismatch on a
# correct run. Measured 2026-10-12 on amp 0.0.1791792883-gf1ab8d; the arm was
# written against 0.0.1789407412, where the stream named the MCP tools
# directly. See findings/amp-mcp-calls-run-inside-code-exec.md.
WRAPPER_TOOLS = {"code_exec", "tool_search"}


def expectation(turns: list[list[dict]]) -> dict:
    tools: Counter[str] = Counter()
    wrappers: Counter[str] = Counter()
    output_tokens = 0
    for events in turns:
        for event in events:
            if event.get("type") != "assistant":
                continue
            message = event.get("message") or {}
            for block in message.get("content") or []:
                if block.get("type") == "tool_use" and block.get("name"):
                    name = compare.mcp_tool_name(block["name"])
                    (wrappers if name in WRAPPER_TOOLS else tools)[name] += 1
            usage = message.get("usage") or {}
            output_tokens += int(usage.get("output_tokens") or 0)
    return {"turns": len(turns), "tools": tools, "wrappers": wrappers,
            "output_tokens": output_tokens}


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
    spans, query_error = wait_for_spans(config, thread, dataset, want["turns"])
    if query_error:
        bail(query_error)
    if not spans:
        bail(
            f"no spans for {thread} in {dataset} after {SETTLE_SECONDS}s. "
            "Either the export never happened or it went to another dataset: "
            "check that the run set AMP_PLUGIN_OPTION_DATASET, not DASH0_DATASET."
        )

    names = Counter(str(s["name"]) for s in spans)
    got_tools = Counter({name.removeprefix("execute_tool "): count
                         for name, count in names.items()
                         if name.startswith("execute_tool ")})

    # A turn's root and its usage children are all `chat` spans, so counting the
    # name alone cannot say how many turns there were. The children carry
    # dash0.amp.timing=observation; the roots never do. Splitting them keeps the
    # turn row an equality — a lower bound would pass on a run that lost a turn
    # and gained a model call — and makes the extra chat spans countable, which
    # is what any prompt-count metric will see.
    chats = [s for s in spans if str(s["name"]).startswith("chat")]
    roots = [s for s in chats
             if s["attrs"].get("dash0.amp.timing") != "observation"]
    children = len(chats) - len(roots)
    # A run that went through a wrapper tool cannot have its tool calls compared:
    # the stream names the wrapper, the spans name what ran inside it, and
    # neither channel can see the other's layer. That is not agreement and not a
    # mismatch — it is a question this run cannot answer, and saying so is the
    # only honest option. The turn and usage rows are unaffected.
    blind = bool(want["wrappers"])
    rows = [
        ("turns -> chat roots", want["turns"], len(roots),
         want["turns"] == len(roots)),
        ("tool calls", sum(want["tools"].values()), sum(got_tools.values()),
         None if blind else
         sum(want["tools"].values()) == sum(got_tools.values())),
        ("tool names", dict(want["tools"]), dict(got_tools),
         None if blind else want["tools"] == got_tools),
    ]

    print(f"run     : {run_dir}")
    print(f"thread  : {thread}")
    print(f"dataset : {dataset}")
    print(f"usage   : {'on (default)' if meta.get('export_usage') else 'off'}")
    print()
    width = max(len(r[0]) for r in rows)
    failures = 0
    for label, wanted, got, ok in rows:
        failures += ok is False
        verdict = "UNPROVABLE" if ok is None else ("ok" if ok else "MISMATCH")
        print(f"{label:<{width}}  stream={wanted!s:<24} dash0={got!s:<24} "
              f"{verdict}")
    if blind:
        print(f"\nthe stream saw only wrapper tools: {dict(want['wrappers'])}. "
              "Amp runs an MCP tool inside `code_exec`, which fires no plugin "
              "hook, so the two channels name different layers and the tool "
              "rows above prove nothing. See "
              "findings/amp-mcp-calls-run-inside-code-exec.md.")

    if meta.get("export_usage"):
        # Not a verdict: one per earlier model call of a turn is correct. It is
        # printed because these are `chat` spans like the roots, so an
        # operation-based prompt count reads each one as another prompt.
        print(f"\nextra chat spans (earlier model calls): {children}")
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
        elif "matched" in status:
            print("  'partial' means some model call's usage never persisted, so "
                  "these are not expected to agree. See "
                  "findings/amp-answering-model-call-is-never-attributed.md.")
        else:
            # Usage export was on and no turn ever matched, so every token count
            # above is unsourced. Printing AGREEMENT here would report a passing
            # comparison of nothing.
            failures += 1
            print("  MISMATCH: usage export is on but no turn reached 'matched', "
                  "so no token count was proven. See "
                  "findings/amp-answering-model-call-is-never-attributed.md.")

    print()
    print("AGREEMENT" if not failures else f"{failures} MISMATCH(ES)")
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
