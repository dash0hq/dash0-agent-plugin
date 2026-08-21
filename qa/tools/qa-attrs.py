#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
"""Check that a run's spans carry no attribute outside the documented contract.

Every other tool here compares counts. This one compares the attribute *surface*,
which is the thing no count can see: an unexpected key changes no span total, so
qa-compare.py exits 0 whether or not it is there.

The expectation is the four attribute tables in DEVELOPMENT.md. That document is
maintained by hand and the pipeline never reads it, so it is an independent
record in the same sense the hook recording is. A key on a span that the document
does not list is either an export nobody declared or a documentation gap, and
both are worth knowing.

Three classes of surplus, reported apart because the fix differs:

  raw payload field   No dotted namespace at all. eventAttributes copies every
                      hook payload field it does not recognize, so this is what a
                      new upstream field looks like on arrival. prompt_id,
                      session_crons, and background_tasks shipped this way.
  undocumented export The plugin writes the key itself, and DEVELOPMENT.md does
                      not list it. Either the contract is stale or the export was
                      not meant to ship.
  added at ingest     No Go source in internal/ writes the key, so Dash0 derived
                      it after the plugin was done. dash0.gen_ai.usage.cost is
                      the documented example. Informational, never a finding:
                      the plugin cannot emit or suppress these.

Usage:
  qa/tools/qa-attrs.py qa/runs/<run-id>
  qa/tools/qa-attrs.py qa/runs/<run-id> --json
"""

import argparse
import importlib.util
import json
import os
import re
import subprocess
import sys
import traceback

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, "..", ".."))


