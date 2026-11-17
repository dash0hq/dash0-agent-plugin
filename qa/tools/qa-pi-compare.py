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
  aborted turns  `agent_settled` with `aborted: true` is a root with
                 dash0.pi.turn.status=aborted. The plugin does not set ERROR
                 status for a stop by the user
  model calls    one assistant `message_end` is one model call, so one chat
                 span; the last call of a turn is its root. A chat span with
                 dash0.pi.usage.source=tool is not a model call of the agent
  models         the multiset of models those calls used
  tools          one `tool_execution_end` is one execute_tool span, by name,
                 with the MCP prefix stripped as the plugin does
  tool errors    `isError: true` is a span with exception.message, which the
                 plugin sets together with ERROR status
  tokens         per model call, Pi's usage: input + cacheRead + cacheWrite is
                 gen_ai.usage.input_tokens, output is output_tokens. The
                 (input, output) pairs of the calls are compared as a
                 multiset, so tokens moved from one call to another fail
                 even when the session totals agree
  tool usage     a `tool_execution_end` whose result has non-zero `usage` is
                 one chat span with dash0.pi.usage.source=tool under that
                 tool's span, with those tokens. Not for the pi-subagents
                 `subagent` and wait results: their usage is the children's
                 total, which the sub-agent rows and the child sessions check

With pi-subagents (QA_PI_SUBAGENTS=1), two more sources count:

  foreground     each row of a `subagent` tool result's details.results is
                 one child, so one invoke_agent span under that tool's span,
                 in the parent trace. The child's usage is one more chat
                 span's tokens. Those rows are pi-subagents' output, which the
                 plugin also reads, so this half checks the copy. The child's
                 own session file, which the plugin never reads, checks the
                 numbers.
  background     a `bg_wait` completion names the child's session file. The
                 child exports itself as its own session, so its spans are
                 read by the child's session id and compared with that file.

A sub-agent run with no child in the stream exits 2: the model did not follow
the prompt, and a run with no child checks no child.

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


# @tintinweb/pi-subagents, with its reportUsage setting, reports the billed
# tokens of its children as the usage of these tools. Its children export
# their own sessions.
TINTINWEB_SUBAGENT_TOOLS = frozenset({"Agent", "get_subagent_result", "steer_subagent"})


def children_total(name: str, result: dict) -> bool:
    """Whether a tool result's usage is the total of sub-agent children.

    The pi-subagents `subagent` result and a management result, such as the
    wait tool's, and the @tintinweb/pi-subagents tools in
    TINTINWEB_SUBAGENT_TOOLS report the usage of children that are counted
    elsewhere.
    """
    details = result.get("details") or {}
    return name == "subagent" or name in TINTINWEB_SUBAGENT_TOOLS or (details.get("mode") == "management"
                                  and isinstance(details.get("results"), list))


def expectation(events: list[dict]) -> dict:
    calls = []
    tools: Counter[str] = Counter()
    errors: Counter[str] = Counter()
    tool_usage = []
    turns = aborted = 0
    for event in events:
        kind = event.get("type")
        if kind == "agent_settled":
            turns += 1
            aborted += bool(event.get("aborted"))
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
            result = event.get("result") or {}
            usage = result.get("usage")
            if isinstance(usage, dict) and not children_total(str(event.get("toolName")), result):
                counts = usage_counts(usage)
                if any(counts):
                    tool_usage.append((name, *counts))
    return {"turns": turns, "aborted": aborted, "calls": calls, "tools": tools,
            "errors": errors, "tool_usage": sorted(tool_usage, key=repr)}


def is_tool_usage(span: dict) -> bool:
    """A chat span that carries a tool's own model usage, not a model call."""
    return span["attrs"].get("dash0.pi.usage.source") == "tool"


def model_chats(spans: list[dict]) -> list[dict]:
    """The chat spans that are model calls: not a tool's usage span."""
    return [s for s in spans if str(s["name"]).startswith("chat") and not is_tool_usage(s)]


