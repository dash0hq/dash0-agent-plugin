#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
"""Audit a Claude Code session's token usage from the local transcripts.

Reconstructs per-model token counts for the main session and for each sub-agent
it spawned. Reads only Claude Code's own transcript files under
~/.claude/projects, so it works on any already-finished session with no
telemetry, debug flag, or plugin involvement required. Claude Code only — the
other supported agents record their usage elsewhere.

Use it to compare three sets of token counts that should agree:

  1. what Claude Code itself reports  (the /usage command)
  2. what this script reconstructs    (ground truth, from the transcripts)
  3. what arrived in Dash0            (the spans for the session)

A gap between 2 and 3 localizes missing telemetry — in particular, whether the
usage sits in sub-agent transcripts whose spans never arrived.

Cost is deliberately not computed: prices and cache-write tiers change, and a
pricing table here would drift from the one the backend applies. Compare the
token counts the cost is derived from instead. Cache writes are reported split by
the TTL recorded in the transcript, since the two tiers are billed differently.

Usage:
  python3 scripts/claude-code-usage-audit.py                 # list recent sessions
  python3 scripts/claude-code-usage-audit.py <SESSION_ID>    # audit one session
  python3 scripts/claude-code-usage-audit.py <SESSION_ID> --json

The session id is the `gen_ai.conversation.id` on the spans in Dash0, and the
transcript filename on disk.
"""

import argparse
import glob
import json
import os
import sys

PROJECTS_DIR = os.path.join(os.path.expanduser("~"), ".claude", "projects")

# Model reported on messages Claude Code generates locally rather than by calling
# the API (for example a cancellation notice). Not billed.
SYNTHETIC_MODEL = "<synthetic>"


class Usage:
    """Token counts for one or more API calls, with cache writes split by TTL."""

    FIELDS = ("input", "output", "cache_write_5m", "cache_write_1h", "cache_read",
              "calls")

    def __init__(self):
        for field in self.FIELDS:
            setattr(self, field, 0)

    @property
    def cache_write(self):
        return self.cache_write_5m + self.cache_write_1h

    def add(self, other):
        for field in self.FIELDS:
            setattr(self, field, getattr(self, field) + getattr(other, field))

    @property
    def total_tokens(self):
        return (self.input + self.output + self.cache_write_5m
                + self.cache_write_1h + self.cache_read)

    def as_dict(self):
        out = {field: getattr(self, field) for field in self.FIELDS}
        out["cache_write"] = self.cache_write
        return out


def parse_usage(raw):
    """Convert one transcript `usage` object into a Usage.

    When a request was retried on a fallback model, the top-level counts mirror
    only the final attempt while `iterations` lists every billed attempt, so the
    iterations are summed instead. This matches how the plugin reads usage.
    """
    iterations = raw.get("iterations")
    if isinstance(iterations, list) and len(iterations) > 1:
        total = Usage()
        for iteration in iterations:
            total.add(parse_usage(iteration))
        return total

    usage = Usage()
    usage.input = raw.get("input_tokens") or 0
    usage.output = raw.get("output_tokens") or 0
    usage.cache_read = raw.get("cache_read_input_tokens") or 0

    # Report cache writes per TTL, since the tiers are billed differently. When a
    # transcript lacks the breakdown, attribute the aggregate to the 1-hour tier,
    # which is the one Claude Code has been observed to use.
    total_write = raw.get("cache_creation_input_tokens") or 0
    split = raw.get("cache_creation")
    if isinstance(split, dict):
        usage.cache_write_5m = split.get("ephemeral_5m_input_tokens") or 0
        usage.cache_write_1h = split.get("ephemeral_1h_input_tokens") or 0
        counted = usage.cache_write_5m + usage.cache_write_1h
        if counted < total_write:  # unknown tier — assume 1h, as Claude Code does
            usage.cache_write_1h += total_write - counted
    else:
        usage.cache_write_1h = total_write
    return usage


def note_meta(entry, meta):
    """Track the session's time window and the Claude Code version that wrote it."""
    stamp = entry.get("timestamp")
    if isinstance(stamp, str) and stamp:
        if meta["first"] is None or stamp < meta["first"]:
            meta["first"] = stamp
        if meta["last"] is None or stamp > meta["last"]:
            meta["last"] = stamp
    version = entry.get("version")
    if isinstance(version, str) and version:
        meta["versions"].add(version)


