#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
"""Read a Cursor agent transcript, which is the cursor runtime's second channel.

Cursor writes one JSONL transcript per conversation under
~/.cursor/projects/<slug>/agent-transcripts/<id>/. The driver copies it into the
run directory as transcript.jsonl. It is Cursor's own record, written by the
host, and the plugin never reads it — internal/source/cursor works from hook
payloads alone. That makes it genuinely independent for what it does carry.

What it carries, and what it does not:

  turns       one user entry wrapping a <user_query> per turn   independent
  tool calls  one `tool_use` block per call, with its name      independent
  usage       nothing. No token count appears anywhere in it.

The turn count is taken from the submitted prompts, not from the `turn_ended`
marker, which was the obvious choice and is wrong. Measured 2026-09-01 on
qa/runs/setup-probe-cursor-turns: a two-turn session wrote six message entries
and exactly ONE `turn_ended`, at the end of the last turn. So that marker ends the
agent loop rather than a turn, and reading it as a turn count reported a healthy
two-turn session as one chat span too many. Cursor wraps every submitted prompt
in <user_query>, which no tool result or system entry carries, so counting those
is both exact and unambiguous.

The gap in that table is the cursor runtime's defining limit. Tokens exist in
exactly one place, the afterAgentResponse hook payload, and that payload is also
the plugin's input. So a cursor run has no independent reading of a token count
at all — weaker than Claude, where the transcript is a true second measurement,
and weaker even than Copilot, whose OTel file at least comes from the host's own
instrumentation. Do not report a cursor token figure as corroborated.

Usage:
  qa/tools/qa-transcript-cursor.py qa/runs/<run-id>
  qa/tools/qa-transcript-cursor.py qa/runs/<run-id> --json
  qa/tools/qa-transcript-cursor.py path/to/transcript.jsonl
"""

import argparse
import collections
import glob
import json
import os
import sys


def blocks(entry):
    """The content blocks of a transcript entry, whatever shape it arrived in.

    Cursor nests the message under `message.content` as a list of typed blocks. A
    string content is accepted too: it costs one branch and it keeps a format
    change from reading as a session with no tool calls.
    """
    message = entry.get("message")
    if not isinstance(message, dict):
        return []
    content = message.get("content")
    if isinstance(content, str):
        return [{"type": "text", "text": content}]
    if isinstance(content, list):
        return [b for b in content if isinstance(b, dict)]
    return []


def read(paths):
    tools = collections.Counter()
    prompts = 0
    loop_ends = 0
    loop_status = collections.Counter()
    user_messages = 0
    assistant_texts = 0
    unparseable = 0
    entries = 0

    for path in paths:
        with open(path) as handle:
            for line in handle:
                line = line.strip()
                if not line:
                    continue
                try:
                    entry = json.loads(line)
                except json.JSONDecodeError:
                    unparseable += 1
                    continue
                entries += 1
                # The loop marker is a bare typed record with no role, so it is
                # matched before the role branches rather than inside them.
                if entry.get("type") == "turn_ended":
                    loop_ends += 1
                    loop_status[entry.get("status") or "<none>"] += 1
                    continue
                role = entry.get("role")
                if role == "user":
                    user_messages += 1
                for block in blocks(entry):
                    kind = block.get("type")
                    if kind == "tool_use":
                        tools[block.get("name") or "<no name>"] += 1
                    elif kind == "text":
                        if role == "assistant":
                            assistant_texts += 1
                        elif role == "user" and "<user_query>" in (block.get("text") or ""):
                            prompts += 1

    return {
        "files": len(paths),
        "entries": entries,
        "turns": prompts,
        "loop_ends": loop_ends,
        "loop_status": dict(loop_status),
        "tools": dict(tools),
        "tool_calls": sum(tools.values()),
        "user_messages": user_messages,
        "assistant_texts": assistant_texts,
        "unparseable": unparseable,
        # Stated rather than implied. A caller that filled a token column with
        # zeros from this channel would be reporting agreement it never had.
        "no_usage": True,
    }


def resolve(target):
    """The transcript files behind a run directory or a single path."""
    if os.path.isfile(target):
        return [target], None
    if not os.path.isdir(target):
        return None, f"{target} is neither a file nor a directory"
    paths = sorted(glob.glob(os.path.join(target, "transcript.jsonl")) +
                   glob.glob(os.path.join(target, "transcript-*.jsonl")))
    if not paths:
        return None, (f"no transcript.jsonl in {target}. Cursor writes the"
                      " transcript under ~/.cursor/projects/<slug>/, and the driver"
                      " copies it in after the session; a run without one has no"
                      " second channel, which is not the same as a session with no"
                      " tool calls.")
    return paths, None


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("target", help="a run directory, or one transcript.jsonl")
    parser.add_argument("--json", action="store_true", dest="as_json")
    args = parser.parse_args()

    paths, error = resolve(args.target)
    if error:
        print(error, file=sys.stderr)
        return 2
    data = read(paths)

    if args.as_json:
        print(json.dumps(data, indent=2))
        return 0

    print(f"transcript files : {data['files']}")
    print(f"entries          : {data['entries']}"
          + (f" ({data['unparseable']} unparseable)" if data["unparseable"] else ""))
    print(f"turns            : {data['turns']} (submitted prompts)")
    print(f"loop ends        : {data['loop_ends']} {data['loop_status'] or ''}"
          "  — one per agent loop, not per turn")
    print(f"user messages    : {data['user_messages']}")
    print(f"assistant texts  : {data['assistant_texts']}")
    print(f"tool calls       : {data['tool_calls']}")
    for name, n in sorted(data["tools"].items()):
        print(f"  {name:<24}{n:>5}")
    print("\nNo token count appears in a Cursor transcript. Usage reaches the plugin"
          "\nthrough the afterAgentResponse hook only, which is the plugin's own input,"
          "\nso a cursor run has no independent reading of a token count.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
