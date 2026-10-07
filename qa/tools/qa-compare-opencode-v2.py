#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
"""Compare an OpenCode QA run's Dash0 spans against OpenCode's own session record.

qa-compare.py hands a manifest with `"runtime": "opencode-v2"` to this tool. It
lines up three channels:

  dash0    the spans Dash0 stored for the session, at full precision
  export   `opencode session export`: OpenCode's own record of each turn, its
           tool calls and every step's token counts. This is the independent
           channel. The plugin never reads it; it reads the live V2 event stream.
  debug    plugin-debug.log, every span the plugin built and sent. The
           product's own output, so it never supplies an expectation; its counts
           are held to the export like Dash0's, which splits "built but lost in
           transport" from "never built". It is also the only place the wire
           payload of the message attributes can be read: Dash0 masks those at
           ingest, so a redacted value there proves nothing about the plugin.

The expectation, per turn (a turn starts at each `user` message):

  chat          one span per turn
  execute_tool  one span per `tool` part in the turn's assistant messages
  input tokens  sum of input + cache.read + cache.write over the turn's steps
  output tokens sum of output + reasoning

Exit 0 when everything reconciles, 1 on a disagreement, 2 when the run could not
be measured: a failed or timed-out turn, a failed export, a turn count that is
not the one the driver ran, or an input that cannot be read. Never read 2 as
either verdict.

A sub-agent is a child session with its own export, session-export-<id>.json.
Each of its turns implies one invoke_agent span instead of a chat span, and its
tool parts count towards the same execute_tool total: its spans carry the
parent's conversation id, so they come back from the same query.

Tool calls are matched by call ID, and the span must carry the export's name
unchanged. An MCP call is named <server>_<tool> in both: the plugin API cannot
tell an MCP tool from a local tool in the same namespace, so a span claiming an
MCP server is itself a disagreement.

With a reload, the export's tool timings must show a tool call that ran before
the reload started and completed after it finished; otherwise the run did not
test a mid-tool handoff and is not measured.

The prompt is checked too: with omit_io on, its text must appear on no span and
every sent chat span's messages must be exactly the redacted structure; with
omit_io off, it must appear in a sent chat span's gen_ai.input.messages.
"""

import argparse
import collections
import importlib.util
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("qa_compare", os.path.join(HERE, "qa-compare.py"))
qa = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(qa)

REDACTED = "<REDACTED>"
MCP_SERVER = "dash0.gen_ai.tool.mcp_server"
CALL_ID = "gen_ai.tool.call.id"


class Unmeasurable(Exception):
    """The run cannot be judged either way; exits 2."""


def _no_duplicate_keys(pairs):
    keys = [k for k, _ in pairs]
    if len(keys) != len(set(keys)):
        raise ValueError(f"duplicate keys {keys}")
    return dict(pairs)


def is_redacted(value):
    """True only for the exact structure the exporter writes under omit_io:
    a non-empty list of {role, parts}, every part {type: text, content:
    <REDACTED>}. A marker next to other text, an extra key or malformed JSON
    is not redacted."""
    try:
        messages = json.loads(value, object_pairs_hook=_no_duplicate_keys)
    except (TypeError, ValueError):
        return False
    if not isinstance(messages, list) or not messages:
        return False
    for m in messages:
        if not isinstance(m, dict) or set(m) != {"role", "parts"} or not isinstance(m["role"], str):
            return False
        if not isinstance(m["parts"], list) or not m["parts"]:
            return False
        if any(part != {"type": "text", "content": REDACTED} for part in m["parts"]):
            return False
    return True


def call_problems(calls, expected_calls):
    """Each execute_tool span against the export's call of the same ID."""
    problems = []
    for span in calls:
        attrs = span["attrs"]
        call_id, got = attrs.get(CALL_ID), attrs.get(qa.TOOL) or "<no name>"
        want = expected_calls.get(call_id)
        if want is None:
            problems.append(f"tool call {call_id} ({got}) is not in the export")
        elif got != want:
            problems.append(f"tool call {call_id}: Dash0 names it {got}, the export {want}")
        if attrs.get(MCP_SERVER):
            problems.append(f"tool call {call_id} claims MCP server {attrs[MCP_SERVER]}, which the plugin cannot know")
    return problems


def reload_landed_mid_tool(export_path, started_ms, ended_ms):
    """Whether a tool call in the export spans the whole reload."""
    for message in json.load(open(export_path)).get("messages") or []:
        for part in message.get("content") or []:
            if part.get("type") != "tool":
                continue
            when = part.get("time") or {}
            if (when.get("ran") or float("inf")) <= started_ms and (when.get("completed") or 0) >= ended_ms:
                return True
    return False