def read_transcript(path, counts=None, is_main=False, meta=None):
    """Per-model Usage for one transcript file.

    One API call can be written as several entries — streaming splits a response
    across blocks, and each entry repeats that call's usage — so entries are
    deduplicated and only one usage object per call is counted. `message.id` is
    the reliable key: `requestId` is absent in some Claude Code versions, and the
    per-entry `uuid` differs between entries of the same call, so keying on
    either would count that call's tokens several times over.

    When `counts` is given, it is populated with the span counts the plugin
    should have emitted for this transcript (see count_spans).
    """
    per_request = {}
    try:
        handle = open(path, encoding="utf-8", errors="replace")
    except OSError as err:
        print(f"warning: cannot read {path}: {err}", file=sys.stderr)
        return {}

    with handle:
        for line in handle:
            line = line.strip()
            if not line:
                continue
            try:
                entry = json.loads(line)
            except ValueError:
                continue  # skip partially written or malformed lines
            if counts is not None:
                count_spans(entry, counts, is_main)
            if meta is not None:
                note_meta(entry, meta)
            if entry.get("type") != "assistant":
                continue
            message = entry.get("message") or {}
            raw_usage = message.get("usage")
            if not isinstance(raw_usage, dict):
                continue
            model = message.get("model") or "unknown"
            # Claude Code writes locally generated messages with model
            # "<synthetic>". They are not API calls, carry no real usage, and are
            # not billed, so they would only add an empty row.
            if model == SYNTHETIC_MODEL:
                continue
            key = message.get("id") or entry.get("requestId") or entry.get("uuid") \
                or len(per_request)
            per_request[key] = (model, raw_usage)

    totals = {}
    for model, raw_usage in per_request.values():
        entry_totals = totals.setdefault(model, Usage())
        entry_totals.add(parse_usage(raw_usage))
        entry_totals.calls += 1
    return totals


# Tool names that spawn a sub-agent. Each such call yields both an
# execute_tool span for the call and an invoke_agent span for the sub-agent
# itself; the name in the transcript has varied across Claude Code versions.
SUBAGENT_TOOL_NAMES = {"agent", "task"}


def count_spans(entry, counts, is_main):
    """Tally the spans the plugin should emit for one transcript entry.

    The plugin emits one `chat` span per user turn (on the Stop hook), one
    `execute_tool` span per tool call, and one `invoke_agent` span per sub-agent
    (on SubagentStop). Counting those triggers across the main transcript and the
    sub-agent transcripts gives the number of spans Dash0 should hold.

    `is_main` distinguishes the session transcript from a sub-agent's: a
    sub-agent's kickoff prompt also looks like a user message, but it produces an
    invoke_agent span, not a chat span, so only the main transcript adds turns.
    """
    if entry.get("type") == "user":
        if not is_main or entry.get("isMeta"):
            return
        content = (entry.get("message") or {}).get("content")
        # Tool results come back as user messages; only real prompts end a turn.
        if isinstance(content, str):
            counts["chat"] += 1
        elif isinstance(content, list):
            first = content[0] if content else {}
            if not (isinstance(first, dict) and first.get("type") == "tool_result"):
                counts["chat"] += 1
        return

    if entry.get("type") != "assistant":
        return
    for block in (entry.get("message") or {}).get("content") or []:
        if not isinstance(block, dict) or block.get("type") != "tool_use":
            continue
        if (block.get("name") or "").lower() in SUBAGENT_TOOL_NAMES:
            counts["invoke_agent"] += 1
        counts["execute_tool"] += 1


def find_main_transcript(session_id):
    matches = glob.glob(os.path.join(PROJECTS_DIR, "*", f"{session_id}.jsonl"))
    return matches[0] if matches else None


def find_subagent_transcripts(main_path, session_id):
    session_dir = os.path.join(os.path.dirname(main_path), session_id, "subagents")
    return sorted(glob.glob(os.path.join(session_dir, "*.jsonl")))


def recent_sessions(limit):
    found = []
    for path in glob.glob(os.path.join(PROJECTS_DIR, "*", "*.jsonl")):
        if f"{os.sep}subagents{os.sep}" in path:
            continue  # sub-agent transcripts are reported under their session
        try:
            found.append((os.path.getmtime(path), path))
        except OSError:
            continue
    found.sort(reverse=True)
    return found[:limit]


def merge(into, totals):
    for model, usage in totals.items():
        into.setdefault(model, Usage()).add(usage)


def print_rows(label, totals):
    if not totals:
        print(f"  {label:<14} (no usage recorded)")
        return
    for model, usage in sorted(totals.items()):
        print(
            f"  {label:<14} {model:<22}"
            f" calls={usage.calls:>4}"
            f" in={usage.input:>8}"
            f" out={usage.output:>8}"
            f" cache_write={usage.cache_write:>9}"
            f" (5m={usage.cache_write_5m:>8} 1h={usage.cache_write_1h:>8})"
            f" cache_read={usage.cache_read:>11}"
        )


def total_tokens(totals):
    return sum(usage.total_tokens for usage in totals.values())


def audit(session_id):
    """Collect (main_path, subagent_paths, main_totals, subagent_totals, counts)."""
    main_path = find_main_transcript(session_id)
    if main_path is None:
        return None
    subagent_paths = find_subagent_transcripts(main_path, session_id)
    counts = {"chat": 0, "execute_tool": 0, "invoke_agent": 0}
    meta = {"first": None, "last": None, "versions": set()}
    main_totals = read_transcript(main_path, counts, is_main=True, meta=meta)
    # A sub-agent's own tool calls are spans too, and they live in its transcript.
    subagent_totals = [(path, read_transcript(path, counts, meta=meta))
                       for path in subagent_paths]
    return main_path, subagent_paths, main_totals, subagent_totals, counts, meta