def tool_usage_spans(spans: list[dict]) -> list[tuple]:
    """(tool, input, output) of each tool usage span, by the tool it is under."""
    by_id = {s["span_id"]: s for s in spans}
    out = []
    for span in spans:
        if not (str(span["name"]).startswith("chat") and is_tool_usage(span)):
            continue
        parent = by_id.get(span.get("parent_span_id")) or {}
        name = str(parent.get("name") or "").removeprefix("execute_tool ")
        out.append((name or None, *span_tokens([span])))
    return sorted(out, key=repr)


def usage_counts(usage: dict) -> tuple[int, int]:
    counts = [int(usage.get(k) or 0) for k in ("input", "cacheRead", "cacheWrite")]
    return sum(counts), int(usage.get("output") or 0)


def call_pairs(calls: list[dict]) -> list[tuple]:
    """(input, output) of each stream call, sorted. None is a call with no usage."""
    return sorted(((c["input"], c["output"]) for c in calls), key=repr)


def span_pairs(spans: list[dict]) -> list[tuple]:
    """(input, output) of each chat span, sorted. None is an absent attribute."""
    def count(span, attr):
        value = span["attrs"].get(attr)
        return None if value is None else int(value)
    return sorted(((count(s, "gen_ai.usage.input_tokens"), count(s, "gen_ai.usage.output_tokens"))
                   for s in spans), key=repr)


def span_tokens(spans: list[dict]) -> tuple[int, int]:
    """Input and output tokens summed over spans, as Dash0 stored them."""
    return (sum(int(s["attrs"].get("gen_ai.usage.input_tokens") or 0) for s in spans),
            sum(int(s["attrs"].get("gen_ai.usage.output_tokens") or 0) for s in spans))


def subagent_rows(events: list[dict]) -> dict:
    """Foreground children per subagent tool call, and background children.

    A foreground call returns its children in details.results. A background
    launch returns no rows; its children finish later and a bg_wait result
    lists them under completions.
    """
    foreground: dict[str, list[dict]] = {}
    background: list[dict] = []
    for event in events:
        if event.get("type") != "tool_execution_end":
            continue
        details = (event.get("result") or {}).get("details") or {}
        if event.get("toolName") == "subagent" and details.get("results"):
            foreground[event.get("toolCallId")] = details["results"]
        elif event.get("toolName") == "bg_wait":
            for completion in details.get("completions") or []:
                background.extend(completion.get("results") or [])
    return {"foreground": foreground, "background": background}


def session_file_usage(path: str) -> dict:
    """What a child's own session file says: its id, calls and tokens.

    A line that is not JSON, such as a last line that is not completely
    written, or a file with no session header, exits 2: the file cannot be
    compared, which is not a product mismatch.
    """
    header, calls = {}, []
    lines = pathlib.Path(path).read_text(encoding="utf-8", errors="replace").splitlines()
    for number, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            entry = json.loads(line)
        except json.JSONDecodeError as error:
            bail(f"{path}:{number} is not JSON ({error}); the child session file "
                 "is incomplete")
        if entry.get("type") == "session":
            header = entry
        message = entry.get("message") or {}
        if message.get("role") == "assistant" and message.get("model"):
            calls.append(usage_counts(message.get("usage") or {}))
    if not header.get("id"):
        bail(f"{path} has no session header with an id; it cannot be compared")
    return {"id": header["id"], "parent": header.get("parentSession"),
            "calls": len(calls), "input": sum(c[0] for c in calls),
            "output": sum(c[1] for c in calls)}


def parent_session_file(run_dir: pathlib.Path, session: str) -> pathlib.Path | None:
    """The parent's own session file: the one whose header has the run's id.

    The sessions directory is Pi's --session-dir, so a child's file can be
    next to the parent's. Glob order is not an identity.
    """
    for path in sorted((run_dir / "sessions").glob("*.jsonl")):
        with path.open(encoding="utf-8", errors="replace") as handle:
            first = handle.readline()
        try:
            header = json.loads(first)
        except json.JSONDecodeError:
            continue
        if header.get("type") == "session" and header.get("id") == session:
            return path.resolve()
    return None


def resolved(path: str | None) -> str | None:
    """A path with symlinks resolved, so /tmp and /private/tmp compare equal."""
    return str(pathlib.Path(path).resolve()) if path else None


