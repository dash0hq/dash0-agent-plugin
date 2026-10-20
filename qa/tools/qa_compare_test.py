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
amp = load("qa_amp_compare", "qa-amp-compare.py")


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


class AmpUsageVerdict(unittest.TestCase):
    """The amp comparer's token accounting. Every case here is a run that used
    to print AGREEMENT while proving nothing."""

    def verdict(self, export_usage, status, children=0, want=10, got=10,
                priced=()):
        return amp.usage_verdict(export_usage, set(status), children, want, got,
                                 list(priced))

    def test_every_turn_matched_and_the_totals_agree(self):
        failures, _ = self.verdict(True, {"matched"})
        self.assertEqual(failures, 0)

    def test_matched_but_the_totals_disagree(self):
        failures, lines = self.verdict(True, {"matched"}, got=9)
        self.assertEqual(failures, 1)
        self.assertIn("must be equal", "".join(lines))

    def test_one_partial_turn_among_matched_ones_fails(self):
        # The run this exists for: the answering-call fix regressing on some
        # turns, which used to print a note and pass.
        failures, lines = self.verdict(True, {"matched", "partial"})
        self.assertEqual(failures, 1)
        self.assertIn("every turn must reach 'matched'", "".join(lines))

    def test_no_turn_matched_fails(self):
        self.assertEqual(self.verdict(True, {"unavailable"})[0], 1)

    def test_usage_off_with_nothing_accounted_passes(self):
        failures, lines = self.verdict(False, {"disabled"})
        self.assertEqual(failures, 0)
        self.assertIn("usage off", "".join(lines))

    def test_usage_off_but_a_root_says_unavailable(self):
        # "unavailable" means the read was attempted and failed; "disabled"
        # means it was never made. Only the second is usage actually off.
        failures, lines = self.verdict(False, {"unavailable"})
        self.assertEqual(failures, 1)
        self.assertIn("must report 'disabled'", "".join(lines))

    def test_usage_off_but_tokens_were_still_reported(self):
        failures, lines = self.verdict(
            False, {"disabled"}, children=2,
            priced=["gen_ai.request.model", "gen_ai.usage.output_tokens"])
        self.assertEqual(failures, 2)
        joined = "".join(lines)
        self.assertIn("no per-model-call chat children", joined)
        self.assertIn("model or token attribute", joined)

    def test_a_root_with_no_usage_status_fails_either_way(self):
        # Every root carries the attribute, so an absent one is a broken
        # export. It used to be discarded from the set, which let the other
        # roots carry the run.
        for export_usage, others in ((True, "matched"), (False, "disabled")):
            with self.subTest(export_usage=export_usage):
                failures, lines = self.verdict(
                    export_usage, {others, "(absent)"})
                self.assertEqual(failures, 1)
                self.assertIn("(absent)", "".join(lines))


class AmpToolComparison(unittest.TestCase):
    def run_compare(self, wrappers, tools):
        import contextlib
        import io
        import pathlib
        import tempfile
        from unittest import mock

        with tempfile.TemporaryDirectory() as directory:
            run = pathlib.Path(directory)
            events = [{"type": "system"}, {"type": "assistant", "message": {
                "content": [{"type": "tool_use", "name": name}
                            for name in wrappers + ["shell_command"] * 2]}}]
            (run / "stream.jsonl").write_text(
                "\n".join(json.dumps(event) for event in events))
            (run / "thread-id").write_text("T-test")
            (run / "meta.json").write_text(json.dumps({"export_usage": False}))
            spans = [{"name": "chat", "attrs": {"dash0.amp.usage.status": "disabled"}}]
            spans += [{"name": "execute_tool " + name, "attrs": {}} for name in tools]
            output = io.StringIO()
            with mock.patch.object(amp.sys, "argv", ["qa-amp-compare.py", directory]), \
                 mock.patch.object(amp.compare, "load_config",
                                   return_value=({"ampDataset": "amp"}, None)), \
                 mock.patch.object(amp, "wait_for_spans", return_value=(spans, None)), \
                 contextlib.redirect_stdout(output):
                return amp.main(), output.getvalue()

    def test_wrappers_do_not_hide_a_missing_ordinary_tool_span(self):
        # Extra MCP spans make the total large enough, but cannot replace a
        # missing second shell_command. Check counts per name, not the total.
        for wrapper in ("tool_search", "code_exec"):
            with self.subTest(wrapper=wrapper):
                status, output = self.run_compare(
                    [wrapper], ["shell_command", "echo_text", "echo_text"])
                self.assertEqual(status, 1)
                self.assertIn("MISMATCH", output)

    def test_wrappers_allow_extra_inner_calls_without_claiming_exact_agreement(self):
        status, output = self.run_compare(
            ["tool_search", "code_exec"], ["shell_command"] * 2 + ["echo_text"])
        self.assertEqual(status, 0)
        self.assertIn("UNPROVABLE", output)
        self.assertNotIn("only wrapper tools", output)

    def test_without_wrappers_tool_counts_must_match_exactly(self):
        for tools, expected in ((["shell_command"] * 2, 0),
                                (["shell_command"] * 2 + ["echo_text"], 1)):
            with self.subTest(tools=tools):
                self.assertEqual(self.run_compare([], tools)[0], expected)


class AmpDriver(unittest.TestCase):
    def test_rerun_clears_logs_but_continued_turns_append(self):
        import pathlib
        import subprocess
        import tempfile

        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            subprocess.run(["git", "init", "-q", directory], check=True)
            (root / "qa/recorder/amp").mkdir(parents=True)
            (root / "qa/recorder/amp/index.ts").write_text("// fixture\n")
            (root / "qa/config.local.json").write_text(json.dumps({
                "ingestUrl": "unused", "authToken": "fake", "ampDataset": "amp",
                "apiUrl": "unused"}))
            (root / "bin").mkdir()
            fake_amp = root / "bin/amp"
            fake_amp.write_text('''#!/bin/sh
if [ "$1 $2" = "threads export" ]; then
  echo '{}'
  exit 0
fi
echo current >> "$QA_AMP_RECORD"
echo current >> "$AMP_PLUGIN_OPTION_DEBUG_FILE"
echo '{"type":"system","session_id":"T-test"}'
''')
            fake_amp.chmod(0o755)
            run = root / "qa/runs/reused"
            run.mkdir(parents=True)
            env = {**os.environ, "PATH": str(root / "bin") + os.pathsep + os.environ["PATH"],
                   "QA_AMP_RESUME": "second turn", "QA_AMP_MCP": "0",
                   "QA_AMP_SKILL": "0", "QA_AMP_TURNS": "", "QA_AMP_EXECUTOR": "local"}
            for _ in range(2):
                for name in ("record.jsonl", "plugin-debug.log"):
                    (run / name).write_text("stale\n")
                result = subprocess.run(
                    ["bash", os.path.join(HERE, "qa-session-amp.sh"), "first turn", "reused"],
                    cwd=directory, env=env, text=True, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                for name in ("record.jsonl", "plugin-debug.log"):
                    self.assertEqual((run / name).read_text(), "current\ncurrent\n")


if __name__ == "__main__":
    unittest.main()
