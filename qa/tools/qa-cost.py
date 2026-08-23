#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
"""Check the cost Dash0 computed for a session against a price table.

No span carries a cost. Dash0 derives `dash0.gen_ai.usage.cost` at ingest from
the token attributes the plugin sent, so a cost check is really a check of two
things at once: that the plugin reported the right tokens, and that they were
priced correctly.

The expected value comes from the token counts in Claude Code's own transcript
multiplied by the published list prices in PRICES below. Neither input involves
the plugin or Dash0, which is what makes the comparison worth making.
`claude-result.json` gives a third, independent figure for the same session:
Claude Code prices the same call itself, in-process.

A cache write is billed by its lifetime, and Dash0 does not currently price the
two lifetimes apart. So this reports a bracket rather than a single number:
`low` prices every cache write as 5-minute, `high` as 1-hour, and `exact` uses
the split the transcript actually recorded. A cost inside the bracket is not a
finding. See qa/learnings/cost-cache-write-duration-is-not-priced-separately.md.

Usage:
  qa/tools/qa-cost.py qa/runs/<run-id>
  qa/tools/qa-cost.py qa/runs/<run-id> --json
"""

import argparse
import importlib.util
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))

# Dollars per million tokens, from Anthropic's published list prices. A cache
# write costs 1.25x the input rate for a 5-minute lifetime and 2x for an hour.
#
# verified: every rate marked so has been reproduced exactly against
# dash0.gen_ai.usage.cost and against claude-result.json. An unverified row is a
# guess from the published table and will produce a wrong expectation without
# saying so, which is why it is labelled here rather than trusted silently.
PRICES = {
    # model            input  output  read   verified
    "claude-haiku-4-5":  (1.0,   5.0,  0.10, True),
    "claude-opus-5":     (5.0,  25.0,  0.50, True),
    "claude-sonnet-5":   (3.0,  15.0,  0.30, False),
    "claude-opus-4-8":  (15.0,  75.0,  1.50, False),
}
WRITE_5M = 1.25
WRITE_1H = 2.00


def canonical(model):
    """claude-haiku-4-5-20251001 -> claude-haiku-4-5.

    The transcript names a dated snapshot, Dash0 stores the canonical name. The
    price is the same for both, so the date is dropped rather than looked up.
    """
    parts = model.rsplit("-", 1)
    if len(parts) == 2 and len(parts[1]) == 8 and parts[1].isdigit():
        return parts[0]
    return model


def price(usage):
    """Expected dollars for one model's usage, as (low, exact, high).

    low and high bracket the cache-write lifetime ambiguity. They are equal to
    exact when the session wrote nothing to the cache, which is the only case
    where a cost can be asserted to the cent.
    """
    rates = PRICES.get(usage["model"])
    if rates is None:
        return None
    inp, out, read, verified = rates
    base = (usage["input"] * inp + usage["output"] * out + usage["cache_read"] * read)
    writes_5m, writes_1h = usage["cache_write_5m"], usage["cache_write_1h"]
    total_writes = writes_5m + writes_1h
    return {
        "model": usage["model"],
        "verified_rates": verified,
        "low": (base + total_writes * inp * WRITE_5M) / 1e6,
        "exact": (base + writes_5m * inp * WRITE_5M + writes_1h * inp * WRITE_1H) / 1e6,
        "high": (base + total_writes * inp * WRITE_1H) / 1e6,
        "cache_writes": total_writes,
    }


