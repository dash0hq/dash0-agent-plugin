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
pi = load("qa_pi_compare", "qa-pi-compare.py")


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
    def run_compare(self, wrappers, tools, root_statuses=("disabled",)):
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
            events *= len(root_statuses)
            (run / "stream.jsonl").write_text(
                "\n".join(json.dumps(event) for event in events))
            (run / "thread-id").write_text("T-test")
            (run / "meta.json").write_text(json.dumps({"export_usage": False}))
            spans = [{"name": "chat", "attrs": {} if status is None else
                      {"dash0.amp.usage.status": status}} for status in root_statuses]
            spans += [{"name": "execute_tool " + name, "attrs": {}} for name in tools]
            output = io.StringIO()
            with mock.patch.object(amp.sys, "argv", ["qa-amp-compare.py", directory]), \
                 mock.patch.object(amp.compare, "load_config",
                                   return_value=({"ampDataset": "amp"}, None)), \
                 mock.patch.object(amp, "wait_for_spans", return_value=(spans, None)), \
                 contextlib.redirect_stdout(output):
                return amp.main(), output.getvalue()

    def test_an_unlabelled_root_cannot_hide_behind_a_disabled_root(self):
        for second_status, expected in (("disabled", 0), (None, 1)):
            with self.subTest(second_status=second_status):
                status, output = self.run_compare(
                    [], ["shell_command"] * 4, ("disabled", second_status))
                self.assertEqual(status, expected, output)
                if second_status is None:
                    self.assertIn("(absent)", output)

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


class PiParentSessionFile(unittest.TestCase):
    def test_the_parent_is_found_by_header_id_not_by_glob_order(self):
        import pathlib
        import tempfile

        with tempfile.TemporaryDirectory() as directory:
            sessions = pathlib.Path(directory) / "sessions"
            sessions.mkdir()
            # The child sorts first, as glob order may also put it.
            (sessions / "a-child.jsonl").write_text(json.dumps(
                {"type": "session", "id": "child", "parentSession": "x"}) + "\n")
            (sessions / "b-parent.jsonl").write_text(json.dumps(
                {"type": "session", "id": "parent"}) + "\n")
            found = pi.parent_session_file(pathlib.Path(directory), "parent")
            self.assertEqual(found, (sessions / "b-parent.jsonl").resolve())
            self.assertIsNone(pi.parent_session_file(pathlib.Path(directory), "other"))


class PiChildSessionRows(unittest.TestCase):
    def test_a_child_with_no_spans_cannot_be_compared(self):
        original = pi.wait_for_spans
        pi.wait_for_spans = lambda *args: ([], None)
        try:
            own = {"id": "child-session", "calls": 1, "input": 1, "output": 1}
            with self.assertRaises(SystemExit) as stop:
                pi.child_session_rows({}, "pi", own, "parent", "child")
            self.assertEqual(stop.exception.code, 2)
        finally:
            pi.wait_for_spans = original


def pi_span(name, span_id, parent="", trace="T", **attrs):
    base = {"gen_ai.harness.name": "pi"}
    if name.startswith("chat"):
        base["dash0.gen_ai.request.model.original"] = "m"
    return {"name": name, "attrs": {**base, **attrs}, "trace_id": trace, "span_id": span_id,
            "parent_span_id": parent}


