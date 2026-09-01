#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
"""Drive one interactive `cursor-agent` session through a pseudo-terminal.

Cursor has a headless mode, `cursor-agent -p`, and it is the wrong tool for this
job. Measured 2026-09-01 against cursor-agent 2026.08.31: print mode fires
sessionStart, preToolUse, postToolUse and sessionEnd, and it fires neither
beforeSubmitPrompt nor afterAgentResponse. afterAgentResponse is the only place
Cursor exposes token usage, and internal/source/cursor renames it to Stop, which
is the single event internal/pipeline turns into a `chat` span. So a print-mode
run produces tool spans and no turn: no chat span, no model, no tokens.

The interactive TUI fires the full set. It needs a terminal, so this script gives
it one and types into it.

Turn completion is read from the recorder's own index rather than from the
screen. The terminal is a spinner, a token counter and a re-drawn prompt box, and
scraping it for "the model stopped" was flaky in every form tried. The recording
is exact: one afterAgentResponse row per completed turn, appended by a separate
process the moment Cursor fires it. Waiting on that also means the driver cannot
declare a turn done before the evidence for it exists.

The session is ended with Ctrl-D. Measured the same day: Ctrl-D exits and fires
sessionEnd, while `/quit` and two Ctrl-Cs both leave the process running until it
is killed — and a killed session delivers no sessionEnd, so the pipeline never
closes the session and the run loses its last span.

Usage:
  qa/tools/qa-cursor-drive.py --project DIR --index PATH --tty-log PATH \\
      [--index-baseline N] --prompt "first turn" [--prompt "second turn"] \\
      [--model MODEL]

Exit codes:
  0  every prompt produced a completed turn, and the session ended cleanly
  1  a turn did not complete, or sessionEnd never fired
  2  the session never started: cursor-agent failed before the first hook
"""

import argparse
import json
import os
import pty
import re
import select
import signal
import sys
import time

# Stripped from the captured terminal log. The TUI repaints constantly, so a raw
# capture is mostly escape sequences and unreadable in a run directory.
ANSI = re.compile(rb"\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[()][A-B]")


def rows(index_path, skip=0):
    """Every recorded hook row after `skip` lines, tolerating a partial tail.

    The recorder appends one line per invocation from a separate process, so a
    read can land mid-write. A partial last line is skipped rather than raised:
    the next poll sees it complete.

    `skip` is how many lines the index already held before this run started. A
    reused run id keeps the earlier rows on purpose, and every count here is a
    readiness signal — counting a previous run's sessionStart makes the driver
    skip waiting and type into a TUI that is not up yet.
    """
    if not os.path.exists(index_path):
        return []
    out = []
    with open(index_path) as handle:
        for lineno, line in enumerate(handle):
            if lineno < skip:
                continue
            try:
                out.append(json.loads(line))
            except json.JSONDecodeError:
                continue
    return out


def count(index_path, event, skip=0):
    return sum(1 for r in rows(index_path, skip) if r.get("hook_event_name") == event)


