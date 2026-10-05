# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
"""Checks for the QA comparers' own verdicts: a comparer that passes a broken
run is worse than none."""

import importlib.util
import json
import os
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))


def load(name, file):
    spec = importlib.util.spec_from_file_location(name, os.path.join(HERE, file))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


qa = load("qa_compare", "qa-compare.py")
oc = load("qa_compare_opencode_v2", "qa-compare-opencode-v2.py")


def span(trace, span_id, parent=""):
    return {"name": span_id, "attrs": {}, "trace_id": trace, "span_id": span_id, "parent_span_id": parent}


class Orphans(unittest.TestCase):
    def test_parent_in_another_trace_is_a_hole(self):
        holes = qa.orphans([span("A", "p"), span("B", "c", parent="p")])
        self.assertEqual([h["name"] for h in holes], ["c"])

    def test_parent_in_the_same_trace_is_fine(self):
        self.assertEqual(qa.orphans([span("A", "p"), span("A", "c", parent="p")]), [])


class Redaction(unittest.TestCase):
    def messages(self, *parts, role="user", **extra):
        return json.dumps([{"role": role, "parts": list(parts), **extra}])

    def test_go_escaped_marker_is_redacted(self):
        # Go escapes the marker as \u003cREDACTED\u003e; decoded, it is the same.
        value = '[{"parts":[{"content":"\\u003cREDACTED\\u003e","type":"text"}],"role":"user"}]'
        self.assertTrue(oc.is_redacted(value))

    def test_marker_next_to_leaked_text_is_not(self):
        leaked = self.messages({"type": "text", "content": oc.REDACTED},
                               {"type": "text", "content": "the real reply"}, role="assistant")
        self.assertFalse(oc.is_redacted(leaked))

    def test_marker_inside_other_content_is_not(self):
        self.assertFalse(oc.is_redacted(self.messages({"type": "text", "content": "<REDACTED> and more"})))

    def test_malformed_extra_and_duplicate_keys_are_not(self):
        self.assertFalse(oc.is_redacted('[{"role":"user","parts":[{"type":"text","content":"<REDACTED>"}]'))
        self.assertFalse(oc.is_redacted(self.messages({"type": "text", "content": oc.REDACTED}, leak="x")))
        self.assertFalse(oc.is_redacted(
            '[{"role":"user","parts":[{"type":"text","content":"<REDACTED>","content":"leak"}]}]'))
        # Ordinary parsing keeps the last value, so this order hides the leak
        # unless duplicates are rejected outright.
        self.assertFalse(oc.is_redacted(
            '[{"role":"user","parts":[{"type":"text","content":"leak","content":"<REDACTED>"}]}]'))
        self.assertFalse(oc.is_redacted("[]"))
        self.assertFalse(oc.is_redacted(None))


def call(call_id, name, server=None):
    attrs = {oc.CALL_ID: call_id, qa.TOOL: name}
    if server:
        attrs[oc.MCP_SERVER] = server
    return {"attrs": attrs}


class Calls(unittest.TestCase):
    export = {"c1": "qa_under_score_echo_text", "c2": "qa_under_score_namespace_custom_echo_text", "c3": "read"}

    def test_names_as_the_export_has_them_reconcile(self):
        calls = [call("c1", "qa_under_score_echo_text"),
                 call("c2", "qa_under_score_namespace_custom_echo_text"), call("c3", "read")]
        self.assertEqual(oc.call_problems(calls, self.export), [])

    def test_call_missing_from_the_export(self):
        self.assertEqual(len(oc.call_problems([call("c9", "read")], self.export)), 1)

    def test_same_name_under_another_call_id(self):
        self.assertEqual(len(oc.call_problems([call("c3", "qa_under_score_echo_text")], self.export)), 1)

    def test_shortened_name_with_an_mcp_server_claim(self):
        # The plugin API cannot prove MCP origin, so any claim is a disagreement.
        self.assertEqual(len(oc.call_problems([call("c2", "namespace_custom_echo_text", "qa_under_score")],
                                              self.export)), 2)
        self.assertEqual(len(oc.call_problems([call("c1", "qa_under_score_echo_text", "qa_under_score")],
                                              self.export)), 1)