def turn_root(span: dict, by_id: dict) -> dict | None:
    """The span at the top of a span's parent chain, or None if a link is missing."""
    seen = set()
    while span.get("parent_span_id"):
        if span["span_id"] in seen:
            return None
        seen.add(span["span_id"])
        span = by_id.get(span["parent_span_id"])
        if span is None:
            return None
    return span


def subagent_table(config, dataset, rows, spans, run_dir, session, child_export,
                   window=("now-2h", "now")):
    """Rows for the sub-agent comparison, as (label, want, got).

    Dash0 returns spans in query order, and children of one call can run in
    parallel, so a child is matched to its agent span by agent name, never by
    position.
    """
    out = []
    by_id = {s["span_id"]: s for s in spans}
    agents = [s for s in spans if str(s["name"]).startswith("invoke_agent ")]
    tool_of = {s["attrs"].get("gen_ai.tool.call.id"): s for s in spans
               if str(s["name"]) == "execute_tool subagent"}
    parent_file = parent_session_file(run_dir, session)
    if parent_file is None:
        bail(f"no session file with id {session} in {run_dir / 'sessions'}")

    want_agents = sum(len(r) for r in rows["foreground"].values())
    out.append(("foreground children -> invoke_agent", want_agents, len(agents)))
    for call_id, children in rows["foreground"].items():
        tool = tool_of.get(call_id)
        under = [a for a in agents if tool and a["parent_span_id"] == tool["span_id"]]
        out.append((f"  under subagent {call_id[-8:]}",
                    sorted(str(c.get("agent")) for c in children),
                    sorted(str(a["attrs"].get("gen_ai.agent.name")) for a in under)))

        want_tokens: Counter[tuple] = Counter()
        for child in children:
            want_in, want_out = usage_counts(child.get("usage") or {})
            # A child that exported itself leaves no tokens on the parent's
            # agent span: each token once.
            want_tokens[(str(child.get("agent")), *((0, 0) if child_export else (want_in, want_out)))] += 1
            file = child.get("sessionFile")
            if not file or not pathlib.Path(file).exists():
                out.append((f"    child {child.get('agent')} file", "present", "missing"))
                continue
            own = session_file_usage(file)
            out.append(("    child file names the parent", str(parent_file), resolved(own["parent"])))
            out.append(("    child file -> tool result", (own["input"], own["output"]),
                        (want_in, want_out)))
            if child_export:
                out += child_session_rows(config, dataset, own, session, "    child session",
                                          window)

        got_tokens: Counter[tuple] = Counter()
        for agent in under:
            usage = [s for s in spans if s["parent_span_id"] == agent["span_id"]
                     and str(s["name"]).startswith("chat")]
            got_tokens[(str(agent["attrs"].get("gen_ai.agent.name")), *span_tokens(usage))] += 1
            # The agent span's turn root must be a chat root of this session.
            # Its own parent is always in its trace, so that proves nothing.
            root = turn_root(agent, by_id)
            is_chat_root = root is not None and str(root["name"]).startswith("chat")
            out.append(("    same trace as parent chat root", agent["trace_id"],
                        root["trace_id"] if is_chat_root else None))
        out.append(("    tokens on chat under agent",
                    sorted(want_tokens.elements()), sorted(got_tokens.elements())))

    for child in rows["background"]:
        file = child.get("sessionFile")
        if not file or not pathlib.Path(file).exists():
            out.append(("background child file", "present", "missing"))
            continue
        own = session_file_usage(file)
        out += child_session_rows(config, dataset, own, session,
                                  f"background {own['id'][-8:]}", window)
    return out


def child_session_rows(config, dataset, own, session, label, window=("now-2h", "now")):
    """A child that exported itself, read by its own session id."""
    child_spans, error = wait_for_spans(config, own["id"], dataset, 1, window)
    if error:
        bail(error)
    if not child_spans:
        # As for the parent: no spans at all is backend lag or a wrong id,
        # which the comparison cannot tell from a product fault.
        bail(f"no spans for child session {own['id']} in {dataset} after "
             f"{SETTLE_SECONDS}s. Check that the child loaded the plugin.")
    chats = model_chats(child_spans)
    parent_ids = {s["attrs"].get("dash0.pi.session.parent.id") for s in child_spans}
    return [
        (f"{label} model calls", own["calls"], len(chats)),
        ("  tokens (in, out)", (own["input"], own["output"]), span_tokens(chats)),
        ("  linked to parent session", {session}, parent_ids),
    ]