class PiRun:
    """A synthetic pi run directory, with Dash0 replaced by a span list."""

    def __init__(self, test, events, spans, meta=None, files=None):
        import pathlib
        import tempfile

        self.test = test
        self.dir = pathlib.Path(tempfile.mkdtemp())
        self.spans = spans
        self.queries = []
        sessions = self.dir / "sessions"
        sessions.mkdir()
        (sessions / "parent.jsonl").write_text(json.dumps({"type": "session", "id": "parent"}) + "\n")
        for name, text in (files or {}).items():
            (self.dir / name).write_text(text)
        (self.dir / "stream.jsonl").write_text("".join(json.dumps(e) + "\n" for e in events))
        (self.dir / "session-id").write_text("parent\n")
        (self.dir / "meta.json").write_text(json.dumps({"pi_exit": 0, **(meta or {})}))
        (self.dir / "manifest.json").write_text(json.dumps({
            "session_id": "parent", "started_at": "2026-11-16T07:42:13.000000Z",
            "ended_at": "2026-11-16T07:42:22.151896Z"}))

    def query(self, config, session, dataset, since, until, limit):
        self.queries.append((session, since, until, limit))
        if callable(self.spans):
            return self.spans(session), None
        return [s for s in self.spans if s["attrs"].get("session", "parent") == session], None

    def run(self):
        """main()'s exit code and printed report."""
        import contextlib
        import io
        import sys
        from unittest import mock

        out = io.StringIO()
        with mock.patch.object(pi.compare, "query_dash0", self.query), \
             mock.patch.object(pi.compare, "load_config", return_value=({"piDataset": "pi"}, None)), \
             mock.patch.object(pi, "SETTLE_SECONDS", 0), \
             mock.patch.object(sys, "argv", ["qa-pi-compare.py", str(self.dir)]), \
             contextlib.redirect_stdout(out), contextlib.redirect_stderr(io.StringIO()):
            try:
                code = pi.main()
            except SystemExit as stop:
                code = stop.code
        return code, out.getvalue()


def pi_call(model="m", input=0, output=0):
    return {"type": "message_end", "message": {"role": "assistant", "model": model,
            "usage": {"input": input, "output": output}}}


class PiTokensPerCall(unittest.TestCase):
    EVENTS = [pi_call(input=10, output=1), pi_call(input=20, output=2), {"type": "agent_settled"}]

    def spans(self, first, second):
        return [pi_span("chat m", "root", **{"gen_ai.usage.input_tokens": first[0],
                                              "gen_ai.usage.output_tokens": first[1]}),
                pi_span("chat m", "call", "root", **{"gen_ai.usage.input_tokens": second[0],
                                                     "gen_ai.usage.output_tokens": second[1]})]

    def test_the_calls_agree_in_any_order(self):
        code, _ = PiRun(self, self.EVENTS, self.spans((20, 2), (10, 1))).run()
        self.assertEqual(code, 0)

    def test_tokens_moved_between_calls_fail_with_equal_totals(self):
        code, out = PiRun(self, self.EVENTS, self.spans((20, 1), (10, 2))).run()
        self.assertEqual(code, 1)
        mismatches = [line for line in out.splitlines() if "MISMATCH" in line]
        self.assertTrue(any("(input, output) per call" in line for line in mismatches), out)
        self.assertFalse(any(line.startswith(("input tokens", "output tokens"))
                             for line in mismatches), out)

    def test_all_tokens_on_one_span_fail(self):
        code, _ = PiRun(self, self.EVENTS, self.spans((30, 3), (0, 0))).run()
        self.assertEqual(code, 1)


def pi_tool(name, call_id, usage=None, details=None):
    result = {}
    if usage is not None:
        result["usage"] = usage
    if details is not None:
        result["details"] = details
    return [{"type": "tool_execution_start", "toolCallId": call_id, "toolName": name},
            {"type": "tool_execution_end", "toolCallId": call_id, "toolName": name,
             "result": result}]