def load_compare():
    """Import qa-compare.py for its config loader and its span query.

    Imported rather than reimplemented so the auth token is handled in exactly
    one place. qa-compare.py strips it from any command it prints; a second copy
    of that logic is a second chance to print a credential.
    """
    path = os.path.join(HERE, "qa-compare.py")
    spec = importlib.util.spec_from_file_location("qa_compare", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


# Attribute keys live in the leftmost column of a markdown table, wrapped in
# backticks. A cell may hold a brace expansion, which DEVELOPMENT.md uses for the
# Codex rate-limit slots.
TABLE_KEY = re.compile(r"^\|\s*`([^`]+)`\s*\|")
BRACES = re.compile(r"\{([^}]*)\}")

# The sections whose tables describe span and resource attributes. "Span shape"
# is deliberately excluded: its rows are span name, kind, and status, not keys.
ATTRIBUTE_SECTIONS = (
    "Resource attributes",
    "On every span",
    "LLM / chat spans",
    "Tool-call spans",
)


def expand(key):
    """`dash0.gen_ai.rate_limit.{primary,secondary}.used_percent` -> both keys."""
    match = BRACES.search(key)
    if not match:
        return [key]
    return [key[:match.start()] + option + key[match.end():]
            for option in match.group(1).split(",")]


def documented_keys(root):
    """Every attribute key DEVELOPMENT.md lists, from its four tables."""
    path = os.path.join(root, "DEVELOPMENT.md")
    if not os.path.exists(path):
        return None, f"{path} does not exist; it is the expectation for this check."
    keys, section, seen_sections = set(), None, set()
    for line in open(path):
        if line.startswith("#"):
            heading = line.lstrip("#").strip()
            section = next((s for s in ATTRIBUTE_SECTIONS if heading.startswith(s)), None)
            if section:
                seen_sections.add(section)
            continue
        if not section:
            continue
        match = TABLE_KEY.match(line)
        if match:
            keys.update(expand(match.group(1)))
    missing = [s for s in ATTRIBUTE_SECTIONS if s not in seen_sections]
    if missing:
        return None, ("DEVELOPMENT.md has no section for: " + ", ".join(missing) +
                      ". The headings moved, so this check is reading a partial"
                      " contract and would report healthy keys as surplus.")
    return keys, None


def plugin_writes(key):
    """Whether any non-test Go file writes this key as a literal.

    The separator between "we exported something undeclared" and "Dash0 derived
    it at ingest". Tests are excluded because a test asserting a key must *not*
    be emitted would otherwise class it as an export.

    cmd/ is searched as well as internal/. The entrypoints write attributes of
    their own -- cmd/copilot-on-event/main.go sets dash0.gen_ai.tool.task.name --
    and a search that misses them files a real undeclared export under "added at
    ingest", which is the one class this tool never reports.

    Still a floor, not a proof: a key assembled from parts (prefix+suffix, or
    fmt.Sprintf) matches no literal and is classed as ingest-added. So a key in
    that class is weak evidence, which is why it is informational only.
    """
    proc = subprocess.run(
        ["grep", "-rlF", f'"{key}"', "--include=*.go",
         os.path.join(ROOT, "internal"), os.path.join(ROOT, "cmd")],
        capture_output=True, text=True, check=False)
    hits = [p for p in proc.stdout.splitlines() if not p.endswith("_test.go")]
    return bool(hits)


def classify(observed, documented):
    """Split the surplus into the three classes, in order of severity."""
    surplus = sorted(observed - documented)
    raw, undocumented, ingest = [], [], []
    for key in surplus:
        if "." not in key:
            raw.append(key)
        elif plugin_writes(key):
            undocumented.append(key)
        else:
            ingest.append(key)
    return {"raw_payload_fields": raw,
            "undocumented_exports": undocumented,
            "added_at_ingest": ingest}


def observe(spans):
    """Every attribute key on any span, plus the resource keys, and who carries them."""
    where = {}
    for span in spans:
        kind = (span["name"].split() or ["<unnamed>"])[0]
        for key in list(span["attrs"]) + list(span["resource"]):
            where.setdefault(key, set()).add(kind)
    return where


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("run_dir")
    parser.add_argument("--dataset", help="override the dataset from the config")
    parser.add_argument("--json", action="store_true", dest="as_json")
    args = parser.parse_args()

    compare = load_compare()

    manifest_path = os.path.join(args.run_dir, "manifest.json")
    if not os.path.exists(manifest_path):
        print(f"no manifest.json in {args.run_dir}; is that a run directory?",
              file=sys.stderr)
        return 2
    manifest = json.load(open(manifest_path))

    config, config_error = compare.load_config(ROOT)
    if config_error:
        print(config_error, file=sys.stderr)
        return 2

    documented, doc_error = documented_keys(ROOT)
    if doc_error:
        print(doc_error, file=sys.stderr)
        return 2

    session_id = manifest.get("session_id")
    if not session_id:
        print(f"{manifest_path} has no session_id; the query cannot be scoped.",
              file=sys.stderr)
        return 2

    dataset = args.dataset or config["dataset"]
    limit = 100
    spans, query_error = compare.query_dash0(
        config, session_id, dataset,
        compare.widen(manifest.get("started_at") or "now-1h", -60),
        compare.widen(manifest.get("ended_at") or "now", 120), limit)
    if query_error:
        print(query_error, file=sys.stderr)
        return 2
    if len(spans or []) >= limit:
        # The documented cap. A truncated span set is a truncated attribute set,
        # so "every attribute is in the contract" would be a claim about a
        # prefix of the session. Exit 2 rather than 0: unknown, not clean.
        print(f"the query returned {len(spans)} spans, its limit of {limit}. The"
              " attribute surface is truncated,\nso a pass here would only cover"
              " part of the session. Query it in time slices.", file=sys.stderr)
        return 2
    if not spans:
        print("0 spans for this session. That is usually ingest lag; re-run"
              " before concluding anything.", file=sys.stderr)
        return 2

    where = observe(spans)
    result = classify(set(where), documented)
    result.update(session=session_id, spans=len(spans),
                  documented=len(documented), observed=len(where),
                  carried_by={k: sorted(v) for k, v in where.items()})

    if args.as_json:
        print(json.dumps(result, indent=2))
    else:
        print(f"session   : {result['session']}")
        print(f"spans     : {result['spans']}")
        print(f"attributes: {result['observed']} distinct keys observed,"
              f" {result['documented']} documented in DEVELOPMENT.md")

        for label, key in (("Raw payload fields", "raw_payload_fields"),
                           ("Undocumented exports", "undocumented_exports")):
            if result[key]:
                print(f"\n{label}")
                for name in result[key]:
                    print(f"  {name}  on {', '.join(where[name])}")
        if result["added_at_ingest"]:
            print("\nAdded at ingest, not sent by the plugin (informational)")
            for name in result["added_at_ingest"]:
                print(f"  {name}  on {', '.join(where[name])}")

        findings = result["raw_payload_fields"] + result["undocumented_exports"]
        if findings:
            print(f"\n{len(findings)} attribute(s) outside the contract.")
            print("A raw payload field means eventAttributes copied a hook field"
                  " nobody denied;\nadd it to attrSkipKeys in internal/otlp/otlp.go."
                  " An undocumented export means\neither DEVELOPMENT.md is stale or"
                  " the export was not meant to ship.")
        else:
            print("\nEvery attribute is in the documented contract.")

    return 1 if result["raw_payload_fields"] or result["undocumented_exports"] else 0


if __name__ == "__main__":
    # Exit 1 is the documented "surplus attributes found", and Python gives an
    # uncaught exception that same code. Without this, a crash reads as a
    # finding: the spec's oracle would report undeclared attributes that were
    # never observed. 2 is "this check could not run", which is what a crash is.
    try:
        sys.exit(main())
    except Exception:  # noqa: BLE001 -- the exit code matters more than the type
        traceback.print_exc()
        print("\nqa-attrs.py failed before it could judge anything. This is exit 2"
              " (check did not run),\nnot exit 1 (attributes outside the"
              " contract).", file=sys.stderr)
        sys.exit(2)