def export_turns(path):
    if not os.path.exists(path):
        return None, f"{path} does not exist"
    data = json.load(open(path))
    turns = []
    for message in data.get("messages") or []:
        kind = message.get("type")
        if kind == "user":
            turns.append({"input": 0, "output": 0, "tools": collections.Counter(), "calls": {}, "outcome": None})
        elif not turns:
            continue
        elif kind == "assistant":
            t, tokens = turns[-1], message.get("tokens") or {}
            cache = tokens.get("cache") or {}
            t["input"] += (tokens.get("input") or 0) + (cache.get("read") or 0) + (cache.get("write") or 0)
            t["output"] += (tokens.get("output") or 0) + (tokens.get("reasoning") or 0)
            for part in message.get("content") or []:
                if part.get("type") == "tool":
                    t["tools"][part.get("name") or "<no name>"] += 1
                    if part.get("id"):
                        t["calls"][part["id"]] = part.get("name") or "<no name>"
        elif kind == "idle":
            turns[-1]["outcome"] = message.get("outcome")
    return turns, None


def debug_spans(run_dir):
    """Every span in plugin-debug.log, as {name, attrs}."""
    path = os.path.join(run_dir, "plugin-debug.log")
    if not os.path.exists(path):
        raise Unmeasurable(f"{path} does not exist; the driver always enables debug")
    spans = []
    for line in open(path):
        if not line.startswith("[dash0:trace] "):
            continue
        payload = json.loads(line.split(" ", 1)[1])
        for rs in payload.get("resourceSpans") or []:
            for ss in rs.get("scopeSpans") or []:
                for span in ss.get("spans") or []:
                    attrs = {a["key"]: qa.attr_value(a.get("value", {})) for a in span.get("attributes") or []}
                    spans.append({"name": span.get("name", ""), "attrs": attrs})
    return spans


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("run_dir")
    parser.add_argument("--dataset", default=None)
    parser.add_argument("--limit", type=int, default=100)
    args = parser.parse_args()
    try:
        return compare(args)
    except Unmeasurable as err:
        print(f"cannot measure this run: {err}", file=sys.stderr)
    except (OSError, ValueError, KeyError) as err:
        # A malformed input is not a product disagreement.
        print(f"cannot measure this run, an input is unreadable: {err!r}", file=sys.stderr)
    return 2