class PiToolUsage(unittest.TestCase):
    """A tool's own model usage is a chat span under the tool, not a model call."""

    EVENTS = [pi_call(input=10, output=1), *pi_tool("web", "t1", {"input": 5, "cacheRead": 2, "output": 3}),
              {"type": "agent_settled"}]

    def spans(self, usage_parent="tool", source="tool"):
        return [pi_span("chat m", "root", **{"gen_ai.usage.input_tokens": 10,
                                              "gen_ai.usage.output_tokens": 1}),
                pi_span("execute_tool web", "tool", "root"),
                {"name": "chat", "trace_id": "T", "span_id": "u", "parent_span_id": usage_parent,
                 "attrs": {"gen_ai.harness.name": "pi", "dash0.pi.usage.source": source,
                           "gen_ai.usage.input_tokens": 7, "gen_ai.usage.output_tokens": 3}}]

    def test_tool_usage_is_not_a_model_call(self):
        code, out = PiRun(self, self.EVENTS, self.spans()).run()
        self.assertEqual(code, 0, out)
        self.assertIn("(tool, input, output) tool usage", out)

    def test_tool_usage_under_the_wrong_span_fails(self):
        code, out = PiRun(self, self.EVENTS, self.spans(usage_parent="root")).run()
        self.assertEqual(code, 1)
        self.assertIn("tool usage", [l for l in out.splitlines() if "MISMATCH" in l][0])

    def test_tool_usage_without_its_marker_is_a_model_call(self):
        code, out = PiRun(self, self.EVENTS, self.spans(source=None)).run()
        self.assertEqual(code, 1)
        self.assertTrue(any(l.startswith("model calls") and "MISMATCH" in l
                            for l in out.splitlines()), out)

    def test_missing_tool_usage_span_fails(self):
        code, _ = PiRun(self, self.EVENTS, self.spans()[:2]).run()
        self.assertEqual(code, 1)

    def test_rows_of_one_tool_sort_the_same_on_both_sides(self):
        # Sorted by value, (web, 9, 1) comes before (web, 10, 1). Sorted by
        # repr, it comes after. Both sides must use the same order.
        events = [pi_call(input=10, output=1),
                  *pi_tool("web", "t1", {"input": 9, "output": 1}),
                  *pi_tool("web", "t2", {"input": 10, "output": 1}),
                  {"type": "agent_settled"}]

        def usage_span(span_id, parent, tokens):
            return {"name": "chat", "trace_id": "T", "span_id": span_id, "parent_span_id": parent,
                    "attrs": {"gen_ai.harness.name": "pi", "dash0.pi.usage.source": "tool",
                              "gen_ai.usage.input_tokens": tokens, "gen_ai.usage.output_tokens": 1}}
        spans = [pi_span("chat m", "root", **{"gen_ai.usage.input_tokens": 10,
                                               "gen_ai.usage.output_tokens": 1}),
                 pi_span("execute_tool web", "tool1", "root"),
                 pi_span("execute_tool web", "tool2", "root"),
                 usage_span("u1", "tool1", 9), usage_span("u2", "tool2", 10)]
        code, out = PiRun(self, events, spans).run()
        self.assertEqual(code, 0, out)

    def test_children_totals_are_not_tool_usage(self):
        usage = {"input": 4, "output": 9}
        events = [*pi_tool("subagent", "s", usage), *pi_tool("bg_wait", "w", usage,
                                                             {"mode": "management", "results": []}),
                  *pi_tool("web", "t", {"input": 0, "output": 0})]
        self.assertEqual(pi.expectation(events)["tool_usage"], [])

    def test_tintinweb_subagents_tool_usage_is_a_children_total(self):
        usage = {"input": 4, "output": 9}
        events = [*pi_tool("Agent", "a", usage, {}),
                  *pi_tool("get_subagent_result", "g", usage, {}),
                  *pi_tool("steer_subagent", "s", usage, {}),
                  *pi_tool("read", "r", usage)]
        self.assertEqual(pi.expectation(events)["tool_usage"], [("read", 4, 9)])


class PiAbortedTurn(unittest.TestCase):
    """A turn that the user stopped is marked by an attribute, not by an error."""

    EVENTS = [pi_call(), {"type": "agent_settled", "aborted": True}]

    def test_aborted_turn_carries_the_status_attribute(self):
        spans = [pi_span("chat m", "root", **{"dash0.pi.turn.status": "aborted"})]
        code, out = PiRun(self, self.EVENTS, spans).run()
        self.assertEqual(code, 0, out)

    def test_aborted_turn_without_the_attribute_fails(self):
        code, out = PiRun(self, self.EVENTS, [pi_span("chat m", "root")]).run()
        self.assertEqual(code, 1)
        self.assertTrue(any(l.startswith("aborted turns") and "MISMATCH" in l
                            for l in out.splitlines()), out)


