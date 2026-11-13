#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
"""Line a recorded Pi run up against the spans Dash0 stored.

The expectation is computed from Pi's own `--mode json` stream, which the
plugin never reads: the plugin gets Pi's events in-process, and the stream is
what Pi writes to stdout for the same session. Agreement is two channels
agreeing, not one channel copied twice.

What is compared, per run:

  turns          one `agent_settled` in the stream is one turn, so one chat
                 root (a chat span with no parent)
  model calls    one assistant `message_end` is one model call, so one chat
                 span; the last call of a turn is its root
  models         the multiset of models those calls used
  tools          one `tool_execution_end` is one execute_tool span, by name,
                 with the MCP prefix stripped as the plugin does
  tool errors    `isError: true` is a span with exception.message, which the
                 plugin sets together with ERROR status
  tokens         per model call, Pi's usage: input + cacheRead + cacheWrite is
                 gen_ai.usage.input_tokens, output is output_tokens

Exit codes: 0 agreement, 1 a real mismatch, 2 the comparison could not be made.

Usage: qa/tools/qa-pi-compare.py qa/runs/<run-id>
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
    """qa-compare.py, for its config loader, span query and MCP name rule.

    Imported rather than copied: the auth token is handled in one place, and
    qa-compare.py strips it from any command it prints.
    """
    spec = importlib.util.spec_from_file_location(
        "qa_compare", os.path.join(HERE, "qa-compare.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


compare = load_compare()

# Dash0 can take minutes to make an accepted batch queryable (measured on the
# amp arm, 2026-10-12). Wait for the turn roots instead of reading once.
SETTLE_SECONDS = 360
POLL_SECONDS = 15


def bail(message: str) -> None:
    """Exit 2: the comparison could not be made, which is not a mismatch."""
    print(message, file=sys.stderr)
    raise SystemExit(2)


def load_stream(path: pathlib.Path) -> list[dict]:
    events = []
    for line in path.read_text(encoding="utf-8", errors="replace").split("\n"):
        line = line.strip()
        if line:
            try:
                events.append(json.loads(line))
            except json.JSONDecodeError:
                pass
    return events


def expectation(events: list[dict]) -> dict:
    calls = []
    tools: Counter[str] = Counter()
    errors: Counter[str] = Counter()
    turns = 0
    for event in events:
        kind = event.get("type")
        if kind == "agent_settled":
            turns += 1
        elif kind == "message_end":
            message = event.get("message") or {}
            if message.get("role") != "assistant" or not message.get("model"):
                continue
            usage = message.get("usage") or {}
            counts = [int(usage.get(k) or 0) for k in ("input", "cacheRead", "cacheWrite", "output")]
            calls.append({
                "model": message["model"],
                # A call that reported nothing carries no token attributes.
                "input": counts[0] + counts[1] + counts[2] if any(counts) else None,
                "output": counts[3] if any(counts) else None,
            })
        elif kind == "tool_execution_end":
            name = compare.mcp_tool_name(str(event.get("toolName") or ""))
            tools[name] += 1
            if event.get("isError"):
                errors[name] += 1
    return {"turns": turns, "calls": calls, "tools": tools, "errors": errors}


def wait_for_spans(config, session, dataset, want_turns):
    deadline = time.monotonic() + SETTLE_SECONDS
    while True:
        spans, error = compare.query_dash0(config, session, dataset, "now-2h", "now", 100)
        if error:
            return None, error
        roots = [s for s in spans or [] if str(s["name"]).startswith("chat")
                 and not s.get("parent_span_id")]
        if len(roots) >= want_turns or time.monotonic() >= deadline:
            return spans, None
        print(f"  waiting for spans: {len(roots)}/{want_turns} turn roots", file=sys.stderr)
        time.sleep(POLL_SECONDS)


def main() -> int:
    if len(sys.argv) != 2:
        bail(__doc__)
    run_dir = pathlib.Path(sys.argv[1])
    for name in ("stream.jsonl", "session-id", "meta.json"):
        if not (run_dir / name).exists():
            bail(f"{run_dir / name} does not exist; the run is incomplete")
    session = (run_dir / "session-id").read_text().strip()
    meta = json.loads((run_dir / "meta.json").read_text())
    if meta.get("pi_exit"):
        bail(f"pi exited {meta['pi_exit']}; the run is incomplete")

    config, error = compare.load_config(ROOT)
    if error:
        bail(error)
    dataset = config.get("piDataset")
    if not dataset:
        bail("qa/config.local.json is missing: piDataset")

    want = expectation(load_stream(run_dir / "stream.jsonl"))
    spans, error = wait_for_spans(config, session, dataset, want["turns"])
    if error:
        bail(error)
    if not spans:
        bail(f"no spans for {session} in {dataset} after {SETTLE_SECONDS}s. Check that "
             "the run set PI_PLUGIN_OPTION_DATASET, and read plugin-debug.log.")

    chats = [s for s in spans if str(s["name"]).startswith("chat")]
    roots = [s for s in chats if not s.get("parent_span_id")]
    tool_spans = [s for s in spans if str(s["name"]).startswith("execute_tool ")]
    got_tools = Counter(str(s["name"]).removeprefix("execute_tool ") for s in tool_spans)
    got_errors = Counter(str(s["name"]).removeprefix("execute_tool ") for s in tool_spans
                         # The span query returns no status, so this reads the
                         # message the plugin sets together with ERROR status.
                         if s["attrs"].get("exception.message"))
    # Dash0 normalizes the model at ingest (claude-haiku-4-5-20251001 becomes
    # claude-haiku-4-5) and keeps what the plugin sent in .original, which is
    # the value to compare. Measured on qa/runs/setup-probe-pi, 2026-11-13.
    got_models = Counter(s["attrs"].get("dash0.gen_ai.request.model.original")
                         or s["attrs"].get("gen_ai.request.model") for s in chats)
    want_models = Counter(c["model"] for c in want["calls"])

    def total(key, attr):
        want_total = sum(c[key] for c in want["calls"] if c[key] is not None)
        got_total = sum(int(s["attrs"].get(attr) or 0) for s in chats)
        return want_total, got_total

    want_in, got_in = total("input", "gen_ai.usage.input_tokens")
    want_out, got_out = total("output", "gen_ai.usage.output_tokens")
    harness = {s["attrs"].get("gen_ai.harness.name") for s in spans}

    rows = [
        ("turns -> chat roots", want["turns"], len(roots)),
        ("model calls -> chat spans", len(want["calls"]), len(chats)),
        ("models", dict(want_models), dict(got_models)),
        ("tools", dict(want["tools"]), dict(got_tools)),
        ("tool errors", dict(want["errors"]), dict(got_errors)),
        ("input tokens", want_in, got_in),
        ("output tokens", want_out, got_out),
        ("harness", {"pi"}, harness),
    ]
    print(f"run     : {run_dir}")
    print(f"session : {session}")
    print(f"dataset : {dataset}")
    print()
    width = max(len(r[0]) for r in rows)
    failures = 0
    for label, wanted, got in rows:
        ok = wanted == got
        failures += not ok
        print(f"{label:<{width}}  stream={wanted!s:<28} dash0={got!s:<28} "
              f"{'ok' if ok else 'MISMATCH'}")
    print()
    print("AGREEMENT" if not failures else f"{failures} MISMATCH(ES)")
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
