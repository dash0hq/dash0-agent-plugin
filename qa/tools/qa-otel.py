#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
"""Read a Copilot session out of its native-OpenTelemetry file, without the plugin.

This is the Copilot runtime's second observation channel, where
claude-code-usage-audit.py stands for Claude Code and qa-rollout.py for Codex.
It differs from both in one way that matters: the file is not only a record of
the session, it is also the plugin's *input*. Copilot's hooks carry no numbers
at all, so tokens, model and every tool execution reach the plugin through this
file. The cost figure this tool prints is Copilot's own; the plugin does not
export it.

That makes the channel weaker than the others by construction, and the report
has to say so. A count taken here and a count taken from Dash0 agreeing proves
the plugin copied its input faithfully; it does not prove Copilot wrote the
right thing in the first place. The hook record remains the only truly
independent input, and it can only speak for the session lifecycle.

Independence has one rule and this file keeps it: nothing here imports or shells
out to internal/source/copilot. The format is re-read from the file, so the two
readers can disagree — which is the entire point of having two.

What the file holds, as of Copilot CLI 1.0.80. Every line is one JSON record
with a `type` of `span`, `metric` or `log`; only spans are read here.

  invoke_agent   one per agent turn at the root of the turn's trace, carrying
                 the turn's whole usage and cost already summed by Copilot; and
                 one per sub-agent, nested under the tool that spawned it. The
                 plugin re-emits the nested ones and represents the root with the
                 turn's own chat span.
  chat           one per model round-trip, under the turn's invoke_agent. Their
                 usage sums to the turn's, and this is what the plugin sums.
  execute_tool   one per tool execution, with real start and end times. These
                 carry no gen_ai.conversation.id; membership is by shared
                 traceId, the same rule internal/source/copilot uses.

Two independent per-turn figures therefore come out of one file, and a
disagreement between them is Copilot's, not the plugin's:

  agent    every top-level invoke_agent span — what Copilot says the turn cost
  chat     every chat span summed — what the plugin sums to put on its chat span

Usage:
  qa/tools/qa-otel.py qa/runs/<run-id>
  qa/tools/qa-otel.py qa/runs/<run-id>/otel.jsonl --session <conversation-id>
  qa/tools/qa-otel.py qa/runs/<run-id> --json

Exit codes:
  0  the file was read
  2  it could not be read, or the run holds no native-OTel file at all
"""

import argparse
import collections
import glob
import json
import os
import sys

# The usage keys Copilot writes, mapped to the names the rest of the harness
# uses. input is INCLUSIVE of the cached part, so cache_read is a subset of it
# and must never be subtracted. Copilot reports no cache-write count; the column
# is reported as absent rather than as zero.
USAGE_KEYS = {
    "input": "gen_ai.usage.input_tokens",
    "output": "gen_ai.usage.output_tokens",
    "cache_read": "gen_ai.usage.cache_read.input_tokens",
    "reasoning": "gen_ai.usage.reasoning.output_tokens",
}


def empty_usage():
    return dict.fromkeys(USAGE_KEYS, 0)


def add(into, attrs):
    for name, key in USAGE_KEYS.items():
        value = attrs.get(key)
        into[name] += int(value) if isinstance(value, (int, float)) else 0


def otel_files(target):
    """Every native-OTel file a run holds, or the single file named directly.

    A run keeps otel.jsonl plus otel-<n>.jsonl for each further file the session
    produced, because a resumed turn can rotate the file and the plugin reads the
    newest one carrying the conversation rather than a fixed name.
    """
    if os.path.isfile(target):
        return [target]
    paths = sorted(glob.glob(os.path.join(target, "otel*.jsonl")))
    return paths


def _op(record):
    attrs = record.get("attributes") or {}
    return attrs.get("gen_ai.operation.name") or (record.get("name") or "").split()[0]


def _spawned_by_a_tool(record, by_id):
    """Whether this span is a sub-agent's invoke_agent.

    A sub-agent is delegated by a tool call, so its invoke_agent hangs directly
    under an execute_tool span. A turn's own root does not, whatever its trace
    is rooted on and whatever earlier turn it chains onto.
    """
    if _op(record) != "invoke_agent":
        return False
    parent = by_id.get(record.get("parentSpanId"))
    return parent is not None and _op(parent) == "execute_tool"


