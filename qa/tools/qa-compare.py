#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
"""Compare the spans Dash0 stored against three records the plugin never wrote.

Reads one run directory from qa/tools/qa-session.sh and lines up:

  dash0       spans read back with `dash0 spans query`, filtered to this session
  hooks       what the plugin was fed: record/index.jsonl and the event payloads
  transcript  claude-code-usage-audit.py over Claude Code's own transcript
  claude      the usage block in `claude -p --output-format json`

Only the first is the product's output. `hooks` is the pipeline's own input, so a
span missing there is a span the plugin was never asked to make; a span present
in `hooks` and absent from `dash0` is the plugin's or the transport's fault. That
distinction is the point of recording the input at all.

Reads qa/config.local.json for the API endpoint, token, and dataset. It does
not use the dash0 CLI's active profile: that profile carries its own dataset,
and reading the wrong one returns an empty result that looks exactly like the
plugin having sent nothing.

Usage:
  qa/tools/qa-compare.py qa/runs/<run-id>
  qa/tools/qa-compare.py qa/runs/<run-id> --json
  qa/tools/qa-compare.py qa/runs/<run-id> --dataset otlp-test
"""

import argparse
import collections
import json
import os
import subprocess
import sys

OP = "gen_ai.operation.name"
MODEL = "gen_ai.request.model"
TOOL = "gen_ai.tool.name"
CONV = "gen_ai.conversation.id"
USAGE_KEYS = {
    "input": "gen_ai.usage.input_tokens",
    "output": "gen_ai.usage.output_tokens",
    "cache_read": "gen_ai.usage.cache_read.input_tokens",
    "cache_write": "gen_ai.usage.cache_creation.input_tokens",
}

# Which hook events the pipeline turns into which span. Derived from
# internal/pipeline/pipeline.go; if that mapping changes, this is the line to fix.
SPAN_FROM_HOOK = {
    "execute_tool": ("PostToolUse", "PostToolUseFailure"),
    "chat": ("Stop", "StopFailure"),
    "invoke_agent": ("SubagentStop",),
}


def attr_value(value):
    for key in ("stringValue", "boolValue"):
        if key in value:
            return value[key]
    if "intValue" in value:
        return int(value["intValue"])
    if "doubleValue" in value:
        return float(value["doubleValue"])
    return None


def load_config(root):
    """qa/config.local.json, or an explanation of why it cannot be used."""
    path = os.path.join(root, "qa", "config.local.json")
    if not os.path.exists(path):
        return None, (f"{path} does not exist. Copy qa/config.local.json.example"
                      " to it and fill in the values.")
    with open(path) as handle:
        try:
            config = json.load(handle)
        except json.JSONDecodeError as err:
            return None, f"{path} is not valid JSON: {err}"
    missing = [k for k in ("apiUrl", "authToken", "dataset") if not config.get(k)]
    if missing:
        return None, f"{path} is missing: {', '.join(missing)}"
    if "REPLACE_ME" in config["authToken"]:
        return None, f"{path} still has the placeholder authToken."
    return config, None