# The span query's limit. A result this long can be cut off.
SPAN_LIMIT = 100


def run_window(manifest: dict) -> tuple[str, str]:
    """The run's time window from manifest.json, widened as qa-attrs.py does."""
    return (compare.widen(manifest.get("started_at") or "now-2h", -60),
            compare.widen(manifest.get("ended_at") or "now", 120))


def wait_for_spans(config, session, dataset, want_turns, window=("now-2h", "now")):
    """Every span for the session in the window, once the turn roots are there.

    A result as long as the limit can be cut off, which would read as a
    mismatch, so it is returned as an error: the comparison cannot be made.
    """
    deadline = time.monotonic() + SETTLE_SECONDS
    while True:
        spans, error = compare.query_dash0(config, session, dataset, *window, SPAN_LIMIT)
        if error:
            return None, error
        if len(spans or []) >= SPAN_LIMIT:
            return None, (f"the query for {session} returned {len(spans)} spans, its "
                          f"limit of {SPAN_LIMIT}. The span set can be cut off, so a "
                          "comparison would cover part of the session.")
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
    for name in ("stream.jsonl", "session-id", "meta.json", "manifest.json"):
        if not (run_dir / name).exists():
            bail(f"{run_dir / name} does not exist; the run is incomplete")
    session = (run_dir / "session-id").read_text().strip()
    meta = json.loads((run_dir / "meta.json").read_text())
    window = run_window(json.loads((run_dir / "manifest.json").read_text()))
    if meta.get("pi_exit"):
        bail(f"pi exited {meta['pi_exit']}; the run is incomplete")

    config, error = compare.load_config(ROOT)
    if error:
        bail(error)
    dataset = config.get("piDataset")
    if not dataset:
        bail("qa/config.local.json is missing: piDataset")

    events = load_stream(run_dir / "stream.jsonl")
    want = expectation(events)
    children = subagent_rows(events)
    if meta.get("pi_subagents") and not (children["foreground"] or children["background"]):
        bail("the stream has no sub-agent child: no subagent result with "
             "details.results and no bg_wait completion. The model did not "
             "follow the prompt; run it again.")
    spans, error = wait_for_spans(config, session, dataset, want["turns"], window)
    if error:
        bail(error)
    if not spans:
        bail(f"no spans for {session} in {dataset} after {SETTLE_SECONDS}s. Check that "
             "the run set PI_PLUGIN_OPTION_DATASET, and read plugin-debug.log.")

    # The chat span under an invoke_agent span carries a foreground child's
    # totals. It is not one of the parent's model calls, so the parent rows
    # leave it out, and the sub-agent rows check it.
    agent_ids = {s["span_id"] for s in spans if str(s["name"]).startswith("invoke_agent ")}
    chats = [s for s in model_chats(spans) if s.get("parent_span_id") not in agent_ids]
    roots = [s for s in chats if not s.get("parent_span_id")]
    got_aborted = sum(s["attrs"].get("dash0.pi.turn.status") == "aborted" for s in roots)
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
        ("aborted turns -> turn.status", want["aborted"], got_aborted),
        ("model calls -> chat spans", len(want["calls"]), len(chats)),
        ("models", dict(want_models), dict(got_models)),
        ("tools", dict(want["tools"]), dict(got_tools)),
        ("tool errors", dict(want["errors"]), dict(got_errors)),
        ("input tokens", want_in, got_in),
        ("output tokens", want_out, got_out),
        ("(input, output) per call", call_pairs(want["calls"]), span_pairs(chats)),
        ("harness", {"pi"}, harness),
    ]
    got_tool_usage = tool_usage_spans(spans)
    if want["tool_usage"] or got_tool_usage:
        rows.append(("(tool, input, output) tool usage", want["tool_usage"], got_tool_usage))
    if children["foreground"] or children["background"]:
        rows += subagent_table(config, dataset, children, spans, run_dir, session,
                               bool(meta.get("pi_subagents_child_export")), window)
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
