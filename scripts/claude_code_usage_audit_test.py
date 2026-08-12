#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
"""Tests for claude-code-usage-audit.py.

Run with:  python3 -m unittest discover -s scripts -p '*_test.py'
"""

import importlib.util
import json
import os
import tempfile
import unittest

# The script is named for the command line, so it is loaded by path rather than
# imported as a module.
_SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                       "claude-code-usage-audit.py")
_spec = importlib.util.spec_from_file_location("cc_usage_audit", _SCRIPT)
audit = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(audit)


def write_transcript(directory, name, entries):
    path = os.path.join(directory, name)
    with open(path, "w", encoding="utf-8") as handle:
        for entry in entries:
            handle.write(json.dumps(entry) + "\n")
    return path


def assistant(message_id, usage, model="claude-opus-5", **extra):
    entry = {"type": "assistant", "message": dict(id=message_id, role="assistant",
                                                  model=model, usage=usage)}
    entry.update(extra)
    return entry


def usage(inp=0, out=0, write=0, read=0, split=None, iterations=None):
    raw = {"input_tokens": inp, "output_tokens": out,
           "cache_creation_input_tokens": write, "cache_read_input_tokens": read}
    if split is not None:
        raw["cache_creation"] = split
    if iterations is not None:
        raw["iterations"] = iterations
    return raw


class ReadTranscriptTest(unittest.TestCase):
    """One API call is written as several entries, each repeating its usage."""

    def setUp(self):
        self.dir = tempfile.mkdtemp()

    def test_counts_a_streamed_call_once(self):
        # Mirrors a captured Bedrock transcript: no requestId, and the first two
        # entries are one call restating cache_creation 15643.
        path = write_transcript(self.dir, "t.jsonl", [
            assistant("msg_a", usage(inp=2, out=1, write=15643)),
            assistant("msg_a", usage(inp=2, out=133, write=15643)),
            assistant("msg_b", usage(inp=2, out=139, write=5762, read=15643)),
        ])

        totals = audit.read_transcript(path)

        self.assertEqual(["claude-opus-5"], list(totals))
        got = totals["claude-opus-5"]
        self.assertEqual(2, got.calls, "two API calls, not three entries")
        self.assertEqual(4, got.input)
        self.assertEqual(272, got.output, "the last entry of a call wins")
        self.assertEqual(21405, got.cache_write)
        self.assertEqual(15643, got.cache_read)

    def test_sums_distinct_calls(self):
        path = write_transcript(self.dir, "t.jsonl", [
            assistant("msg_1", usage(inp=10, out=5, write=100, read=1000)),
            assistant("msg_2", usage(inp=20, out=7, write=200, read=2000)),
        ])

        got = audit.read_transcript(path)["claude-opus-5"]

        self.assertEqual(2, got.calls)
        self.assertEqual(30, got.input)
        self.assertEqual(300, got.cache_write)

    def test_splits_cache_writes_by_ttl(self):
        path = write_transcript(self.dir, "t.jsonl", [
            assistant("msg_a", usage(write=1000, split={
                "ephemeral_5m_input_tokens": 400,
                "ephemeral_1h_input_tokens": 600})),
        ])

        got = audit.read_transcript(path)["claude-opus-5"]

        self.assertEqual(400, got.cache_write_5m)
        self.assertEqual(600, got.cache_write_1h)
        self.assertEqual(1000, got.cache_write)

    def test_attributes_writes_to_1h_when_ttl_is_absent(self):
        path = write_transcript(self.dir, "t.jsonl", [
            assistant("msg_a", usage(write=750)),
        ])

        got = audit.read_transcript(path)["claude-opus-5"]

        self.assertEqual(0, got.cache_write_5m)
        self.assertEqual(750, got.cache_write_1h)

    def test_sums_billed_iterations_of_a_retried_call(self):
        # A fallback retry bills every iteration; the top-level fields mirror
        # only the final one.
        path = write_transcript(self.dir, "t.jsonl", [
            assistant("msg_a", usage(inp=5607, out=698, iterations=[
                usage(inp=5607, out=2, write=50, read=1000),
                usage(inp=5607, out=698, write=100, read=2000),
            ])),
        ])

        got = audit.read_transcript(path)["claude-opus-5"]

        self.assertEqual(11214, got.input)
        self.assertEqual(700, got.output)

    def test_ignores_locally_generated_messages(self):
        path = write_transcript(self.dir, "t.jsonl", [
            assistant("msg_a", usage(inp=5), model=audit.SYNTHETIC_MODEL),
            assistant("msg_b", usage(inp=7)),
        ])

        totals = audit.read_transcript(path)

        self.assertEqual(["claude-opus-5"], list(totals))
        self.assertEqual(7, totals["claude-opus-5"].input)

    def test_skips_malformed_lines(self):
        path = os.path.join(self.dir, "t.jsonl")
        with open(path, "w", encoding="utf-8") as handle:
            handle.write("not json at all {{{\n")
            handle.write(json.dumps(assistant("msg_a", usage(inp=3))) + "\n")

        self.assertEqual(3, audit.read_transcript(path)["claude-opus-5"].input)

    def test_separates_per_model(self):
        path = write_transcript(self.dir, "t.jsonl", [
            assistant("msg_a", usage(inp=10), model="claude-opus-5"),
            assistant("msg_b", usage(inp=20), model="claude-haiku-4-5"),
        ])

        totals = audit.read_transcript(path)

        self.assertEqual(10, totals["claude-opus-5"].input)
        self.assertEqual(20, totals["claude-haiku-4-5"].input)


class CountSpansTest(unittest.TestCase):
    """The counts must match the spans the plugin emits for the same session."""

    def setUp(self):
        self.counts = {"chat": 0, "execute_tool": 0, "invoke_agent": 0}

    def count(self, entry, is_main=True):
        audit.count_spans(entry, self.counts, is_main)

    def test_a_real_prompt_starts_a_turn(self):
        self.count({"type": "user", "message": {"content": "hello"}})
        self.count({"type": "user",
                    "message": {"content": [{"type": "text", "text": "hi"}]}})

        self.assertEqual(2, self.counts["chat"])

    def test_tool_results_and_meta_do_not_start_a_turn(self):
        self.count({"type": "user",
                    "message": {"content": [{"type": "tool_result"}]}})
        self.count({"type": "user", "isMeta": True, "message": {"content": "x"}})

        self.assertEqual(0, self.counts["chat"])

    def test_a_subagent_prompt_is_not_a_turn(self):
        # A sub-agent's kickoff prompt looks like a user message in its own
        # transcript, but it produces an invoke_agent span, not a chat span.
        self.count({"type": "user", "message": {"content": "go"}}, is_main=False)

        self.assertEqual(0, self.counts["chat"])

    def test_a_subagent_tool_call_yields_both_spans(self):
        for name in ("Agent", "Task", "task"):
            with self.subTest(name=name):
                self.counts = {"chat": 0, "execute_tool": 0, "invoke_agent": 0}
                self.count({"type": "assistant", "message": {"content": [
                    {"type": "tool_use", "name": name}]}})

                self.assertEqual(1, self.counts["invoke_agent"])
                self.assertEqual(1, self.counts["execute_tool"],
                                 "the Task call is itself a tool span")

    def test_an_ordinary_tool_call_yields_one_span(self):
        self.count({"type": "assistant", "message": {"content": [
            {"type": "tool_use", "name": "Bash"},
            {"type": "text", "text": "ok"}]}})

        self.assertEqual(1, self.counts["execute_tool"])
        self.assertEqual(0, self.counts["invoke_agent"])


if __name__ == "__main__":
    unittest.main()