class Session:
    """One cursor-agent process on a pty, with its output accumulated."""

    def __init__(self, project, argv, env, tty_log):
        self.tty_log = tty_log
        self.buf = bytearray()
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.chdir(project)
            os.environ.update(env)
            # A fixed geometry, so the TUI wraps the same way on every machine and
            # a captured log is diffable between runs.
            os.environ.setdefault("TERM", "xterm-256color")
            os.environ["COLUMNS"] = "120"
            os.environ["LINES"] = "48"
            os.execvp(argv[0], argv)

    def pump(self, seconds):
        """Read whatever the terminal produced, for at most `seconds`."""
        deadline = time.time() + seconds
        while time.time() < deadline:
            ready, _, _ = select.select([self.fd], [], [], min(0.3, max(0.01, deadline - time.time())))
            if not ready:
                continue
            try:
                chunk = os.read(self.fd, 65536)
            except OSError:
                return False
            if not chunk:
                return False
            self.buf.extend(chunk)
        return True

    def type(self, text):
        """Type one line and submit it.

        The newline is a separate write after a pause. Sent in one buffer with
        the text, the TUI treated it as a pasted block and kept the prompt in the
        composer instead of submitting it.
        """
        os.write(self.fd, text.encode())
        self.pump(0.6)
        os.write(self.fd, b"\r")

    def exited(self):
        try:
            pid, _ = os.waitpid(self.pid, os.WNOHANG)
        except ChildProcessError:
            return True
        return pid != 0

    def finish(self):
        """Ctrl-D, then wait for the process to go. Kill only as a last resort.

        Both failure paths below reach here with cursor-agent already gone, and a
        write to the master of a pty whose child has exited raises EIO. Unguarded,
        that traceback replaced the diagnosis the caller was about to print and
        lost the terminal log with it — the one account of what a failed run
        showed. So the write is allowed to fail, and the log is saved either way.
        """
        try:
            os.write(self.fd, b"\x04")
        except OSError:
            self.save()
            return
        for _ in range(40):
            self.pump(0.5)
            if self.exited():
                break
        else:
            os.kill(self.pid, signal.SIGKILL)
            self.pump(1)
        self.save()

    def save(self):
        with open(self.tty_log, "wb") as handle:
            handle.write(ANSI.sub(b"", bytes(self.buf)))


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--project", required=True)
    parser.add_argument("--index", required=True,
                        help="the recorder's index.jsonl; turn completion is read from it")
    parser.add_argument("--index-baseline", type=int, default=0,
                        help="lines the index already held before this run; rows"
                             " before it belong to an earlier run under the same"
                             " run id and are not evidence about this one")
    parser.add_argument("--tty-log", required=True)
    parser.add_argument("--prompt", action="append", required=True,
                        help="one per turn, submitted in order")
    parser.add_argument("--model", default=None)
    parser.add_argument("--start-timeout", type=float, default=60.0)
    parser.add_argument("--turn-timeout", type=float, default=300.0)
    args = parser.parse_args()

    argv = ["cursor-agent", "--force", "--trust"]
    if args.model:
        argv += ["--model", args.model]

    session = Session(args.project, argv, {}, args.tty_log)

    # Readiness is sessionStart in the recording, not a prompt drawn on screen.
    # An unauthenticated or crashed cursor-agent draws nothing and fires nothing,
    # and this is the only signal that separates "still starting" from "never
    # started".
    deadline = time.time() + args.start_timeout
    while time.time() < deadline and not count(args.index, "sessionStart", args.index_baseline):
        if session.exited():
            break
        session.pump(0.5)
    if not count(args.index, "sessionStart", args.index_baseline):
        session.finish()
        print("qa: cursor-agent fired no sessionStart. The session never started;"
              " see the tty log.", file=sys.stderr)
        return 2

    # The TUI accepts input before it has finished its first paint, and a prompt
    # typed then lands in a composer that is about to be redrawn empty.
    session.pump(3)

    completed = 0
    for turn, prompt in enumerate(args.prompt, start=1):
        session.type(prompt)
        deadline = time.time() + args.turn_timeout
        while time.time() < deadline:
            if count(args.index, "afterAgentResponse", args.index_baseline) >= turn:
                completed = turn
                break
            if session.exited():
                break
            session.pump(1)
        if completed != turn:
            session.finish()
            print(f"qa: turn {turn} produced no afterAgentResponse within"
                  f" {args.turn_timeout:.0f}s. Cursor fires that event once per"
                  " completed turn, so the turn did not finish.", file=sys.stderr)
            return 1
        # Cursor writes the transcript and the remaining hooks just after
        # afterAgentResponse. Submitting the next prompt into that window raced
        # the repaint and dropped characters.
        session.pump(2)

    session.finish()
    if not count(args.index, "sessionEnd", args.index_baseline):
        print("qa: the session ended without a sessionEnd hook, so the pipeline"
              " never closed it. Ctrl-D should produce one.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