def read(paths, session_id=None):
    """Everything one pass over the file(s) yields, or an explanation."""
    out = {
        "files": paths,
        "conversations": set(),
        "session_id": session_id,
        "record_types": collections.Counter(),
        "spans": collections.Counter(),
        "models": [],
        "tools": collections.Counter(),
        "failed_tools": 0,
        "subagent_tools": 0,
        "turns": [],
        "subagents": 0,
        "agent": empty_usage(),
        "chat": empty_usage(),
        "chat_spans": 0,
        "cost": 0.0,
        "malformed_lines": 0,
    }

    # Two passes over the parsed records: the first learns which traces belong to
    # the conversation, the second attributes spans. execute_tool spans carry no
    # conversation id at all, so a single pass would drop every tool span that
    # appears before the chat span naming its trace.
    records = []
    for path in paths:
        try:
            handle = open(path)
        except OSError as err:
            return None, f"cannot read {path}: {err}"
        with handle:
            for line in handle:
                line = line.strip()
                if not line:
                    continue
                try:
                    records.append(json.loads(line))
                except json.JSONDecodeError:
                    # Counted, not skipped in silence: Copilot writes this file
                    # from a live process, so a torn last line is expected, and
                    # anything more than that is worth seeing.
                    out["malformed_lines"] += 1

    conv_traces = set()
    for record in records:
        out["record_types"][record.get("type")] += 1
        if record.get("type") != "span":
            continue
        attrs = record.get("attributes") or {}
        conv = attrs.get("gen_ai.conversation.id")
        if conv:
            out["conversations"].add(conv)
        if conv and (session_id is None or conv == session_id):
            if record.get("traceId"):
                conv_traces.add(record["traceId"])

    by_id = {}
    mine = []
    for record in records:
        if record.get("type") != "span":
            continue
        attrs = record.get("attributes") or {}
        by_id[record.get("spanId")] = record
        conv = attrs.get("gen_ai.conversation.id")
        if (session_id is not None and conv == session_id) or \
           (session_id is None and conv) or \
           record.get("traceId") in conv_traces:
            mine.append(record)

    for record in mine:
        attrs = record.get("attributes") or {}
        op = attrs.get("gen_ai.operation.name") or (record.get("name") or "").split()[0]
        out["spans"][op] += 1
        model = attrs.get("gen_ai.request.model")
        if model and model not in out["models"]:
            out["models"].append(model)

        if op == "chat":
            out["chat_spans"] += 1
            add(out["chat"], attrs)
            cost = attrs.get("github.copilot.cost")
            out["cost"] += cost if isinstance(cost, (int, float)) else 0
        elif op == "invoke_agent":
            # A sub-agent's invoke_agent hangs under the tool that spawned it, so
            # only the turn's own root is an agent turn. Summing every
            # invoke_agent would count a delegating turn's tokens twice: once in
            # the sub-agent's roll-up and once in the parent's.
            #
            # What marks a sub-agent is hanging under the execute_tool call that
            # spawned it. "Has a parent in this file" is too loose in both
            # directions: an interactive session's turn root carries a parent
            # from the traceparent Copilot injects, and a later turn's root can
            # chain onto an earlier turn's root in the same file, so either would
            # report a turn as a sub-agent. The plugin reads it the same way, in
            # internal/source/copilot — arrived at independently, because this
            # reader must not import it.
            if _spawned_by_a_tool(record, by_id):
                # Nested: a sub-agent. The plugin emits one span per sub-agent,
                # between the tool that spawned it and the tools it ran, so this
                # is a span-count expectation.
                out["subagents"] += 1
                continue
            turn = empty_usage()
            add(turn, attrs)
            turn["model"] = model
            turn["cost"] = attrs.get("github.copilot.cost")
            turn["rounds"] = attrs.get("github.copilot.turn_count")
            out["turns"].append(turn)
            add(out["agent"], attrs)
        elif op == "execute_tool":
            out["tools"][attrs.get("gen_ai.tool.name") or "<no name>"] += 1
            if (record.get("status") or {}).get("code") == 2:
                out["failed_tools"] += 1
            # A tool run inside a sub-agent has an invoke_agent somewhere above
            # it. The plugin emits such a tool under the sub-agent's own
            # invoke_agent span, so counting them here makes that check possible.
            parent = record.get("parentSpanId")
            seen = set()
            while parent and parent not in seen:
                seen.add(parent)
                above = by_id.get(parent)
                if above is None:
                    break
                if _spawned_by_a_tool(above, by_id):
                    out["subagent_tools"] += 1
                    break
                parent = above.get("parentSpanId")

    out["conversations"] = sorted(out["conversations"])
    out["record_types"] = dict(out["record_types"])
    out["spans"] = dict(out["spans"])
    out["tools"] = dict(out["tools"])
    return out, None