def transcript_usage(root, session_id):
    """Per-model usage from Claude Code's transcript, sub-agents included."""
    script = os.path.join(root, "claude", "tools", "claude-code-usage-audit.py")
    proc = subprocess.run([sys.executable, script, session_id, "--json"],
                          capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        return None, proc.stderr.strip() or f"audit exited {proc.returncode}"
    audit = json.loads(proc.stdout)
    rows = []
    for model, row in (audit.get("total") or {}).items():
        rows.append({
            "model": canonical(model),
            "input": row.get("input", 0),
            "output": row.get("output", 0),
            "cache_read": row.get("cache_read", 0),
            "cache_write_5m": row.get("cache_write_5m", 0),
            "cache_write_1h": row.get("cache_write_1h", 0),
        })
    return rows, None


def compare_module():
    """qa-compare.py's config loading and span query, reused rather than copied."""
    spec = importlib.util.spec_from_file_location(
        "qa_compare", os.path.join(HERE, "qa-compare.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("run_dir")
    parser.add_argument("--dataset", default=None)
    parser.add_argument("--json", action="store_true", dest="as_json")
    args = parser.parse_args()

    compare = compare_module()
    run_dir = os.path.abspath(args.run_dir)
    manifest_path = os.path.join(run_dir, "manifest.json")
    if not os.path.exists(manifest_path):
        print(f"no manifest.json in {run_dir}; is that a run directory?", file=sys.stderr)
        return 2
    manifest = json.load(open(manifest_path))
    session_id = manifest["session_id"]

    root = subprocess.run(["git", "rev-parse", "--show-toplevel"],
                          capture_output=True, text=True, check=True).stdout.strip()
    config, config_error = compare.load_config(root)
    if config_error:
        print(f"qa/config.local.json is unusable, so no cost can be read:\n"
              f"  {config_error}", file=sys.stderr)
        return 2

    usage, audit_error = transcript_usage(root, session_id)
    if audit_error:
        print(f"the transcript audit failed, so there is no expectation:\n"
              f"  {audit_error}", file=sys.stderr)
        return 2

    spans, span_error = compare.query_dash0(
        config, session_id, args.dataset or config["dataset"],
        compare.widen(manifest.get("started_at") or "now-1h", -60),
        compare.widen(manifest.get("ended_at") or "now", 120), 100)
    if span_error:
        print(f"ERROR: reading spans from Dash0 failed.\n{span_error}", file=sys.stderr)
        return 2

    dash0_cost = sum(span["attrs"].get("dash0.gen_ai.usage.cost") or 0
                     for span in spans)
    priced = [p for p in (price(u) for u in usage) if p]
    unpriced = [u["model"] for u in usage if u["model"] not in PRICES]
    expected = {
        key: sum(p[key] for p in priced) for key in ("low", "exact", "high")
    }
    claude_cost = None
    result_path = os.path.join(run_dir, "claude-result.json")
    if os.path.exists(result_path):
        claude_cost = json.load(open(result_path)).get("total_cost_usd")

    data = {
        "run_dir": run_dir,
        "session": session_id,
        "models": priced,
        "unpriced_models": unpriced,
        "expected": expected,
        "dash0_cost": dash0_cost,
        "claude_cost": claude_cost,
        "cache_writes": sum(p["cache_writes"] for p in priced),
        "unverified_rates": [p["model"] for p in priced if not p["verified_rates"]],
    }
    if args.as_json:
        print(json.dumps(data, indent=2))
        return 0
    return report(data)


def report(data):
    print(f"run     : {data['run_dir']}")
    print(f"session : {data['session']}")
    print(f"models  : {', '.join(p['model'] for p in data['models']) or '(none)'}")

    if data["unpriced_models"]:
        print(f"\nERROR: no price for {', '.join(data['unpriced_models'])}. Add it to"
              " PRICES in this file;\nan expectation computed without it would be"
              " silently too low.")
        return 2
    if data["unverified_rates"]:
        print(f"\nWARNING: the rates for {', '.join(data['unverified_rates'])} have"
              " never been reproduced\nagainst a real span. Treat a mismatch as a"
              " wrong price table before treating it as a bug.")

    exp, low, high = data["expected"]["exact"], data["expected"]["low"], data["expected"]["high"]
    print(f"\nExpected from the transcript x list prices: ${exp:.6f}")
    if data["cache_writes"]:
        print(f"  bracket, cache-write lifetime unpriced : ${low:.6f} .. ${high:.6f}"
              f"  ({data['cache_writes']} cache-write tokens)")
    print(f"Dash0 dash0.gen_ai.usage.cost              : ${data['dash0_cost']:.6f}")
    if data["claude_cost"] is not None:
        print(f"Claude Code total_cost_usd                 : ${data['claude_cost']:.6f}"
              "   (main session only, no sub-agents)")

    if data["cache_writes"] == 0:
        # No lifetime ambiguity, so the cent is assertable. A tenth of a cent of
        # slack absorbs float accumulation across spans, nothing else.
        ok = abs(data["dash0_cost"] - exp) < 1e-6
        verdict = "matches to the microdollar" if ok else "DIFFERS"
    else:
        ok = low - 1e-6 <= data["dash0_cost"] <= high + 1e-6
        verdict = "inside the bracket" if ok else "OUTSIDE the bracket"
    print(f"\nDash0's cost {verdict}.")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
