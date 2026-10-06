#!/usr/bin/env python3
"""A fake OpenAI-compatible model for Copilot app QA runs.

The app accepts a custom model provider. Pointing one QA session's model at
this server makes the model's behaviour a property of the run: a failure on
demand, a recovery, or a fixed token count. Only that session's model calls
come here, so nothing else on the machine changes.

  qa-fake-model.py serve --port 8765 --log qa/runs/<run-id>/fake-model.jsonl [--mode ok]
  qa-fake-model.py self-test

Modes:
  ok            reply with --reply, reporting --prompt-tokens/--completion-tokens
  error         answer --status with --message, every time
  fail-once     answer --status with --message once, then behave as ok
  context       answer 400 with code context_length_exceeded
  hang          start the stream and send nothing more, until the client goes

The log has one line per request: what was asked and what was answered,
including the usage reported. It is written by this server, not the plugin, so
it is an independent record of every model call and its tokens. Request
headers are never logged.
"""
import argparse
import json
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MODEL = "qa-fake"


class State:
    def __init__(self, args):
        self.args = args
        self.calls = 0
        self.lock = threading.Lock()

    def log(self, entry):
        if not self.args.log:
            return
        with self.lock, open(self.args.log, "a") as handle:
            handle.write(json.dumps(entry) + "\n")


def make_handler(state):
    a = state.args

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def send_json(self, status, body):
            data = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def do_GET(self):
            if self.path.rstrip("/").endswith("/models"):
                self.send_json(200, {"object": "list", "data": [
                    {"id": MODEL, "object": "model", "created": 0, "owned_by": "qa"}]})
            else:
                self.send_json(404, {"error": {"message": "not found"}})

        def do_POST(self):
            length = int(self.headers.get("Content-Length") or 0)
            try:
                req = json.loads(self.rfile.read(length) or b"{}")
            except ValueError:
                req = {}
            with state.lock:
                state.calls += 1
                call = state.calls
            entry = {"call": call, "at": time.strftime("%Y-%m-%dT%H:%M:%S%z"), "path": self.path,
                     "model": req.get("model"), "stream": bool(req.get("stream")),
                     "messages": len(req.get("messages") or req.get("input") or []),
                     "tools": len(req.get("tools") or [])}

            mode = a.mode
            if mode == "fail-once" and call > 1:
                mode = "ok"
            if mode in ("error", "fail-once"):
                entry.update(status=a.status, error=a.message)
                state.log(entry)
                return self.send_json(a.status, {"error": {
                    "message": a.message, "type": "invalid_request_error", "code": a.code}})
            if mode == "context":
                entry.update(status=400, error="context_length_exceeded")
                state.log(entry)
                return self.send_json(400, {"error": {
                    "message": "This model's maximum context length was exceeded.",
                    "type": "invalid_request_error", "code": "context_length_exceeded"}})

            usage = {"prompt_tokens": a.prompt_tokens, "completion_tokens": a.completion_tokens,
                     "total_tokens": a.prompt_tokens + a.completion_tokens}
            entry.update(status=200, usage=usage if mode == "ok" else None)
            state.log(entry)
            rid = f"chatcmpl-qa-{call}"

            if not req.get("stream"):
                return self.send_json(200, {
                    "id": rid, "object": "chat.completion", "created": int(time.time()), "model": MODEL,
                    "choices": [{"index": 0, "finish_reason": "stop",
                                 "message": {"role": "assistant", "content": a.reply}}],
                    "usage": usage})

            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.end_headers()

            def chunk(delta, finish=None, with_usage=False):
                body = {"id": rid, "object": "chat.completion.chunk", "created": int(time.time()),
                        "model": MODEL, "choices": [{"index": 0, "delta": delta, "finish_reason": finish}]}
                if with_usage:
                    body["choices"] = []
                    body["usage"] = usage
                self.wfile.write(f"data: {json.dumps(body)}\n\n".encode())
                self.wfile.flush()

            try:
                chunk({"role": "assistant", "content": ""})
                if mode == "hang":
                    # Keep the connection open until the client stops the turn.
                    for _ in range(600):
                        time.sleep(0.5)
                        self.wfile.write(b": keep-alive\n\n")
                        self.wfile.flush()
                    return
                chunk({"content": a.reply})
                chunk({}, finish="stop")
                chunk({}, with_usage=True)
                self.wfile.write(b"data: [DONE]\n\n")
                self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                state.log({"call": call, "client_closed": True})

    return Handler


def parser():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="command", required=True)
    s = sub.add_parser("serve")
    s.add_argument("--port", type=int, default=8765)
    s.add_argument("--log")
    s.add_argument("--mode", choices=["ok", "error", "fail-once", "context", "hang"], default="ok")
    s.add_argument("--status", type=int, default=400)
    s.add_argument("--message", default="qa fake model error")
    s.add_argument("--code", default="qa_fake_error")
    s.add_argument("--reply", default="done")
    s.add_argument("--prompt-tokens", type=int, default=1000)
    s.add_argument("--completion-tokens", type=int, default=10)
    sub.add_parser("self-test")
    return p


def start(argv):
    args = parser().parse_args(["serve", "--port", "0"] + argv)
    server = ThreadingHTTPServer(("127.0.0.1", args.port), make_handler(State(args)))
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, f"http://127.0.0.1:{server.server_address[1]}/v1"


def post(base, stream=False):
    req = urllib.request.Request(f"{base}/chat/completions", method="POST",
                                 data=json.dumps({"model": MODEL, "stream": stream,
                                                  "messages": [{"role": "user", "content": "hi"}]}).encode(),
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.status, resp.read().decode()
    except urllib.error.HTTPError as err:
        return err.code, err.read().decode()


def self_test():
    import tempfile
    log = tempfile.NamedTemporaryFile(suffix=".jsonl", delete=False).name

    server, base = start(["--log", log, "--prompt-tokens", "1234", "--completion-tokens", "7"])
    status, body = post(base)
    assert status == 200 and json.loads(body)["usage"]["prompt_tokens"] == 1234, body
    status, body = post(base, stream=True)
    events = [json.loads(line[6:]) for line in body.splitlines() if line.startswith("data: {")]
    assert "".join(c["delta"].get("content", "") for e in events for c in e["choices"]) == "done", body
    assert events[-1]["usage"]["completion_tokens"] == 7 and body.rstrip().endswith("data: [DONE]"), body
    with urllib.request.urlopen(f"{base}/models", timeout=5) as resp:
        assert json.load(resp)["data"][0]["id"] == MODEL
    server.shutdown()

    server, base = start(["--mode", "fail-once", "--status", "429", "--message", "QA-ERR-x"])
    status, body = post(base)
    assert status == 429 and json.loads(body)["error"]["message"] == "QA-ERR-x", body
    assert post(base)[0] == 200
    server.shutdown()

    server, base = start(["--mode", "context"])
    status, body = post(base)
    assert status == 400 and json.loads(body)["error"]["code"] == "context_length_exceeded"
    server.shutdown()

    rows = [json.loads(line) for line in open(log)]
    assert [r["call"] for r in rows] == [1, 2] and rows[0]["usage"]["prompt_tokens"] == 1234, rows
    print("self-test ok")


def main():
    args = parser().parse_args()
    if args.command == "self-test":
        self_test()
        return 0
    server = ThreadingHTTPServer(("127.0.0.1", args.port), make_handler(State(args)))
    print(f"qa-fake-model: {args.mode} on http://127.0.0.1:{args.port}/v1 (model {MODEL})", file=sys.stderr)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