class PiRunWindow(unittest.TestCase):
    EVENTS = [pi_call(), {"type": "agent_settled"}]

    def test_the_query_uses_the_manifest_window(self):
        run = PiRun(self, self.EVENTS, [pi_span("chat m", "root")])
        self.assertEqual(run.run()[0], 0)
        _, since, until, _ = run.queries[0]
        self.assertEqual((since, until), ("2026-11-16T07:41:13.000Z", "2026-11-16T07:44:22.000Z"))

    def test_a_full_page_of_spans_cannot_be_compared(self):
        spans = [pi_span("chat m", f"s{n}") for n in range(pi.SPAN_LIMIT)]
        self.assertEqual(PiRun(self, self.EVENTS, spans).run()[0], 2)

    def test_a_missing_manifest_cannot_be_compared(self):
        run = PiRun(self, self.EVENTS, [pi_span("chat m", "root")])
        (run.dir / "manifest.json").unlink()
        self.assertEqual(run.run()[0], 2)


class PiSubagents(unittest.TestCase):
    """Foreground children: agent spans under the subagent call."""

    def child_file(self, run_dir, name, parent, usage):
        path = run_dir / name
        path.write_text(json.dumps({"type": "session", "id": name, "parentSession": parent}) + "\n"
                        + json.dumps({"message": {"role": "assistant", "model": "m",
                                                  "usage": usage}}) + "\n")
        return str(path)

    def events(self, children):
        return [pi_call(input=5, output=1),
                {"type": "tool_execution_end", "toolName": "subagent", "toolCallId": "call-1",
                 "result": {"details": {"results": children}}},
                {"type": "agent_settled"}]

    def spans(self, agents, parent_trace="T"):
        out = [pi_span("chat m", "root", **{"gen_ai.usage.input_tokens": 5,
                                             "gen_ai.usage.output_tokens": 1}),
               pi_span("execute_tool subagent", "tool", "root", **{"gen_ai.tool.call.id": "call-1"})]
        for n, (name, tokens) in enumerate(agents):
            out.append(pi_span(f"invoke_agent {name}", f"a{n}", "tool",
                               **{"gen_ai.agent.name": name}))
            out.append(pi_span("chat m", f"u{n}", f"a{n}", **{
                "gen_ai.usage.input_tokens": tokens[0], "gen_ai.usage.output_tokens": tokens[1]}))
        return out

    def test_no_child_in_a_subagent_run_cannot_be_compared(self):
        events = [pi_call(), {"type": "agent_settled"}]
        run = PiRun(self, events, [pi_span("chat m", "root")], meta={"pi_subagents": "0.88.0"})
        self.assertEqual(run.run()[0], 2)

    def two_children(self, agents):
        """A run with children alpha (10, 1) and beta (20, 2), each with its file."""
        run = PiRun(self, [], [])
        parent = str(run.dir / "sessions" / "parent.jsonl")
        children = []
        for name, usage in (("alpha", {"input": 10, "output": 1}),
                            ("beta", {"input": 20, "output": 2})):
            children.append({"agent": name, "usage": usage,
                             "sessionFile": self.child_file(run.dir, name, parent, usage)})
        (run.dir / "stream.jsonl").write_text(
            "".join(json.dumps(e) + "\n" for e in self.events(children)))
        run.spans = self.spans(agents)
        return run.run()

    def test_parallel_children_match_by_name_not_by_order(self):
        # Dash0 returns beta first.
        code, out = self.two_children([("beta", (20, 2)), ("alpha", (10, 1))])
        self.assertEqual(code, 0, out)

    def test_swapped_child_tokens_fail(self):
        code, out = self.two_children([("alpha", (20, 2)), ("beta", (10, 1))])
        self.assertEqual(code, 1, out)
        mismatches = [line for line in out.splitlines() if "MISMATCH" in line]
        self.assertTrue(any("tokens on chat under agent" in line for line in mismatches), out)

    def test_a_foreground_child_with_no_session_file_is_a_mismatch(self):
        children = [{"agent": "alpha", "usage": {"input": 10, "output": 1},
                     "sessionFile": "/nonexistent/child.jsonl"}]
        code, out = PiRun(self, self.events(children), self.spans([("alpha", (10, 1))])).run()
        self.assertEqual(code, 1)
        self.assertIn("child alpha file", out)

    def test_an_agent_outside_the_parent_trace_is_a_mismatch(self):
        children = [{"agent": "alpha", "usage": {"input": 10, "output": 1}}]
        spans = self.spans([("alpha", (10, 1))])
        # The tool span and the agent under it are in another trace than the
        # parent's chat root. The agent and its direct parent still agree, so
        # only a check against the chat root sees it.
        for span in spans:
            if span["span_id"] in ("tool", "a0", "u0"):
                span["trace_id"] = "OTHER"
        code, out = PiRun(self, self.events(children), spans).run()
        self.assertEqual(code, 1, out)
        mismatches = [line for line in out.splitlines() if "MISMATCH" in line]
        self.assertTrue(any("same trace as parent chat root" in line for line in mismatches), out)

    def test_an_agent_whose_chain_does_not_reach_a_chat_root_is_a_mismatch(self):
        children = [{"agent": "alpha", "usage": {"input": 10, "output": 1}}]
        spans = [s for s in self.spans([("alpha", (10, 1))])]
        for span in spans:
            if span["span_id"] == "tool":
                span["parent_span_id"] = "lost"
        code, out = PiRun(self, self.events(children), spans).run()
        self.assertEqual(code, 1, out)

    def test_the_parent_path_is_compared_resolved(self):
        run = PiRun(self, [], [])
        # Pi writes the parent path as it was given, here through a symlink,
        # as /tmp is on macOS.
        link = run.dir.parent / (run.dir.name + "-link")
        link.symlink_to(run.dir)
        self.addCleanup(link.unlink)
        usage = {"input": 10, "output": 1}
        child = self.child_file(run.dir, "child.jsonl", str(link / "sessions" / "parent.jsonl"), usage)
        children = [{"agent": "alpha", "usage": usage, "sessionFile": child}]
        (run.dir / "stream.jsonl").write_text(
            "".join(json.dumps(e) + "\n" for e in self.events(children)))
        run.spans = self.spans([("alpha", (10, 1))])
        code, out = run.run()
        self.assertEqual(code, 0, out)


