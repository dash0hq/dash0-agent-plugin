#!/usr/bin/env python3
"""Prepare and collect a GitHub Copilot app QA run.

The app has no headless mode, so the session itself is driven by hand, from
another app session with send_session_message, or through the GUI. This does
everything around it:

  qa-app-run.py prepare <run-id> <session-cwd> [--omit-io] [--fake-model]
  qa-app-run.py collect <run-id> [--session-id <id>]

prepare checks that the installed extension is the working tree's, builds the
binary into the bootstrap's cache, and writes the session's project config,
pointing debug_file into the run directory. The runner runs it in the session's
worktree after create_session and before send_session_message: the config is
untracked, so the worktree never has it otherwise, and the session would fall
back to the user's own ~/.copilot/dash0-agent-plugin.local.md.

--fake-model pins the session's model to qa-fake, the provider
qa-fake-model.py serves, through the repository settings file
.github/copilot/settings.local.json, which the runtime reads when the session
starts. Without it, prepare removes that file, so an earlier pin never carries
into a run meant for a real model.

collect finds the session (or takes --session-id), copies its events.jsonl,
writes manifest.json, and saves Dash0's spans for it. qa-attrs.py reads the
same run directory.

Exit 0 on success, 1 when a precondition fails, 2 when something could not be
read. The auth token is never printed.
"""
import argparse
import datetime
import importlib.util
import json
import os
import shutil
import subprocess
import sys

ROOT = subprocess.run(["git", "rev-parse", "--show-toplevel"],
                      capture_output=True, text=True, check=True).stdout.strip()
HERE = os.path.dirname(os.path.abspath(__file__))
EXTENSION = os.path.expanduser("~/.copilot/extensions/copilot-app")
SESSIONS = os.path.expanduser("~/.copilot/session-state")