def compare(args):
    run_dir = os.path.abspath(args.run_dir)
    manifest = json.load(open(os.path.join(run_dir, "manifest.json")))
    # A turn that failed or timed out still produces a chat span, so the counts
    # would reconcile; the run measured nothing it set out to.
    if manifest.get("opencode_exit_code") != 0:
        raise Unmeasurable(f"opencode run exited {manifest.get('opencode_exit_code')}; see opencode-stderr.log")
    if manifest.get("export_exit_code", 0) != 0:
        raise Unmeasurable(f"opencode session export exited {manifest['export_exit_code']}")
    root = subprocess.run(["git", "rev-parse", "--show-toplevel"],
                          capture_output=True, text=True, check=True).stdout.strip()
    config, error = qa.load_config(root)
    if error:
        raise Unmeasurable(f"qa/config.local.json is unusable: {error}")
    spans, error = qa.query_dash0(config, manifest["session_id"], args.dataset or config["dataset"],
                                  qa.widen(manifest["started_at"], -60),
                                  qa.widen(manifest["ended_at"], 120), args.limit)
    if error:
        raise Unmeasurable(f"Dash0 query failed:\n{error}")
    if len(spans) >= args.limit:
        raise Unmeasurable(f"Dash0 returned {len(spans)} spans, the query limit: the result is truncated")
    turns, error = export_turns(os.path.join(run_dir, "session-export.json"))
    if error:
        raise Unmeasurable(error)
    if len(turns) != manifest.get("turns"):
        raise Unmeasurable(f"the export has {len(turns)} turn(s), the driver ran {manifest.get('turns')}")
    child_turns = []
    for child in filter(None, (manifest.get("subagent_sessions") or "").split(",")):
        found, error = export_turns(os.path.join(run_dir, f"session-export-{child}.json"))
        if error:
            raise Unmeasurable(error)
        child_turns += found
    sent = debug_spans(run_dir)
    problems = []

    def ops(found, op):
        return [s for s in found if s["attrs"].get(qa.OP) == op]
    chats, sent_chats = ops(spans, "chat"), ops(sent, "chat")
    agents = ops(spans, "invoke_agent")
    calls = ops(spans, "execute_tool")
    expected_calls = {}
    for t in turns + child_turns:
        expected_calls.update(t["calls"])
    expected_tools = sum((t["tools"] for t in turns + child_turns), collections.Counter())

    print(f"run {manifest['run_id']}  session {manifest['session_id']}  model {manifest.get('model')}")
    print(f"{'':16}{'dash0':>8}{'export':>8}{'debug':>8}")
    for op, dash0, want in (("chat", len(chats), len(turns)),
                            ("invoke_agent", len(agents), len(child_turns)),
                            ("execute_tool", len(calls), sum(expected_tools.values()))):
        built = len(ops(sent, op))
        print(f"{op:16}{dash0:>8}{want:>8}{built:>8}")
        if dash0 != want:
            problems.append(f"{op}: Dash0 has {dash0}, the export has {want}")
        if built != want:
            problems.append(f"{op}: the plugin sent {built}, the export has {want}")

    problems += call_problems(calls, expected_calls)

    # Dash0 does not return span start times through this query, so turns are
    # matched as a multiset of (input, output) pairs rather than by position.
    def pairs(found):
        return sorted((s["attrs"].get(qa.USAGE_KEYS["input"]) or 0,
                       s["attrs"].get(qa.USAGE_KEYS["output"]) or 0) for s in found)
    print("\nper-turn tokens (input, output)")
    for label, found, expected in (("chat", chats, turns), ("invoke_agent", agents, child_turns)):
        got, want = pairs(found), sorted((t["input"], t["output"]) for t in expected)
        if not got and not want:
            continue
        print(f"  {label:13} dash0   {got}\n  {'':13} export  {want}")
        if got != want:
            problems.append(f"{label} per-turn token counts differ")

    # Dash0 masks gen_ai.input/output.messages at ingest (measured 2026-10-05:
    # the plugin sent the prompt, Dash0 stored <REDACTED>), so what the plugin
    # sent is judged on the debug log, the wire payload. Dash0 still has to
    # carry both attributes: the conversation view splits turns on them.
    omit_io = manifest.get("omit_io", True)
    prompt = manifest.get("prompt") or ""
    for chat in chats:
        for key in ("gen_ai.input.messages", "gen_ai.output.messages"):
            if not chat["attrs"].get(key):
                problems.append(f"a chat span in Dash0 has no {key}")
    for chat in sent_chats:
        for key in ("gen_ai.input.messages", "gen_ai.output.messages"):
            value = chat["attrs"].get(key)
            if omit_io and not is_redacted(value):
                problems.append(f"omit_io is on but a sent chat span's {key} is not the redacted structure")
    if prompt and omit_io:
        carrying = sorted({s["name"] for s in spans + sent
                           if any(isinstance(v, str) and prompt in v for v in s["attrs"].values())})
        if carrying:
            problems.append(f"omit_io is on but the prompt text is on: {carrying}")
    if prompt and not omit_io:
        if not any(prompt in (c["attrs"].get("gen_ai.input.messages") or "") for c in sent_chats):
            problems.append("omit_io is off but no sent chat span carries the prompt")
        elif not any(prompt in (c["attrs"].get("gen_ai.input.messages") or "") for c in chats):
            print("note: the plugin sent the prompt; Dash0 stored the messages masked")

    if manifest.get("reload_after"):
        # Only numbers count: the driver writes "-" when nothing was running.
        def pids(key):
            return {p for p in (manifest.get(key) or "").split(",") if p.isdigit()}
        before, after = pids("exporter_pids_before_reload"), pids("exporter_pids_after_reload")
        print(f"reload after {manifest['reload_after']}s: exporter {sorted(before)} -> {sorted(after)}")
        if not before or not after:
            raise Unmeasurable("no exporter process was running on one side of the reload; see reload.log")
        if before & after:
            problems.append("the reload did not replace the exporter, so it tested nothing")
        started, ended = manifest.get("reload_started_ms") or "", manifest.get("reload_ended_ms") or ""
        if not (started.isdigit() and ended.isdigit()):
            raise Unmeasurable("the driver recorded no reload timing; see reload.log")
        if not reload_landed_mid_tool(os.path.join(run_dir, "session-export.json"), int(started), int(ended)):
            raise Unmeasurable("no tool call was running for the whole reload, so no mid-tool handoff was tested")

    holes = qa.orphans(spans)
    if holes:
        problems.append(f"{len(holes)} span(s) parented outside their trace: {holes}")
    for t in turns:
        if t["outcome"] not in (None, "succeeded"):
            print(f"note: a turn ended with outcome {t['outcome']!r}")

    print()
    for p in problems:
        print(f"DIFF  {p}")
    print("OK    everything reconciles" if not problems else f"{len(problems)} disagreement(s)")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