def report_text(session_id, result):
    main_path, subagent_paths, main_totals, subagent_totals, counts, meta = result

    print(f"session   : {session_id}")
    print(f"transcript: {main_path}")
    print(f"sub-agents: {len(subagent_paths)}")
    if meta["first"] and meta["last"]:
        print(f"window    : {meta['first']} .. {meta['last']}  (UTC)")
    if meta["versions"]:
        print(f"claude code: {', '.join(sorted(meta['versions']))}")

    print("\nMain session")
    print_rows("main", main_totals)

    print("\nSub-agents")
    if not subagent_totals:
        print("  (none — no sub-agent transcripts on disk for this session)")
    for path, totals in subagent_totals:
        label = os.path.basename(path).removesuffix(".jsonl")[:14]
        print_rows(label, totals)

    grand = {}
    merge(grand, main_totals)
    for _, totals in subagent_totals:
        merge(grand, totals)

    print("\nTotal (main + sub-agents)")
    print_rows("TOTAL", grand)

    subagent_only = {}
    for _, totals in subagent_totals:
        merge(subagent_only, totals)

    if subagent_only:
        grand_tokens = total_tokens(grand)
        sub_tokens = total_tokens(subagent_only)
        share = (sub_tokens / grand_tokens * 100) if grand_tokens else 0.0
        # Keep a decimal below 10% so a small-but-nonzero share is not shown as 0%.
        formatted = f"{share:.0f}%" if share >= 10 else f"{share:.1f}%"
        print(f"\nSub-agents account for {sub_tokens:,} of {grand_tokens:,} tokens ({formatted})")

    total_spans = sum(counts.values())
    print("\nSpans Dash0 should hold for this session")
    print(f"  chat          {counts['chat']:>5}   (one per user turn)")
    print(f"  execute_tool  {counts['execute_tool']:>5}   (one per tool call, incl. sub-agents')")
    print(f"  invoke_agent  {counts['invoke_agent']:>5}   (one per sub-agent)")
    print(f"  TOTAL         {total_spans:>5}")

    print("\nCompare with:")
    print("  - Claude Code's own numbers: run /usage in that session")
    print("  - Dash0: the spans whose")
    print(f"    gen_ai.conversation.id = {session_id}")
    print("  Fewer spans in Dash0 than above, or usage that appears above but not")
    print("  in Dash0, means telemetry never arrived. Sub-agent rows correspond to")
    print("  the invoke_agent spans.")


def report_json(session_id, result):
    main_path, subagent_paths, main_totals, subagent_totals, counts, meta = result

    def encode(totals):
        return {model: usage.as_dict() for model, usage in sorted(totals.items())}

    grand = {}
    merge(grand, main_totals)
    for _, totals in subagent_totals:
        merge(grand, totals)

    print(json.dumps({
        "session_id": session_id,
        "transcript": main_path,
        "subagent_count": len(subagent_paths),
        "main": encode(main_totals),
        "subagents": [
            {"transcript": path, "usage": encode(totals)}
            for path, totals in subagent_totals
        ],
        "total": encode(grand),
        "total_tokens": total_tokens(grand),
        "expected_spans": dict(counts, total=sum(counts.values())),
        "first_event": meta["first"],
        "last_event": meta["last"],
        "claude_code_versions": sorted(meta["versions"]),
    }, indent=2))


def main():
    parser = argparse.ArgumentParser(
        description="Audit a Claude Code session's token usage from local transcripts.",
    )
    parser.add_argument("session_id", nargs="?",
                        help="session id to audit; omit to list recent sessions")
    parser.add_argument("--json", action="store_true", dest="as_json",
                        help="emit machine-readable JSON")
    parser.add_argument("--limit", type=int, default=15, metavar="N",
                        help="how many sessions to list (default: 15)")
    args = parser.parse_args()

    if not os.path.isdir(PROJECTS_DIR):
        print(f"No Claude Code transcripts found at {PROJECTS_DIR}", file=sys.stderr)
        return 1

    if not args.session_id:
        sessions = recent_sessions(args.limit)
        if not sessions:
            print(f"No sessions found under {PROJECTS_DIR}", file=sys.stderr)
            return 1
        print("Recent sessions (newest first). Pass one as the argument:\n")
        for _, path in sessions:
            print(f"  {os.path.basename(path).removesuffix('.jsonl')}   {path}")
        return 0

    session_id = args.session_id.strip().removesuffix(".jsonl")
    result = audit(session_id)
    if result is None:
        print(f"No transcript for session {session_id!r} under {PROJECTS_DIR}.",
              file=sys.stderr)
        print("Run without arguments to list available sessions.", file=sys.stderr)
        return 1

    if args.as_json:
        report_json(session_id, result)
    else:
        report_text(session_id, result)
    return 0


if __name__ == "__main__":
    sys.exit(main())