def compare():
    spec = importlib.util.spec_from_file_location("qa_compare", os.path.join(HERE, "qa-compare.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def now():
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000+00:00")


def binary_path():
    version = next(line.split('"')[1] for line in open(os.path.join(ROOT, "copilot-app", "copilot-app-on-event.sh"))
                   if line.startswith("VERSION="))
    goos, goarch = (subprocess.run(["go", "env", v], capture_output=True, text=True, check=True).stdout.strip()
                    for v in ("GOOS", "GOARCH"))
    return os.path.expanduser(
        f"~/.local/state/dash0-agent-plugin/copilot-app/bin/copilot-app-on-event-{version}-{goos}-{goarch}")


def fake_model_id():
    """The provider-qualified id the app gives qa-fake, e.g. <provider-uuid>/qa-fake."""
    import sqlite3
    db = sqlite3.connect(f"file:{os.path.expanduser('~/.copilot/data.db')}?mode=ro", uri=True)
    row = db.execute("SELECT provider_id FROM provider_models WHERE model_id = 'qa-fake'").fetchone()
    db.close()
    return f"{row[0]}/qa-fake" if row else None


def pin_model(cwd, model):
    """Write, or with model None remove, the session's repository model setting."""
    path = os.path.join(cwd, ".github", "copilot", "settings.local.json")
    if model is None:
        if os.path.exists(path):
            os.remove(path)
        return None
    os.makedirs(os.path.dirname(path), exist_ok=True)
    ignore = os.path.join(os.path.dirname(path), ".gitignore")
    if not os.path.exists(ignore):
        with open(ignore, "w") as handle:
            handle.write("settings.local.json\n.gitignore\n")
    with open(path, "w") as handle:
        json.dump({"model": model}, handle)
    return path


def prepare(args, config):
    cwd = os.path.abspath(os.path.expanduser(args.session_cwd))
    if not os.path.isdir(cwd):
        print(f"{cwd} does not exist. Create the session in the app first.", file=sys.stderr)
        return 1
    # The config holds the QA token, so it must be a file git ignores. A
    # .copilot/.gitignore of "*" makes it one in any repository.
    os.makedirs(os.path.join(cwd, ".copilot"), exist_ok=True)
    ignore = os.path.join(cwd, ".copilot", ".gitignore")
    if not os.path.exists(ignore):
        with open(ignore, "w") as handle:
            handle.write("*\n")
    if subprocess.run(["git", "-C", cwd, "check-ignore", "-q", ".copilot/dash0-agent-plugin.local.md"],
                      capture_output=True).returncode != 0:
        print(f"git does not ignore {cwd}/.copilot/dash0-agent-plugin.local.md; refusing to write the QA "
              "token there.", file=sys.stderr)
        return 1
    # One copy only: a second, in the repo's .github/extensions/, joins the
    # same session and every span arrives twice.
    if subprocess.run(["diff", "-rq", os.path.join(ROOT, "copilot-app"), EXTENSION],
                      capture_output=True).returncode != 0:
        print(f"{EXTENSION} is missing or differs from copilot-app/. Install the working tree's copy.",
              file=sys.stderr)
        return 1
    if os.path.exists(os.path.join(cwd, ".github", "extensions")):
        print(f"{cwd}/.github/extensions exists next to the user-level install; spans would arrive twice.",
              file=sys.stderr)
        return 1

    # -buildvcs=false so the build is a function of the code alone, and the
    # binary-is-the-working-tree check can compare bytes.
    binary = binary_path()
    os.makedirs(os.path.dirname(binary), exist_ok=True)
    subprocess.run(["go", "build", "-buildvcs=false", "-o", binary, "./cmd/copilot-app-on-event"],
                   cwd=ROOT, check=True)

    run_dir = os.path.join(ROOT, "qa", "runs", args.run_id)
    os.makedirs(run_dir, exist_ok=True)
    os.makedirs(os.path.join(cwd, ".copilot"), exist_ok=True)
    path = os.path.join(cwd, ".copilot", "dash0-agent-plugin.local.md")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as handle:
        handle.write("---\n"
                     f'otlp_url: "{config["ingestUrl"]}"\n'
                     f'auth_token: "{config["authToken"]}"\n'
                     f'dataset: "{config["dataset"]}"\n'
                     'debug: "true"\n'
                     f'debug_file: "{os.path.join(run_dir, "plugin-debug.log")}"\n'
                     f'omit_io: "{"true" if args.omit_io else "false"}"\n'
                     "---\n")
    model = None
    if args.fake_model:
        model = fake_model_id()
        if not model:
            print("the app has no qa-fake model. Add the provider once: see the fake model in setup.md.",
                  file=sys.stderr)
            return 1
    pin_model(cwd, model)
    with open(os.path.join(run_dir, "started-at"), "w") as handle:
        handle.write(now())
    with open(os.path.join(run_dir, "session-cwd"), "w") as handle:
        handle.write(cwd)
    print(f"prepared {run_dir}\n  config  {path} (omit_io {'on' if args.omit_io else 'off'})\n"
          f"  binary  {binary}\n  model   {model or 'the app default'}\nSend the first prompt now.")
    return 0


def session_start(session_id):
    try:
        with open(os.path.join(SESSIONS, session_id, "events.jsonl")) as handle:
            first = json.loads(handle.readline())
    except (OSError, ValueError):
        return None
    return first["data"] if first.get("type") == "session.start" else None


def collect(args, config):
    run_dir = os.path.join(ROOT, "qa", "runs", args.run_id)
    try:
        started = open(os.path.join(run_dir, "started-at")).read().strip()
        cwd = open(os.path.join(run_dir, "session-cwd")).read().strip()
    except OSError:
        print(f"{run_dir} was not prepared; run prepare first.", file=sys.stderr)
        return 2
    # qa-compare.widen parses exactly this shape.
    started = datetime.datetime.fromisoformat(started).strftime("%Y-%m-%dT%H:%M:%S.000+00:00")

    session_id = args.session_id
    if not session_id:
        # The session ran in the prepared directory or, when that is a main
        # checkout, in a worktree of it, which the app names itself. Both share
        # one git directory. A runner's worktree session can be created before
        # prepare runs, so the start time only breaks a tie.
        def repo(path):
            return subprocess.run(["git", "-C", path, "rev-parse", "--path-format=absolute", "--git-common-dir"],
                                  capture_output=True, text=True).stdout.strip() if os.path.isdir(path) else ""
        ours = repo(cwd)
        found = []
        for s in os.listdir(SESSIONS):
            data = session_start(s) or {}
            where = data.get("context", {}).get("cwd") or ""
            if where == cwd or (ours and where and repo(where) == ours):
                found.append((s, data.get("startTime", "")))
        if len(found) > 1:
            after = datetime.datetime.fromisoformat(started)
            found = [f for f in found if f[1] and
                     datetime.datetime.fromisoformat(f[1].replace("Z", "+00:00")) >= after]
        found = [f[0] for f in found]
        if len(found) != 1:
            print(f"{len(found)} sessions ran in {cwd} or its worktrees since prepare; pass --session-id.", file=sys.stderr)
            return 2
        session_id = found[0]

    try:
        shutil.copy(os.path.join(SESSIONS, session_id, "events.jsonl"), run_dir)
    except OSError as err:
        print(f"no event log for session {session_id}: {err}", file=sys.stderr)
        return 2
    ended = now()
    with open(os.path.join(run_dir, "ended-at"), "w") as handle:
        handle.write(ended)
    with open(os.path.join(run_dir, "manifest.json"), "w") as handle:
        json.dump({"runtime": "copilot-app", "session_id": session_id,
                   "started_at": started, "ended_at": ended}, handle, indent=2)

    debug_log = os.path.join(run_dir, "plugin-debug.log")
    if not os.path.exists(debug_log) or os.path.getsize(debug_log) == 0:
        print("plugin-debug.log is empty: the session did not read this run's config. "
              "Was it written into the session's cwd before the first prompt?", file=sys.stderr)
        return 1

    module = compare()
    since = (datetime.datetime.fromisoformat(started) - datetime.timedelta(minutes=10)).isoformat()
    until = (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(minutes=2)).isoformat()
    spans, error = module.query_dash0(config, session_id, config["dataset"], since, until, 100)
    if error:
        print(error, file=sys.stderr)
        return 2
    with open(os.path.join(run_dir, "dash0-spans.json"), "w") as handle:
        json.dump(spans, handle, indent=2)
    names = sorted(s["name"] for s in spans)
    print(f"collected {run_dir}\n  session {session_id}\n  dash0   {len(spans)} spans: {', '.join(names)}")
    if len(spans) == 100:
        print("  the query hit its limit; the count is a floor", file=sys.stderr)
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)
    p = sub.add_parser("prepare")
    p.add_argument("run_id")
    p.add_argument("session_cwd")
    p.add_argument("--omit-io", action="store_true")
    p.add_argument("--fake-model", action="store_true")
    c = sub.add_parser("collect")
    c.add_argument("run_id")
    c.add_argument("--session-id")
    args = parser.parse_args()

    config, error = compare().load_config(ROOT)
    if error:
        print(error, file=sys.stderr)
        return 2
    if args.command == "prepare" and not config.get("ingestUrl"):
        print("qa/config.local.json has no ingestUrl.", file=sys.stderr)
        return 2
    return prepare(args, config) if args.command == "prepare" else collect(args, config)


if __name__ == "__main__":
    sys.exit(main())