def query_dash0(config, session_id, dataset, since, until, limit):
    """Every span Dash0 holds for this session, as attribute dicts.

    --precision disabled is not optional: adaptive sampling would drop spans and
    the drop would read as the plugin never sending them.
    """
    cmd = [
        "dash0", "spans", "query",
        "--api-url", config["apiUrl"],
        "--auth-token", config["authToken"],
        "--dataset", dataset,
        "--precision", "disabled",
        "--filter", f"{CONV} is {session_id}",
        "--from", since, "--to", until,
        "--limit", str(limit),
        "-o", "json",
    ]
    proc = subprocess.run(cmd, capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        detail = proc.stdout.strip() or proc.stderr.strip()
        # The token is in argv, so the command is rebuilt for display with it
        # removed. A failing check gets pasted into reports.
        shown = [("<authToken>" if arg == config["authToken"] else arg) for arg in cmd]
        return None, f"{' '.join(shown)}\n{detail}"
    payload = json.loads(proc.stdout)
    spans = []
    for resource_span in payload.get("resourceSpans") or []:
        resource = {a["key"]: attr_value(a.get("value", {}))
                    for a in resource_span.get("resource", {}).get("attributes") or []}
        for scope_span in resource_span.get("scopeSpans") or []:
            for span in scope_span.get("spans") or []:
                attrs = {a["key"]: attr_value(a.get("value", {}))
                         for a in span.get("attributes") or []}
                spans.append({"name": span.get("name", ""), "attrs": attrs,
                              "resource": resource})
    return spans, None


def dash0_summary(spans):
    counts = collections.Counter()
    usage = {}
    tools = collections.Counter()
    services = set()
    for span in spans:
        attrs = span["attrs"]
        op = attrs.get(OP)
        counts[op] += 1
        services.add(span["resource"].get("service.name"))
        if op == "execute_tool":
            tools[attrs.get(TOOL) or "<no name>"] += 1
        if op in ("chat", "invoke_agent"):
            row = usage.setdefault(attrs.get(MODEL) or "<no model>",
                                   dict.fromkeys(USAGE_KEYS, 0))
            for name, key in USAGE_KEYS.items():
                row[name] += attrs.get(key) or 0
    return {"spans": dict(counts), "total": len(spans), "usage": usage,
            "tools": dict(tools), "services": sorted(s for s in services if s)}


def hooks_summary(run_dir, session_id):
    """The expectation the plugin's own input implies, with no plugin involved.

    Scoped to one session, because the recorder appends and a reused run id
    therefore holds every session ever recorded into it. The Dash0 side is
    filtered to `session_id`, so counting hooks across all of them reported the
    surplus as telemetry the plugin failed to send. Payloads come from each row's
    own `event_file` rather than from a glob over the directory, which is what
    keeps the two halves in step.
    """
    index = os.path.join(run_dir, "record", "index.jsonl")
    if not os.path.exists(index):
        return {"error": "no record/index.jsonl; was the recorder registered?"}
    all_rows = [json.loads(line) for line in open(index)]
    rows = [r for r in all_rows if r.get("session_id") == session_id]
    # A payload that did not parse has no session id, so it cannot be attributed.
    # Counting it separately keeps a recording failure visible instead of
    # dropping it as somebody else's session.
    unattributed = sum(1 for r in all_rows if not r.get("session_id"))
    by_event = collections.Counter(r["hook_event_name"] for r in rows)

    tools = collections.Counter()
    for row in rows:
        if "PostToolUse" not in row["hook_event_name"]:
            continue
        path = os.path.join(run_dir, "record", row.get("event_file") or "")
        try:
            with open(path) as handle:
                tools[json.load(handle).get("tool_name") or "<no name>"] += 1
        except (OSError, json.JSONDecodeError):
            tools["<unparseable>"] += 1

    expected = {span: sum(by_event[h] for h in hooks)
                for span, hooks in SPAN_FROM_HOOK.items()}
    snapshots = {r.get("transcript_sha256") for r in rows if r.get("transcript_sha256")}
    return {
        "invocations": len(rows),
        "other_sessions": len(all_rows) - len(rows) - unattributed,
        "unattributed": unattributed,
        "by_event": dict(by_event),
        "expected_spans": dict(expected, total=sum(expected.values())),
        "tools": dict(tools),
        "transcript_snapshots": len(snapshots),
        # Absent is not an error: Claude Code names the transcript before it
        # writes it, so the first few hooks legitimately point at nothing.
        "absent": sum(1 for r in rows if r.get("transcript_absent")),
        "errors": [r for r in rows if r.get("transcript_error")],
    }


def transcript_summary(root, session_id):
    script = os.path.join(root, "claude", "tools", "claude-code-usage-audit.py")
    proc = subprocess.run([sys.executable, script, session_id, "--json"],
                          capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        return {"error": proc.stderr.strip() or f"audit exited {proc.returncode}"}
    return json.loads(proc.stdout)


def claude_summary(run_dir):
    path = os.path.join(run_dir, "claude-result.json")
    if not os.path.exists(path):
        return {"error": "no claude-result.json in the run"}
    with open(path) as handle:
        try:
            result = json.load(handle)
        except json.JSONDecodeError as err:
            return {"error": f"unparseable: {err}"}
    usage = result.get("usage") or {}
    return {
        "cost_usd": result.get("total_cost_usd"),
        "num_turns": result.get("num_turns"),
        "is_error": result.get("is_error"),
        "input": usage.get("input_tokens"),
        "output": usage.get("output_tokens"),
        "cache_read": usage.get("cache_read_input_tokens"),
        "cache_write": usage.get("cache_creation_input_tokens"),
        "models": sorted((result.get("modelUsage") or {}).keys()),
    }


def totals(rows, keys):
    out = dict.fromkeys(keys, 0)
    for row in rows.values():
        for key in keys:
            out[key] += row.get(key, 0) or 0
    return out


def report(data):
    manifest, dash0 = data["manifest"], data["dash0"]
    hooks, transcript, claude = data["hooks"], data["transcript"], data["claude"]
    findings = []

    print(f"run       : {data['run_dir']}")
    print(f"session   : {manifest.get('session_id')}")
    print(f"under test: {manifest.get('binary_under_test')}")
    print(f"dataset   : {data['dataset']} at {data['api_url']}")
    print(f"window    : {data['since']} .. {data['until']}")

    if data.get("dash0_error"):
        print(f"\nERROR: reading spans from Dash0 failed.\n{data['dash0_error']}")
        print("\nEverything below that depends on Dash0 is unavailable, not zero.")
        return 2

    print(f"spans     : {dash0['total']} in Dash0, from service(s) "
          f"{', '.join(dash0['services']) or '(none)'}")
    if dash0["total"] >= data["limit"]:
        print(f"\nWARNING: the query returned {dash0['total']} spans, its limit. The"
              " result is truncated,\nso every count below is a floor. Split the"
              " session or query it in time slices.")
    if hooks.get("error"):
        print(f"\nERROR: {hooks['error']}")
        return 2
    print(f"hooks     : {hooks['invocations']} invocations recorded, "
          f"{hooks['transcript_snapshots']} distinct transcript snapshots, "
          f"{hooks['absent']} before the transcript existed")
    if hooks["other_sessions"]:
        print(f"            {hooks['other_sessions']} invocation(s) from an earlier"
              " session in this directory, ignored.\n            The run id was"
              " reused; use a fresh one so the record holds one session.")
    if hooks["unattributed"]:
        print(f"            {hooks['unattributed']} invocation(s) carry no session id,"
              " so they cannot be attributed.\n            A payload that did not"
              " parse is a recording failure, not another session.")

    print("\nSpan counts")
    print(f"  {'type':<14}{'dash0':>8}{'hooks':>8}{'transcript':>12}")
    tx_expected = transcript.get("expected_spans", {})
    for kind in ("chat", "execute_tool", "invoke_agent", "total"):
        got = dash0["total"] if kind == "total" else dash0["spans"].get(kind, 0)
        want_hooks = hooks["expected_spans"].get(kind, 0)
        want_tx = tx_expected.get(kind, 0)
        flag = ""
        if got != want_hooks:
            flag = "  <-- differs from the hooks it was fed"
            findings.append(f"{kind}: Dash0 has {got}, the hooks imply {want_hooks}")
        elif got != want_tx:
            flag = "  <-- differs from the transcript"
            findings.append(f"{kind}: Dash0 has {got}, the transcript implies {want_tx}")
        print(f"  {kind:<14}{got:>8}{want_hooks:>8}{want_tx:>12}{flag}")

    print("\nTool spans")
    names = sorted(set(dash0["tools"]) | set(hooks["tools"]))
    if not names:
        print("  (none)")
    for name in names:
        got, want = dash0["tools"].get(name, 0), hooks["tools"].get(name, 0)
        flag = "" if got == want else "  <-- differs"
        if got != want:
            findings.append(f"tool {name}: Dash0 has {got}, PostToolUse fired {want}")
        print(f"  {name:<20}{got:>6}{want:>6}{flag}")

    print("\nTokens")
    keys = ("input", "output", "cache_read", "cache_write")
    d0 = totals(dash0["usage"], keys)
    tx = totals(transcript.get("total", {}), keys)
    print(f"  {'metric':<14}{'dash0':>10}{'transcript':>12}{'claude':>10}")
    for key in keys:
        cc = claude.get(key)
        flag = "" if d0[key] == tx.get(key, 0) else "  <-- differs"
        if d0[key] != tx.get(key, 0):
            findings.append(f"{key} tokens: Dash0 {d0[key]}, transcript {tx.get(key, 0)}")
        print(f"  {key:<14}{d0[key]:>10}{tx.get(key, 0):>12}"
              f"{cc if cc is not None else '-':>10}{flag}")

    print("\nModels")
    print(f"  dash0     : {', '.join(sorted(dash0['usage'])) or '(none)'}")
    print(f"  transcript: {', '.join(sorted(transcript.get('total', {}))) or '(none)'}")
    print(f"  claude    : {', '.join(claude.get('models') or []) or '(none)'}")

    if claude.get("cost_usd") is not None:
        print(f"\nClaude Code's own figures: ${claude['cost_usd']:.4f} over "
              f"{claude.get('num_turns')} turn(s), is_error={claude.get('is_error')}")

    if hooks.get("errors"):
        print(f"\nWARNING: the recorder could not read a transcript on "
              f"{len(hooks['errors'])} invocation(s); those events have no snapshot.")

    print()
    if findings:
        print(f"{len(findings)} difference(s):")
        for finding in findings:
            print(f"  - {finding}")
        print("\nA model in `claude` but not in `dash0` is the known auxiliary-model")
        print("gap (claude/README.md), not a new finding.")
        return 1
    print("All three records agree.")
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("run_dir")
    parser.add_argument("--dataset", default=None,
                        help="override the dataset from qa/config.local.json")
    # The CLI refuses JSON output above 100 records, so 100 is the ceiling for
    # this channel. A session that hits it is reported as truncated rather than
    # silently under-counted.
    parser.add_argument("--limit", type=int, default=100)
    parser.add_argument("--json", action="store_true", dest="as_json")
    args = parser.parse_args()

    run_dir = os.path.abspath(args.run_dir)
    manifest_path = os.path.join(run_dir, "manifest.json")
    if not os.path.exists(manifest_path):
        print(f"no manifest.json in {run_dir}; is that a run directory?", file=sys.stderr)
        return 2
    manifest = json.load(open(manifest_path))

    root = subprocess.run(["git", "rev-parse", "--show-toplevel"],
                          capture_output=True, text=True, check=True).stdout.strip()
    config, config_error = load_config(root)
    if config_error:
        print(f"qa/config.local.json is unusable, so no span can be read:\n"
              f"  {config_error}", file=sys.stderr)
        return 2
    dataset = args.dataset or config["dataset"]
    # Bound the query by the run's own window, widened by a minute at each end for
    # ingest lag and clock skew. An unbounded query would pick up a re-run of the
    # same pinned session id.
    since = manifest.get("started_at") or "now-1h"
    until = manifest.get("ended_at") or "now"
    spans, error = query_dash0(config, manifest["session_id"], dataset,
                               widen(since, -60), widen(until, 120), args.limit)

    data = {
        "run_dir": run_dir,
        "dataset": dataset,
        "api_url": config["apiUrl"],
        "limit": args.limit,
        "since": since,
        "until": until,
        "manifest": manifest,
        "dash0": dash0_summary(spans or []),
        "dash0_error": error,
        "hooks": hooks_summary(run_dir, manifest["session_id"]),
        "transcript": transcript_summary(root, manifest["session_id"]),
        "claude": claude_summary(run_dir),
    }

    if args.as_json:
        print(json.dumps(data, indent=2))
        return 0
    return report(data)


def widen(stamp, seconds):
    """Shift an ISO stamp by seconds. Relative stamps like now-1h pass through."""
    if stamp.startswith("now"):
        return stamp
    from datetime import datetime, timedelta, timezone
    parsed = datetime.strptime(stamp, "%Y-%m-%dT%H:%M:%S.%f%z")
    shifted = parsed.astimezone(timezone.utc) + timedelta(seconds=seconds)
    return shifted.strftime("%Y-%m-%dT%H:%M:%S.000Z")


if __name__ == "__main__":
    sys.exit(main())
