#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
"""Compare the spans Dash0 stored against three records the plugin never wrote.

Reads one run directory from qa/tools/qa-session.sh or qa-session-codex.sh and
lines up four columns. Two of them are the same in both runtimes:

  dash0       spans read back with `dash0 spans query`, filtered to this session
  hooks       what the plugin was fed: record/index.jsonl and the event payloads

The other two are whatever the runtime's own records are. `manifest.json` names
the runtime and this tool follows it:

  claude      transcript  claude-code-usage-audit.py over the session transcript
              harness     the usage block in `claude -p --output-format json`
  codex       transcript  qa-rollout.py over the Codex rollout: usage only, no
                          span counts, so those cells read `-` rather than 0
              harness     what `codex exec --json` reported, plus the plugin's
                          own debug log, which only the Codex runtime has
  copilot     transcript  qa-otel.py over Copilot's native-OTel file
              harness     what `copilot --output-format json` reported, plus the
                          plugin's debug log

The Copilot runtime inverts one thing, and reading its table without knowing
that is how a healthy run gets filed as a bug. Its hooks carry no numbers and no
tool events the plugin consumes: a Copilot tool span is built from the
native-OTel file, not from a hook, and so is the invoke_agent span of every
sub-agent it spawns. So the `hooks` column expects a chat span and nothing else,
its other cells read `-`, and the tool and agent comparisons run against the
OTel channel instead. That channel is the plugin's own input as well
as an observation, so an agreement there is weaker than the one the other two
runtimes get — it proves a faithful copy, not a correct measurement.

Only `dash0` and `harness` are the product's output. `hooks` is the pipeline's
own input, so a span missing there is a span the plugin was never asked to make;
a span present in `hooks` and absent from `dash0` is the plugin's or the
transport's fault. That distinction is the point of recording the input at all.

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
import glob
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
    "reasoning": "gen_ai.usage.reasoning.output_tokens",
}

# A manifest with no runtime is a Claude run: the Claude driver predates the
# field, and its existing run directories still have to compare.
DEFAULT_RUNTIME = "claude"

# Which hook events the pipeline turns into which span, per runtime. Derived
# from internal/pipeline/pipeline.go and the runtime's own normalizer.
#
# Claude and Codex share one mapping: Codex reuses Claude's event names and
# internal/source/codex normalizes to the same vocabulary. Copilot needs its own
# for two reasons. Its payloads carry no event name at all, so the recorder
# writes the camelCase name the host passed as an argv; and its tool spans do not
# come from hooks — internal/source/copilot drops postToolUse deliberately,
# because those events carry no duration and never fire inside a sub-agent.
#
# A None means "this runtime's hooks say nothing about this span". It is not the
# same as zero, and the report prints `-` for it rather than comparing against a
# number nobody claimed.
SPAN_FROM_HOOK = {
    "claude": {
        "execute_tool": ("PostToolUse", "PostToolUseFailure"),
        "chat": ("Stop", "StopFailure"),
        "invoke_agent": ("SubagentStop",),
    },
    "copilot": {
        # Tool spans come from the native-OTel file; the OTel channel carries
        # that expectation.
        "execute_tool": None,
        "chat": ("agentStop",),
        # A Copilot sub-agent's hook session is dropped wholesale, so the hooks
        # imply nothing about invoke_agent spans either. The OTel file does.
        "invoke_agent": None,
    },
}
SPAN_FROM_HOOK["codex"] = SPAN_FROM_HOOK["claude"]

# Which recorded hook events name a tool call, per runtime. Copilot's are
# recorded but never consumed by the plugin, so they are a free second opinion
# rather than an expectation — the report says so where it prints them.
TOOL_HOOKS = {
    "claude": ("PostToolUse",),
    "codex": ("PostToolUse",),
    "copilot": ("postToolUse",),
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
                # The ids come along so parenting can be checked. Everything
                # else here compares counts and values, which is how a session
                # whose sub-agent spans hung outside the trace still reconciled.
                spans.append({"name": span.get("name", ""), "attrs": attrs,
                              "resource": resource,
                              "span_id": span.get("spanId", ""),
                              "parent_span_id": span.get("parentSpanId", "")})
    return spans, None


def mcp_tool_name(raw):
    """The tool name a hook payload carries, reduced to the one a span carries.

    An MCP call arrives at the hook as mcp__<server>__<tool>, and the plugin
    exports gen_ai.tool.name as <tool> with the server on its own attribute. The
    two sides of the tool table are therefore named differently for the same
    call, and without this every MCP call printed two rows, both flagged as
    differing, and the tool exited 1 on a healthy run.

    This mirrors NormalizeMCPToolName in internal/pipeline/pipeline.go, which
    makes the tool table's MCP rows deductive rather than independent: it is the
    harness agreeing with a documented rule so that the counts, ids and
    durations either side of it stay comparable. The rule itself is asserted by
    qa/specs/claude/, against the raw name in the payload.
    """
    if not raw.startswith("mcp__"):
        return raw
    parts = raw.split("__", 2)
    if len(parts) < 3 or not parts[2]:
        return raw
    return parts[2]


def span_tool_name(raw):
    """The tool name a payload carries, reduced to the one its span carries.

    Two renames happen between the hook and the span, and both sides of the tool
    table have to speak the same language or every affected call prints as two
    differing rows.

    The MCP one is below. The other is Codex's spawn call: anchorSpawnAgent in
    internal/source/codex renames it to `Agent`, because that span is the
    sub-agent's anchor and the name matches what Claude's Task tool produces.
    Codex namespaces it and has changed the prefix — bare `spawn_agent` in
    0.142.5, `collaborationspawn_agent` in 0.149.1 — so match the suffix, as the
    product does.

    This still catches a broken anchor, which is the point. If the rename does
    not happen the span keeps its raw name, and the row reads "Dash0 has 0
    Agent, PostToolUse fired 1" instead of quietly agreeing.
    """
    if raw.endswith("spawn_agent"):
        return "Agent"
    return mcp_tool_name(raw)


def orphans(spans):
    """Spans whose parent is not among the session's own spans.

    A span pointing at a parent nobody emitted is invisible to every other
    comparison here: the counts still reconcile, the attributes are all in the
    contract, and the trace is quietly broken. Both Codex parenting defects
    passed every other check in this tool and were found by eye.

    Cheap and exact, because a session's trace is self-contained. Every span the
    plugin builds is parented on another span of the same session — a tool call
    under its turn, a sub-agent's work under its agent's anchor — and the turn's
    `chat` span is the root, with no parent at all. There is no legitimate
    parent outside the set, so anything left over is a real hole.

    The caller must skip this on a truncated result: a missing span makes its
    children look orphaned when the only thing wrong is the query limit.
    """
    known = {s["span_id"] for s in spans if s["span_id"]}
    out = []
    for span in spans:
        parent = span["parent_span_id"]
        if parent and parent not in known:
            out.append({"name": span["name"],
                        "agent": span["attrs"].get("gen_ai.agent.id"),
                        "parent": parent})
    return out


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
            "tools": dict(tools), "services": sorted(s for s in services if s),
            "orphans": orphans(spans)}


def hooks_summary(run_dir, session_id, runtime):
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
    # A Copilot sub-agent fires its own hook lifecycle under a synthetic
    # call_<toolCallId> session that carries nothing linking back to the parent.
    # internal/source/copilot drops those wholesale rather than mint a
    # token-less conversation per sub-agent, so they are expected in the
    # recording and must not be reported as "the run id was reused".
    subagents = sum(1 for r in all_rows
                    if (r.get("session_id") or "").startswith("call_"))
    # A payload that did not parse has no session id, so it cannot be attributed.
    # Counting it separately keeps a recording failure visible instead of
    # dropping it as somebody else's session.
    unattributed = sum(1 for r in all_rows if not r.get("session_id"))
    if not rows:
        # Without this the filter turns a total recording failure into a pass:
        # zero hooks imply zero spans, Dash0 holds zero spans, and the three
        # records "agree" at zero. Whatever went wrong, it is not agreement.
        return {"error": (f"no recorded hook belongs to session {session_id}."
                          f" {len(all_rows)} row(s) in the index, of which"
                          f" {unattributed} could not be attributed. Either the"
                          " recorder never fired for this session or the"
                          " manifest names the wrong one.")}
    by_event = collections.Counter(r["hook_event_name"] for r in rows)

    tools = collections.Counter()
    tool_hooks = TOOL_HOOKS[runtime]
    for row in rows:
        if not any(h in row["hook_event_name"] for h in tool_hooks):
            continue
        path = os.path.join(run_dir, "record", row.get("event_file") or "")
        try:
            with open(path) as handle:
                payload = json.load(handle)
                # Copilot's camelCase payloads name it toolName; Claude and
                # Codex both use tool_name.
                raw = payload.get("tool_name") or payload.get("toolName") or "<no name>"
                tools[span_tool_name(raw)] += 1
        except (OSError, json.JSONDecodeError):
            tools["<unparseable>"] += 1

    expected = {span: (None if hooks is None else sum(by_event[h] for h in hooks))
                for span, hooks in SPAN_FROM_HOOK[runtime].items()}
    # A total is only a total when every part of it was claimed. On Copilot the
    # hooks say nothing about tool spans, so the sum of what they do say is a
    # floor, and printing it as a total reports every tool span as a surplus.
    expected["total"] = (None if any(v is None for v in expected.values())
                         else sum(expected.values()))
    snapshots = {r.get("transcript_sha256") for r in rows if r.get("transcript_sha256")}
    return {
        "invocations": len(rows),
        "subagent_sessions": subagents,
        "tools_are_an_expectation": SPAN_FROM_HOOK[runtime]["execute_tool"] is not None,
        "other_sessions": len(all_rows) - len(rows) - unattributed - subagents,
        "unattributed": unattributed,
        "by_event": dict(by_event),
        "expected_spans": expected,
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


def rollout_summary(root, run_dir):
    """The Codex runtime's second channel: usage from the rollout, and no counts.

    qa-rollout.py is run as a subprocess rather than imported, for the same
    reason claude-code-usage-audit.py is: it is a separate reader of a separate
    format, and keeping the process boundary keeps that visible.

    It reports no expected_spans. A rollout can be read for tool calls and turn
    boundaries, but the hook record already answers that question from the
    pipeline's own input, which is the stronger record. So the span-count column
    is absent for Codex rather than filled with a weaker second guess.
    """
    path = os.path.join(run_dir, "rollout.jsonl")
    if not os.path.exists(path):
        # A compressed session rollout keeps its .zst, so say which of the two
        # this is: unreadable and unavailable, or simply absent. Reporting a
        # compressed rollout as "no rollout" sends the reader after the driver.
        if os.path.exists(path + ".zst"):
            return {"error": "the session's rollout is compressed (.zst), so usage is"
                             " unavailable from it rather than zero. Neither the plugin nor"
                             " qa-rollout.py reads zstd; the plugin marks such a span"
                             " dash0.codex.rollout.compressed."}
        return {"error": "no rollout.jsonl in the run; the driver found no rollout file"}
    script = os.path.join(root, "qa", "tools", "qa-rollout.py")

    def read(rollout):
        proc = subprocess.run([sys.executable, script, rollout, "--json"],
                              capture_output=True, text=True, check=False)
        if proc.returncode != 0:
            return None, proc.stderr.strip() or f"qa-rollout.py exited {proc.returncode}"
        return json.loads(proc.stdout), None

    data, err = read(path)
    if err:
        return {"error": err}

    # The FILE figure, not the turn figure, and every sub-agent's file too.
    #
    # The Dash0 column sums every span of the session: one chat span per turn
    # plus one invoke_agent per completed sub-agent task. So the only figure it
    # can be compared against is the whole session's. Using the last turn's
    # counts made this column short by every earlier turn on a resumed session,
    # and short by the whole sub-agent on a delegating one — reported as a
    # difference when nothing was wrong.
    #
    # A sub-agent's usage lives only in its own rollout, which the driver keeps
    # as rollout-subagent-<thread id>.jsonl. Per-turn scoping is a separate
    # question and belongs to the spec that asserts it, which partitions by the
    # recorder's Stop timestamps rather than trusting either figure here.
    totals = dict(data["file"])
    subagents = 0
    for sub in sorted(glob.glob(os.path.join(run_dir, "rollout-subagent-*.jsonl"))):
        sub_data, sub_err = read(sub)
        if sub_err:
            return {"error": f"{sub}: {sub_err}"}
        subagents += 1
        for key in totals:
            totals[key] += sub_data["file"][key]

    row = {"input": totals["input"],
           "output": totals["output"],
           "cache_read": totals["cache_read"],
           # Codex does report a cache-write count, as cache_write_input_tokens.
           # This column used to hardcode zero on the belief that it did not,
           # which is precisely why nobody noticed the plugin was not parsing the
           # field: both sides agreed at zero for the wrong reason.
           "cache_write": totals["cache_write"],
           "reasoning": totals["reasoning"]}
    models = data["models"] or ["<no model>"]
    return {
        "total": {models[0]: row},
        "no_span_counts": True,
        "token_count_events": data["token_count_events"],
        "turn_boundaries": data["turn_boundaries"],
        "file_total": data["file"],
        "subagent_rollouts": subagents,
        "tool_calls": data["tool_calls"],
    }


def otel_summary(root, run_dir, session_id):
    """The Copilot runtime's second channel: Copilot's own native-OTel file.

    qa-otel.py is run as a subprocess rather than imported, for the same reason
    qa-rollout.py is: it is a separate reader of a separate format, and keeping
    the process boundary keeps that visible.

    Unlike the other two runtimes' second channels, this one carries a span-count
    expectation — and it has to, because Copilot's hooks carry none. Every
    execute_tool span the plugin emits is built from an execute_tool span in this
    file, and the turn count comes from its top-level invoke_agent spans.

    Read the agreement for what it is worth. This file is the plugin's input, so
    matching it proves the plugin copied faithfully, not that Copilot measured
    correctly. The hook record is the only fully independent input a Copilot run
    has, and it can speak only for the session lifecycle.
    """
    script = os.path.join(root, "qa", "tools", "qa-otel.py")
    proc = subprocess.run([sys.executable, script, run_dir, "--json"],
                          capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        # Absent is not zero. A run driven with native OTel off has no file by
        # design, and the plugin's documented degradation is a chat span with no
        # usage — so the channel reports nothing and the hooks column is left to
        # carry the run. Filling the column with zeros would turn that expected
        # degradation into two findings.
        return {
            "unavailable": proc.stderr.strip() or f"qa-otel.py exited {proc.returncode}",
            "no_span_counts": True,
            "no_usage": True,
            "total": {},
        }
    data = json.loads(proc.stdout)

    turns = len(data["turns"])
    row = {name: data["chat"][name] for name in
           ("input", "output", "cache_read", "reasoning")}
    # Copilot reports no cache-write count anywhere. Zero would read as the
    # plugin inventing tokens if it ever started emitting one, so the key is
    # simply absent and the report prints `-`.
    models = data["models"] or ["<no model>"]
    return {
        "total": {models[0]: row},
        "expected_spans": {
            "chat": turns,
            "execute_tool": sum(data["tools"].values()),
            # One per sub-agent, from the file's nested invoke_agent spans. The
            # file's ROOT invoke_agent is the turn itself, which the pipeline's
            # chat span already represents, so it is not counted here.
            "invoke_agent": data["subagents"],
            "total": turns + sum(data["tools"].values()) + data["subagents"],
        },
        "tools": data["tools"],
        "turns": turns,
        "chat_spans": data["chat_spans"],
        "subagent_tools": data["subagent_tools"],
        "subagents": data["subagents"],
        "failed_tools": data["failed_tools"],
        "agent_total": data["agent"],
        "cost": data["cost"],
        "conversations": data["conversations"],
        "session_scoped": session_id in data["conversations"],
    }


def copilot_summary(run_dir, manifest):
    """The Copilot runtime's own figures, from `copilot --output-format json`.

    Copilot's event stream reports no input tokens at all: only per-message
    output tokens, an AI-credit figure, and the session result. The missing
    counts are reported as absent rather than as zero, exactly as the Codex
    harness column is, because a zero there would read as a real disagreement
    with Dash0.
    """
    out = {
        "num_turns": manifest.get("turns"),
        "is_error": manifest.get("copilot_exit_code") not in (0, None),
        "cost_usd": None,
        "models": [],
        "spans_logged": manifest.get("spans_logged"),
        "session_id": None,
        "premium_requests": None,
        "nano_aiu": None,
    }
    for key in ("input", "cache_read", "cache_write", "reasoning"):
        out[key] = None

    path = os.path.join(run_dir, "copilot-events.jsonl")
    if not os.path.exists(path):
        out["error"] = "no copilot-events.jsonl in the run"
        out["output"] = None
        return out
    output_tokens, errors, models = 0, 0, []
    for line in open(path):
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        kind, data = event.get("type"), event.get("data") or {}
        if kind == "assistant.message":
            output_tokens += data.get("outputTokens") or 0
            if data.get("model") and data["model"] not in models:
                models.append(data["model"])
        elif kind == "session.usage_checkpoint":
            out["nano_aiu"] = data.get("totalNanoAiu")
            out["premium_requests"] = data.get("totalPremiumRequests")
        elif kind == "result":
            out["session_id"] = event.get("sessionId")
            usage = event.get("usage") or {}
            if usage.get("premiumRequests") is not None:
                out["premium_requests"] = usage["premiumRequests"]
        elif kind in ("session.error", "error"):
            errors += 1
    out["output"] = output_tokens
    out["models"] = sorted(models)
    out["stream_errors"] = errors
    return out


def codex_summary(run_dir, manifest):
    """The Codex runtime's own figures. Codex reports no cost, so there is none.

    `codex exec --json` is an event stream whose shape is Codex's to change, so
    usage is looked for and reported as absent when not found, never as zero.
    """
    out = {
        "num_turns": None,
        "is_error": manifest.get("codex_exit_code") not in (0, None),
        "cost_usd": None,
        "models": [],
        "spans_logged": manifest.get("spans_logged"),
    }
    for key in ("input", "output", "cache_read", "cache_write"):
        out[key] = None

    path = os.path.join(run_dir, "codex-events.jsonl")
    if not os.path.exists(path):
        out["error"] = "no codex-events.jsonl in the run"
        return out
    errors = 0
    for line in open(path):
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("type") == "error":
            errors += 1
        usage = event.get("usage")
        if isinstance(usage, dict):
            out["input"] = usage.get("input_tokens")
            out["output"] = usage.get("output_tokens")
            out["cache_read"] = usage.get("cached_input_tokens")
    out["stream_errors"] = errors
    return out


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
    hooks, transcript, harness = data["hooks"], data["transcript"], data["harness"]
    runtime = data["runtime"]
    findings = []

    print(f"run       : {data['run_dir']}  ({runtime})")
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
    if hooks.get("subagent_sessions"):
        print(f"            {hooks['subagent_sessions']} invocation(s) belong to a"
              " sub-agent's own call_ session.\n            The plugin drops those"
              " wholesale; the sub-agent's work arrives through the OTel file.")
    if hooks["other_sessions"]:
        print(f"            {hooks['other_sessions']} invocation(s) from an earlier"
              " session in this directory, ignored.\n            The run id was"
              " reused; use a fresh one so the record holds one session.")
    if hooks["unattributed"]:
        print(f"            {hooks['unattributed']} invocation(s) carry no session id,"
              " so they cannot be attributed.\n            A payload that did not"
              " parse is a recording failure, not another session.")

    # The second channel is a transcript, a rollout or an OTel file depending on
    # the runtime. The column keeps one width and changes its name, so a report
    # never claims a Copilot run had a transcript.
    tx_label = {"claude": "transcript", "codex": "rollout", "copilot": "otel"}[runtime]
    print("\nSpan counts")
    print(f"  {'type':<14}{'dash0':>8}{'hooks':>8}{tx_label:>12}")
    tx_expected = transcript.get("expected_spans", {})
    # The Codex rollout carries no span-count expectation. Printing 0 there would
    # read as "the transcript expected none" and flag every row as a difference,
    # so the column is left empty and only the hooks are compared.
    tx_counts = not transcript.get("no_span_counts")
    for kind in ("chat", "execute_tool", "invoke_agent", "total"):
        got = dash0["total"] if kind == "total" else dash0["spans"].get(kind, 0)
        want_hooks = hooks["expected_spans"].get(kind, 0)
        want_tx = tx_expected.get(kind, 0) if tx_counts else None
        flag = ""
        # A None expectation is not zero. Copilot's hooks say nothing about a
        # tool span, so comparing against 0 there would report every tool the
        # plugin correctly emitted as a surplus.
        if want_hooks is not None and got != want_hooks:
            flag = "  <-- differs from the hooks it was fed"
            findings.append(f"{kind}: Dash0 has {got}, the hooks imply {want_hooks}")
        elif want_tx is not None and got != want_tx:
            flag = f"  <-- differs from the {tx_label}"
            findings.append(f"{kind}: Dash0 has {got}, the {tx_label} implies {want_tx}")
        shown = "-" if want_tx is None else want_tx
        hooks_shown = "-" if want_hooks is None else want_hooks
        print(f"  {kind:<14}{got:>8}{hooks_shown:>8}{shown:>12}{flag}")

    # Parenting, which no other comparison here can see. Skipped on a truncated
    # result: the missing spans would make their children look orphaned.
    if dash0["total"] >= data["limit"]:
        print("\nParenting: not checked, the span set is truncated.")
    elif dash0["orphans"]:
        print(f"\nParenting: {len(dash0['orphans'])} span(s) point at a parent no span carries")
        for orphan in dash0["orphans"]:
            agent = f" (agent {orphan['agent']})" if orphan["agent"] else ""
            print(f"  {orphan['name']}{agent}  <-- parent {orphan['parent']} was never emitted")
            findings.append(f"{orphan['name']}: parent {orphan['parent']} is not a span of this session")
        print("  These hang outside the trace. Every count above can still reconcile,")
        print("  which is why this is checked separately: the spans exist, the tree does not.")
    else:
        print("\nParenting: every span's parent is a span of this session.")

    # Which record the tool table is compared against. On Claude and Codex the
    # hooks are the pipeline's input and therefore the expectation. On Copilot
    # the plugin ignores the tool hooks entirely and builds every tool span from
    # the native-OTel file, so that file is the expectation and the recorded
    # postToolUse events are printed underneath as a second opinion.
    tools_from_hooks = hooks["tools_are_an_expectation"]
    expected_tools = hooks["tools"] if tools_from_hooks else (transcript.get("tools") or {})
    source = "PostToolUse fired" if tools_from_hooks else "the OTel file holds"
    print(f"\nTool spans  (expectation: {'hooks' if tools_from_hooks else 'the OTel file'})")
    names = sorted(set(dash0["tools"]) | set(expected_tools))
    if not names:
        print("  (none)")
    for name in names:
        got, want = dash0["tools"].get(name, 0), expected_tools.get(name, 0)
        flag = "" if got == want else "  <-- differs"
        if got != want:
            findings.append(f"tool {name}: Dash0 has {got}, {source} {want}")
        print(f"  {name:<20}{got:>6}{want:>6}{flag}")
    if not tools_from_hooks and hooks["tools"]:
        print(f"  postToolUse also fired for: {hooks['tools']}. The plugin does not read"
              "\n  those events — they carry no duration and never fire inside a"
              " sub-agent —\n  so this is a second opinion, not an expectation. Fewer"
              " here than in Dash0 is\n  normal on a delegating session.")

    print("\nTokens")
    keys = ["input", "output", "cache_read", "cache_write"]
    # reasoning is compared only where the second channel reports it. The Codex
    # rollout does; claude-code-usage-audit.py does not, so asking for it there
    # would read as Dash0 inventing tokens rather than the channel being silent.
    if any("reasoning" in row for row in (transcript.get("total") or {}).values()):
        keys.append("reasoning")
    d0 = totals(dash0["usage"], keys)
    tx = totals(transcript.get("total", {}), keys)
    # Copilot reports no cache-write count in either channel, so comparing it
    # would print a row of zeros that says nothing.
    reported = [k for k in keys
                if not (runtime == "copilot" and k == "cache_write")]
    label = "claude" if runtime == "claude" else runtime
    print(f"  {'metric':<14}{'dash0':>10}{tx_label:>12}{label:>10}")
    # The same rule as the span counts: a channel that could not be read reports
    # nothing, never zero.
    tx_usage = not transcript.get("no_usage")
    for key in reported:
        own = harness.get(key)
        want = tx.get(key, 0) if tx_usage else None
        flag = "" if want is None or d0[key] == want else "  <-- differs"
        if want is not None and d0[key] != want:
            findings.append(f"{key} tokens: Dash0 {d0[key]}, {tx_label} {want}")
        print(f"  {key:<14}{d0[key]:>10}{'-' if want is None else want:>12}"
              f"{own if own is not None else '-':>10}{flag}")
    if not tx_usage:
        print(f"  The {tx_label} channel is unavailable, so those cells are `-` rather"
              " than 0 and\n  nothing is compared against them:"
              f" {transcript.get('unavailable')}")

    if runtime == "codex":
        subs = transcript.get("subagent_rollouts") or 0
        print(f"\nRollout: {transcript.get('token_count_events')} token_count event(s),"
              f" {transcript.get('turn_boundaries')} turn boundary/ies"
              + (f", plus {subs} sub-agent rollout(s) summed in." if subs else "."))
        if transcript.get("turn_boundaries") == 0 and transcript.get("token_count_events"):
            print("  The file has no user_message to scope a turn on, so the transcript"
                  " column is\n  the whole session. On a single-turn probe that is the same"
                  " number; on anything\n  longer, read qa/tools/qa-rollout.py's note before"
                  " calling a difference a finding.")
        if manifest.get("spans_logged") is not None:
            print(f"  The plugin's debug log holds {manifest['spans_logged']} span(s)."
                  " A span there but not in\n  Dash0 was sent and lost; a span in neither was"
                  " never built.")

    if runtime == "copilot" and transcript.get("no_usage"):
        print("\nNative OTel: no file for this run. The plugin's documented degradation"
              " is a chat span\n  per turn with no usage and no tool spans, which is"
              " what the hooks column above\n  checks. Nothing here can say whether"
              " usage should have been present.")
    elif runtime == "copilot":
        print(f"\nNative OTel: {transcript.get('turns')} agent turn(s) over"
              f" {transcript.get('chat_spans')} model round-trip(s),"
              f" cost {transcript.get('cost')} AI credits.")
        agent = transcript.get("agent_total") or {}
        if agent and any(agent.get(k) != tx.get(k, 0) for k in ("input", "output")):
            print("  Copilot's own per-turn roll-up disagrees with the sum of its chat"
                  " spans\n  (agent"
                  f" {agent.get('input')}/{agent.get('output')} vs chat"
                  f" {tx.get('input')}/{tx.get('output')}). That is Copilot's"
                  " arithmetic, not the plugin's;\n  run qa/tools/qa-otel.py for the"
                  " detail before filing anything.")
        if transcript.get("subagents"):
            print(f"  {transcript['subagents']} sub-agent(s), running"
                  f" {transcript.get('subagent_tools')} tool call(s) between them. Both"
                  " reach Dash0\n  through the OTel file only — no hook fires for either"
                  " — as an invoke_agent span\n  under the tool that spawned it, with"
                  " that agent's tools beneath it.")
        if not transcript.get("session_scoped") and transcript.get("conversations"):
            print("  WARNING: the OTel file holds no span for this session id"
                  f" ({', '.join(transcript['conversations'])} instead).\n  Every figure"
                  " in the otel column is therefore about another conversation.")
    if runtime == "copilot":
        if manifest.get("spans_logged") is not None:
            print(f"  The plugin's debug log holds {manifest['spans_logged']} span(s)."
                  " A span there but not in\n  Dash0 was sent and lost; a span in neither"
                  " was never built.")
        # Copilot is the one runtime whose session id is pinned in advance, so
        # the id the run queried and the id Copilot used are two records of the
        # same thing and can be checked against each other.
        harness_session = harness.get("session_id")
        if harness_session and harness_session != manifest.get("session_id"):
            print(f"  Copilot reported session {harness_session}, but the run is scoped"
                  f" to {manifest.get('session_id')}.")
            findings.append(f"session id: Copilot reported {harness_session},"
                            f" the run queried {manifest.get('session_id')}")

    print("\nModels")
    print(f"  dash0     : {', '.join(sorted(dash0['usage'])) or '(none)'}")
    print(f"  {tx_label:<10}: {', '.join(sorted(transcript.get('total', {}))) or '(none)'}")
    print(f"  {label:<10}: {', '.join(harness.get('models') or []) or '(none)'}")

    if harness.get("cost_usd") is not None:
        print(f"\nClaude Code's own figures: ${harness['cost_usd']:.4f} over "
              f"{harness.get('num_turns')} turn(s), is_error={harness.get('is_error')}")

    if hooks.get("errors"):
        print(f"\nWARNING: the recorder could not read a transcript on "
              f"{len(hooks['errors'])} invocation(s); those events have no snapshot.")

    print()
    if findings:
        print(f"{len(findings)} difference(s):")
        for finding in findings:
            print(f"  - {finding}")
        if runtime == "claude":
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

    runtime = manifest.get("runtime", DEFAULT_RUNTIME)
    if runtime == "codex":
        transcript = rollout_summary(root, run_dir)
        harness = codex_summary(run_dir, manifest)
    elif runtime == "copilot":
        transcript = otel_summary(root, run_dir, manifest["session_id"])
        harness = copilot_summary(run_dir, manifest)
    elif runtime == "claude":
        transcript = transcript_summary(root, manifest["session_id"])
        harness = claude_summary(run_dir)
    else:
        print(f"manifest.json names runtime {runtime!r}, which this tool has no"
              " channels for. Add them here rather than comparing two of four"
              " columns.", file=sys.stderr)
        return 2

    data = {
        "run_dir": run_dir,
        "runtime": runtime,
        "dataset": dataset,
        "api_url": config["apiUrl"],
        "limit": args.limit,
        "since": since,
        "until": until,
        "manifest": manifest,
        "dash0": dash0_summary(spans or []),
        "dash0_error": error,
        "hooks": hooks_summary(run_dir, manifest["session_id"], runtime),
        "transcript": transcript,
        "harness": harness,
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