def report(data):
    print(f"otel      : {', '.join(data['files'])}")
    print(f"session   : {data['session_id'] or '(every conversation in the file)'}")
    if len(data["conversations"]) > 1:
        print(f"            the file holds {len(data['conversations'])} conversations:"
              f" {', '.join(data['conversations'])}")
    print(f"models    : {', '.join(data['models']) or '(none)'}")
    print(f"records   : {data['record_types']}")
    print(f"spans     : {data['spans'] or '(none)'}")
    if data["malformed_lines"]:
        print(f"WARNING   : {data['malformed_lines']} line(s) did not parse")

    turns = len(data["turns"])
    print(f"\nAgent turns: {turns}, over {data['chat_spans']} model round-trip(s)")
    print(f"  {'metric':<12}{'agent':>10}{'chat':>10}")
    for name in USAGE_KEYS:
        flag = "" if data["agent"][name] == data["chat"][name] else "  <-- Copilot disagrees with itself"
        print(f"  {name:<12}{data['agent'][name]:>10}{data['chat'][name]:>10}{flag}")
    print(f"  {'cost':<12}{sum(t['cost'] or 0 for t in data['turns']):>10}"
          f"{data['cost']:>10}")

    if turns > 1:
        print("\nPer turn (the plugin puts each of these on its own chat span)")
        for i, turn in enumerate(data["turns"], 1):
            print(f"  turn {i}: input {turn['input']}, output {turn['output']},"
                  f" cache_read {turn['cache_read']}, {turn['rounds']} round-trip(s)")

    if data["subagents"]:
        print(f"\nSub-agents: {data['subagents']}. Each gets its own invoke_agent span in"
              " Dash0,\n  under the tool that spawned it, carrying gen_ai.agent.name and"
              " gen_ai.agent.id.")

    print(f"\nTool executions: {data['tools'] or '(none)'}")
    if data["subagent_tools"]:
        print(f"  {data['subagent_tools']} of them ran inside a sub-agent. The plugin"
              " emits those under the\n  sub-agent's own invoke_agent span, which sits"
              " under the task span that spawned it,\n  so they are expected in Dash0 as"
              " well.")
    if data["failed_tools"]:
        print(f"  {data['failed_tools']} failed, and should carry an error status in Dash0.")

    if not turns:
        print("\nNo top-level invoke_agent span, so this file describes no completed"
              "\nturn. Read the usage as unavailable, not as zero.")
    print("\nThis file is the plugin's input as well as this channel's source, so an"
          "\nagreement with Dash0 proves the plugin copied it faithfully — not that"
          "\nCopilot measured the session correctly.")
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0],
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("target", help="a run directory, or one otel*.jsonl file")
    parser.add_argument("--session", default=None,
                        help="the gen_ai.conversation.id to scope to")
    parser.add_argument("--json", action="store_true", dest="as_json")
    args = parser.parse_args()

    session = args.session
    if session is None and os.path.isdir(args.target):
        manifest = os.path.join(args.target, "manifest.json")
        if os.path.exists(manifest):
            session = json.load(open(manifest)).get("session_id")

    paths = otel_files(args.target)
    if not paths:
        print(f"no otel*.jsonl in {args.target}. Either the run was driven with"
              " QA_COPILOT_NO_OTEL=1,\nor native OTel never wrote a file — in which case"
              " the plugin had no usage to read\neither, and its spans carry none.",
              file=sys.stderr)
        return 2

    data, error = read(paths, session)
    if error:
        print(error, file=sys.stderr)
        return 2
    if args.as_json:
        print(json.dumps(data, indent=2))
        return 0
    return report(data)


if __name__ == "__main__":
    sys.exit(main())
