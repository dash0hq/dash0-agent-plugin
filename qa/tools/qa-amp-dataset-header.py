#!/usr/bin/env python3
"""Prove AMP_PLUGIN_OPTION_DATASET decides the Dash0-Dataset header.

The amp arm reads back from `ampDataset` while the machine's own ~/.amp config
names a different dataset. That only works because the harness prefix outranks
the config file, and DASH0_DATASET does not. This captures the header
against a local listener, so it needs no Dash0 credential and sends nothing
anywhere. Prints one line per case; qa-setup's
`amp-dataset-override-reaches-the-header` names the expected values.
"""

import http.server
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import threading

seen: list[str | None] = []


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self) -> None:
        seen.append(self.headers.get("Dash0-Dataset"))
        self.rfile.read(int(self.headers.get("Content-Length", 0)))
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"{}")

    def log_message(self, *args: object) -> None:
        pass


def main() -> int:
    root = pathlib.Path(__file__).resolve().parents[2]
    helper = pathlib.Path(tempfile.mkdtemp()) / "amp-on-event"
    build = subprocess.run(
        ["go", "build", "-buildvcs=false", "-o", str(helper), "./cmd/amp-on-event"],
        cwd=root,
        capture_output=True,
    )
    if build.returncode != 0:
        sys.exit(f"could not build the helper: {build.stderr.decode().strip()}")

    server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    url = f"http://127.0.0.1:{server.server_port}"

    # A throwaway HOME standing in for the machine's install, so the real
    # ~/.amp/dash0-agent-plugin.local.md is neither read nor written.
    home = tempfile.mkdtemp()
    config = pathlib.Path(home, ".amp")
    config.mkdir()
    (config / "dash0-agent-plugin.local.md").write_text(
        f'---\notlp_url: "{url}"\ndataset: "default"\nagent_name: "amp"\n---\n'
    )

    envelope = json.dumps(
        {
            "thread_id": "T-test",
            "id": 1,
            "start": "2026-09-15T10:00:00Z",
            "end": "2026-09-15T10:00:01Z",
            "status": "done",
            "executor": "local",
            "tools": [],
            "assistant_ids": [],
            "truncated": False,
        }
    ).encode()

    # An arbitrary probe value: the point is which env var reaches the header,
    # not which dataset. Deliberately not anyone's real dataset name, so this
    # tool carries no machine-local configuration.
    probe = "qa-probe-dataset"
    cases = [
        ("config file only", {}),
        (f"AMP_PLUGIN_OPTION_DATASET={probe}", {"AMP_PLUGIN_OPTION_DATASET": probe}),
        (f"DASH0_DATASET={probe}", {"DASH0_DATASET": probe}),
    ]
    for label, extra in cases:
        env = {
            "HOME": home,
            "PATH": os.environ["PATH"],
            # Never a real credential: nothing leaves the machine.
            "AMP_PLUGIN_OPTION_AUTH_TOKEN": "qa-dummy-token",
            **extra,
        }
        # Count this case's own request. `if not seen` would pass on the stale
        # entry from the previous case, and print its header as this one's —
        # exactly the way to conclude that DASH0_DATASET won when it did not.
        before = len(seen)
        subprocess.run([str(helper)], input=envelope, env=env, capture_output=True)
        if len(seen) == before:
            sys.exit(f"{label}: the helper sent nothing")
        print(f"{label}: {seen[-1]}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
