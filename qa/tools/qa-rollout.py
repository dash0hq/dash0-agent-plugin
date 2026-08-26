#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
"""Read token usage out of a Codex rollout file, independently of the plugin.

This is the Codex runtime's second observation channel, and it stands where
claude-code-usage-audit.py stands for Claude Code. A rollout is Codex's own
record of a session, written by Codex, so a number computed from it owes nothing
to the plugin that also read it.

Independence has one rule and this file keeps it: nothing here imports or shells
out to internal/source/codex. The format is re-read from the file, so the two
readers can disagree — which is the entire point of having two.

What a rollout holds, as of codex-cli 0.149.1:

  session_meta   one, first: session_id, cli_version, cwd, originator
  turn_context   the model and settings a turn ran under
  response_item  the conversation, including function_call items (tool calls)
  event_msg      Codex's own events; token_count carries info.last_token_usage
  world_state    a workspace snapshot, ignored here

Token usage is a running series, one token_count per model round-trip, not a
total. Two figures therefore come out of one file, and they differ as soon as a
session has more than one turn:

  file    every token_count summed — the whole session
  turn    the counts since the last turn boundary — the last turn only

The plugin puts the TURN figure on each chat span, so a single-turn session is
the case where both agree and neither can hide a bug in the other. Prefer one
for a first run.

> The turn boundary is `event_msg` / `user_message`, which is what
> internal/source/codex/rollout.go resets on. Codex 0.149.1 was observed writing
> `task_started` instead on an exec session, so a rollout with no `user_message`
> at all reports `turn_boundaries: 0` here and its `turn` figure equals its
> `file` figure. That is reported, never silently substituted: if a multi-turn
> session shows zero boundaries, the product's per-turn scoping is the thing to
> go and look at.

Usage:
  qa/tools/qa-rollout.py qa/runs/<run-id>/rollout.jsonl
  qa/tools/qa-rollout.py qa/runs/<run-id>/rollout.jsonl --json

Exit codes:
  0  the file was read
  2  it could not be read, or it is compressed and therefore unreadable here
"""

import argparse
import collections
import json
import sys

# The four counts Codex reports per model round-trip, mapped to the names the
# rest of the harness uses. input is INCLUSIVE of the cached part, so cache_read
# is a subset of it and must never be subtracted.
USAGE_FIELDS = {
    "input": "input_tokens",
    "cache_read": "cached_input_tokens",
    "output": "output_tokens",
    "reasoning": "reasoning_output_tokens",
}


def empty_usage():
    return dict.fromkeys(USAGE_FIELDS, 0)


def add(into, wire):
    for name, field in USAGE_FIELDS.items():
        into[name] += wire.get(field) or 0


def read(path):
    """Everything one pass over a rollout yields, or an explanation."""
    if path.endswith(".zst"):
        return None, (f"{path} is a compressed rollout. Neither this tool nor the plugin"
                      " reads zstd, so usage is unavailable from this run rather than"
                      " zero. The plugin marks such a span dash0.codex.rollout.compressed.")
    try:
        handle = open(path)
    except OSError as err:
        return None, f"cannot read {path}: {err}"

    out = {
        "path": path,
        "session_id": None,
        "cli_version": None,
        "record_types": collections.Counter(),
        "event_types": collections.Counter(),
        "models": [],
        "tool_calls": collections.Counter(),
        "token_count_events": 0,
        "turn_boundaries": 0,
        "file": empty_usage(),
        "turn": empty_usage(),
        "malformed_lines": 0,
    }
    with handle:
        for line in handle:
            line = line.strip()
            if not line:
                continue
            try:
                record = json.loads(line)
            except json.JSONDecodeError:
                # Counted, not skipped in silence: a rollout that does not parse
                # is the most interesting thing this tool can find.
                out["malformed_lines"] += 1
                continue
            kind = record.get("type")
            out["record_types"][kind] += 1
            payload = record.get("payload") or {}

            if kind == "session_meta":
                out["session_id"] = payload.get("session_id") or payload.get("id")
                out["cli_version"] = payload.get("cli_version")
            elif kind == "turn_context":
                model = payload.get("model")
                if model and model not in out["models"]:
                    out["models"].append(model)
            elif kind == "response_item":
                if payload.get("type") == "function_call":
                    out["tool_calls"][payload.get("name") or "<no name>"] += 1
            elif kind == "event_msg":
                event = payload.get("type")
                out["event_types"][event] += 1
                if event == "user_message":
                    out["turn_boundaries"] += 1
                    out["turn"] = empty_usage()
                elif event == "token_count":
                    usage = (payload.get("info") or {}).get("last_token_usage") or {}
                    out["token_count_events"] += 1
                    add(out["file"], usage)
                    add(out["turn"], usage)

    out["record_types"] = dict(out["record_types"])
    out["event_types"] = dict(out["event_types"])
    out["tool_calls"] = dict(out["tool_calls"])
    return out, None


def report(data):
    print(f"rollout   : {data['path']}")
    print(f"session   : {data['session_id']} (codex-cli {data['cli_version']})")
    print(f"models    : {', '.join(data['models']) or '(none)'}")
    print(f"records   : {data['record_types']}")
    if data["malformed_lines"]:
        print(f"WARNING   : {data['malformed_lines']} line(s) did not parse")

    print(f"\ntoken_count events: {data['token_count_events']}"
          f", turn boundaries: {data['turn_boundaries']}")
    print(f"  {'metric':<12}{'file':>10}{'turn':>10}")
    for name in USAGE_FIELDS:
        print(f"  {name:<12}{data['file'][name]:>10}{data['turn'][name]:>10}")

    if not data["token_count_events"]:
        print("\nNo token_count event, so this session reports no usage at all. An"
              "\ninterrupted or failed turn does this; so does a session that never"
              "\nreached the model. Read it as unavailable, not as zero.")
    elif data["turn_boundaries"] == 0:
        print("\nNo user_message boundary in the file, so `turn` equals `file`. On a"
              "\nsingle-turn session that is correct. On a multi-turn one it means the"
              "\nturn-scoping rule the plugin uses has nothing to key on — go and look.")

    print(f"\nTool calls in the rollout: {data['tool_calls'] or '(none)'}")
    print("Informational only. The hook record is what says how many execute_tool")
    print("spans to expect; this is a second opinion on the same session, not the")
    print("expectation.")
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0],
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("rollout")
    parser.add_argument("--json", action="store_true", dest="as_json")
    args = parser.parse_args()

    data, error = read(args.rollout)
    if error:
        print(error, file=sys.stderr)
        return 2
    if args.as_json:
        print(json.dumps(data, indent=2))
        return 0
    return report(data)


if __name__ == "__main__":
    sys.exit(main())