class PiSessionFile(unittest.TestCase):
    def write(self, text):
        import pathlib
        import tempfile

        path = pathlib.Path(tempfile.mkdtemp()) / "child.jsonl"
        path.write_text(text)
        return str(path)

    def test_blank_lines_are_skipped(self):
        header = json.dumps({"type": "session", "id": "abc"})
        message = json.dumps({"message": {"role": "assistant", "model": "m",
                                          "usage": {"input": 2, "output": 1}}})
        own = pi.session_file_usage(self.write(f"{header}\n\n{message}\n\n"))
        self.assertEqual((own["id"], own["calls"], own["input"]), ("abc", 1, 2))

    def test_a_cut_off_line_cannot_be_compared(self):
        header = json.dumps({"type": "session", "id": "abc"})
        with self.assertRaises(SystemExit) as stop:
            pi.session_file_usage(self.write(header + '\n{"message": {"ro'))
        self.assertEqual(stop.exception.code, 2)

    def test_a_file_with_no_header_cannot_be_compared(self):
        with self.assertRaises(SystemExit) as stop:
            pi.session_file_usage(self.write(json.dumps({"message": {}}) + "\n"))
        self.assertEqual(stop.exception.code, 2)


class PiDriver(unittest.TestCase):
    def drive(self, root, run_id):
        import subprocess

        return subprocess.run(["bash", os.path.join(HERE, "qa-session-pi.sh"), "unused", run_id],
                              cwd=root, text=True, capture_output=True)

    def setUp(self):
        import pathlib
        import subprocess
        import tempfile

        self.root = pathlib.Path(tempfile.mkdtemp())
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)
        (self.root / "qa").mkdir()
        (self.root / "qa/config.local.json").write_text(json.dumps({
            "ingestUrl": "unused", "authToken": "fake", "piDataset": "pi", "apiUrl": "unused"}))

    def test_a_run_id_with_a_path_is_refused(self):
        keep = self.root / "qa/keep"
        keep.write_text("evidence")
        for run_id in ("..", "../qa", "a/b", ".", "a..b"):
            with self.subTest(run_id=run_id):
                result = self.drive(self.root, run_id)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertIn("plain directory name", result.stderr)
        self.assertTrue(keep.exists())

    def test_an_existing_run_is_not_replaced(self):
        run = self.root / "qa/runs/earlier"
        run.mkdir(parents=True)
        (run / "stream.jsonl").write_text("evidence")
        result = self.drive(self.root, "earlier")
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("exists", result.stderr)
        self.assertEqual((run / "stream.jsonl").read_text(), "evidence")