class Reload(unittest.TestCase):
    def export(self, ran, completed):
        path = os.path.join(self.tmp, "export.json")
        with open(path, "w") as out:
            json.dump({"messages": [{"type": "assistant", "content": [
                {"type": "text", "time": {"ran": 1, "completed": 10**15}},
                {"type": "tool", "time": {"ran": ran, "completed": completed}}]}]}, out)
        return path

    def setUp(self):
        import tempfile
        self.tmp = tempfile.mkdtemp()

    def test_reload_inside_the_tool_call(self):
        self.assertTrue(oc.reload_landed_mid_tool(self.export(1000, 9000), 3000, 4000))

    def test_reload_before_the_tool_started(self):
        self.assertFalse(oc.reload_landed_mid_tool(self.export(5000, 9000), 3000, 4000))

    def test_tool_finished_before_the_reload_did(self):
        self.assertFalse(oc.reload_landed_mid_tool(self.export(1000, 3500), 3000, 4000))

    def test_tool_that_never_ran(self):
        self.assertFalse(oc.reload_landed_mid_tool(self.export(None, 9000), 3000, 4000))


class Comparer(unittest.TestCase):
    """compare() end to end on a synthetic run, with Dash0 replaced by the
    spans each case passes in."""

    REDACTED_IN = '[{"role":"user","parts":[{"type":"text","content":"<REDACTED>"}]}]'
    REDACTED_OUT = '[{"role":"assistant","parts":[{"type":"text","content":"<REDACTED>"}]}]'

    def run_compare(self, tool_attrs, **manifest):
        import tempfile, types
        from unittest import mock
        run = tempfile.mkdtemp()
        chat = {qa.OP: "chat", qa.USAGE_KEYS["input"]: 15, qa.USAGE_KEYS["output"]: 7,
                "gen_ai.input.messages": self.REDACTED_IN, "gen_ai.output.messages": self.REDACTED_OUT}
        tool = {qa.OP: "execute_tool", oc.CALL_ID: "c1", **tool_attrs}
        spans = [{"name": "chat m", "attrs": chat, "trace_id": "t", "span_id": "a", "parent_span_id": ""},
                 {"name": "execute_tool", "attrs": tool, "trace_id": "t", "span_id": "b", "parent_span_id": "a"}]
        export = {"messages": [{"type": "user"}, {"type": "assistant",
                  "tokens": {"input": 10, "output": 5, "reasoning": 2, "cache": {"read": 5}},
                  "content": [{"type": "tool", "id": "c1", "name": "qa_under_score_echo_text",
                               "time": {"ran": 1000, "completed": 9000}}]}]}
        def otlp(span):
            return {"name": span["name"], "attributes": [
                {"key": k, "value": {"intValue": v} if isinstance(v, int) else {"stringValue": v}}
                for k, v in span["attrs"].items()]}
        debug = {"resourceSpans": [{"scopeSpans": [{"spans": [otlp(x) for x in spans]}]}]}
        files = {"manifest.json": {"run_id": "r", "session_id": "s", "turns": 1, "opencode_exit_code": 0,
                                   "started_at": "2026-10-06T00:00:00.000Z",
                                   "ended_at": "2026-10-06T00:01:00.000Z", **manifest},
                 "session-export.json": export}
        for name, body in files.items():
            with open(os.path.join(run, name), "w") as out:
                json.dump(body, out)
        with open(os.path.join(run, "plugin-debug.log"), "w") as out:
            out.write("[dash0:trace] " + json.dumps(debug) + "\n")
        args = types.SimpleNamespace(run_dir=run, dataset=None, limit=100)
        with mock.patch.object(oc.qa, "load_config", return_value=({"dataset": "d"}, None)), \
             mock.patch.object(oc.qa, "query_dash0", return_value=(spans, None)), \
             mock.patch("sys.stdout"):
            try:
                return oc.compare(args)
            except oc.Unmeasurable:
                return 2

    def test_mcp_call_named_as_the_export_names_it(self):
        self.assertEqual(self.run_compare({qa.TOOL: "qa_under_score_echo_text"}), 0)

    def test_shortened_name_claiming_an_mcp_server(self):
        self.assertEqual(self.run_compare({qa.TOOL: "echo_text", oc.MCP_SERVER: "qa_under_score"}), 1)

    def test_call_id_the_export_does_not_have(self):
        self.assertEqual(self.run_compare({qa.TOOL: "qa_under_score_echo_text", oc.CALL_ID: "c9"}), 1)

    def reload(self, started, ended):
        return self.run_compare({qa.TOOL: "qa_under_score_echo_text"}, reload_after="2",
                                exporter_pids_before_reload="1", exporter_pids_after_reload="2",
                                reload_started_ms=started, reload_ended_ms=ended)

    def test_reload_during_the_tool_call(self):
        self.assertEqual(self.reload("3000", "4000"), 0)

    def test_reload_before_the_tool_started(self):
        self.assertEqual(self.reload("500", "800"), 2)


if __name__ == "__main__":
    unittest.main()