class AmpDriver(unittest.TestCase):
    def test_incomplete_config_stops_before_starting_amp(self):
        import pathlib
        import subprocess
        import tempfile

        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            subprocess.run(["git", "init", "-q", directory], check=True)
            (root / "qa/recorder/amp").mkdir(parents=True)
            (root / "qa/recorder/amp/index.ts").write_text("// fixture\n")
            (root / "bin").mkdir()
            fake_amp = root / "bin/amp"
            fake_amp.write_text('#!/bin/sh\ntouch amp-was-started\nexit 1\n')
            fake_amp.chmod(0o755)
            config = {"ingestUrl": "unused", "authToken": "fake",
                      "ampDataset": "amp", "apiUrl": "unused"}
            # Stale environment values must not fill in a missing config key.
            env = {**os.environ,
                   "PATH": str(root / "bin") + os.pathsep + os.environ["PATH"],
                   "QA_OTLP_URL": "unused", "QA_AUTH_TOKEN": "fake",
                   "QA_DATASET": "stale-dataset", "QA_API_URL": "unused",
                   "QA_AMP_MCP": "0", "QA_AMP_SKILL": "0"}
            for missing in config:
                with self.subTest(missing=missing):
                    (root / "amp-was-started").unlink(missing_ok=True)
                    (root / "qa/config.local.json").write_text(json.dumps(
                        {key: value for key, value in config.items() if key != missing}))
                    result = subprocess.run(
                        ["bash", os.path.join(HERE, "qa-session-amp.sh"), "unused", "invalid"],
                        cwd=directory, env=env, text=True, capture_output=True)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(missing + " is missing", result.stderr)
                    self.assertFalse((root / "amp-was-started").exists(), result.stderr)

    def test_rerun_clears_logs_and_preserves_every_continued_turn(self):
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
for prompt do :; done
printf '%s\\n' "$prompt" >> "$QA_AMP_RECORD"
printf '%s\\n' "$prompt" >> "$AMP_PLUGIN_OPTION_DEBUG_FILE"
echo '{"type":"system","session_id":"T-test"}'
''')
            fake_amp.chmod(0o755)
            run = root / "qa/runs/reused"
            run.mkdir(parents=True)
            turns = root / "turns.txt"
            env = {**os.environ, "PATH": str(root / "bin") + os.pathsep + os.environ["PATH"],
                   "QA_AMP_RESUME": "second turn", "QA_AMP_MCP": "0",
                   "QA_AMP_SKILL": "0", "QA_AMP_TURNS": str(turns), "QA_AMP_EXECUTOR": "local"}
            for ending in ("\n", ""):
                turns.write_text("third turn\n\nfourth \\turn" + ending)
                for name in ("record.jsonl", "plugin-debug.log"):
                    (run / name).write_text("stale\n")
                result = subprocess.run(
                    ["bash", os.path.join(HERE, "qa-session-amp.sh"), "first turn", "reused"],
                    cwd=directory, env=env, text=True, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                for name in ("record.jsonl", "plugin-debug.log"):
                    self.assertEqual((run / name).read_text(),
                                     "first turn\nsecond turn\nthird turn\nfourth \\turn\n")


if __name__ == "__main__":
    unittest.main()
