#!/usr/bin/env bash
# Amp install/uninstall contracts (runnable locally and in CI):
#   - install-amp.sh lays out the plugin dir with the two names the bridge needs
#   - --project installs into the workspace instead of the user directory
#   - uninstall-amp.sh removes only the dash0 plugin directory
#   - --project uninstall leaves the machine-wide config file in place
# Requires: go, make, bash, python3. No network: the contract installs from a local build
# via DASH0_SOURCE_DIR, which is also how a contributor installs a working tree.
# The release download path needs a published release and is not covered here.
set -euo pipefail
# Redirecting stdin alone does not hide /dev/tty. Run the unattended contracts
# in a new session; the explicit pty.fork test below creates its own terminal.
if ( : < /dev/tty ) 2>/dev/null; then
  exec python3 - "$0" "$@" <<'PY'
import subprocess
import sys
sys.exit(subprocess.call(['bash'] + sys.argv[1:], start_new_session=True))
PY
fi
# shellcheck source=test/contracts/lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

SRC=$(mktemp -d)
make -C "$REPO" build-binary PKG=./cmd/amp-on-event OUT="$SRC/amp-on-event" >/dev/null
cp "$REPO/amp/index.ts" "$SRC/index.ts"
mkdir -p "$SRC/skills/dash0-configure"
cp "$REPO/amp/skills/dash0-configure/SKILL.md" "$SRC/skills/dash0-configure/SKILL.md"

export HOME=/tmp/amp-contract-home; rm -rf "$HOME"; mkdir -p "$HOME"
export DASH0_SOURCE_DIR="$SRC"
export DASH0_OTLP_URL=http://localhost:4319
export DASH0_AUTH_TOKEN=amp-contract-token
export DASH0_DATASET=amp-contract-ds
# Not set by this contract, so a developer's own value would otherwise reach
# the installer and write a team_name the re-run case does not expect.
unset DASH0_TEAM_NAME

echo "== install-amp.sh installs index.ts + the helper under the names the bridge resolves =="
bash "$REPO/install-amp.sh" < /dev/null
PLUGIN_DIR="$HOME/.config/amp/plugins/dash0"
fail=0
# amp/index.ts spawns exactly ./amp-on-event next to itself, so the asset's
# -<os>-<arch> suffix must be gone and the file must be executable.
[ -f "$PLUGIN_DIR/index.ts" ]  || { echo "ERROR: no index.ts in $PLUGIN_DIR"; fail=1; }
[ -x "$PLUGIN_DIR/amp-on-event" ] || { echo "ERROR: no executable amp-on-event in $PLUGIN_DIR"; fail=1; }
# index.ts registers this exact relative path; Amp resolves it against the
# plugin directory, so a flat copy would register a skill that does not exist.
[ -f "$PLUGIN_DIR/skills/dash0-configure/SKILL.md" ] \
  || { echo "ERROR: no skills/dash0-configure/SKILL.md in $PLUGIN_DIR"; fail=1; }
grep -q 'auth_token: "amp-contract-token"' "$HOME/.amp/dash0-agent-plugin.local.md" \
  || { echo "ERROR: the token did not reach ~/.amp/dash0-agent-plugin.local.md"; fail=1; }
[ "$fail" -eq 0 ] || exit 1
echo "PASS: install-amp.sh laid out the plugin directory and wrote the config"

echo "== a re-run with no credentials keeps the config it would otherwise blank =="
# The config file is machine-wide and shared with every --project install, so a
# non-interactive re-run that answers nothing used to write auth_token: "" and
# stop every other project exporting. It also dropped export_usage: false, the
# one key step 8 tells the user to add.
# Unquoted is valid YAML and what a hand-edit usually produces. Reading only
# the quoted form would treat this one as absent while still stripping its
# line, which loses the value rather than preserving it.
python3 - "$HOME/.amp/dash0-agent-plugin.local.md" <<'PY'
from pathlib import Path
import sys
path = Path(sys.argv[1])
text = path.read_text()
path.write_text('\ufeff' + text.rsplit('---', 1)[0] +
                'export_usage: false\nteam_name: platform\n---\n' +
                'Notes stay outside config.\nexport_usage: true\nauth_token: not-a-token')
PY
( unset DASH0_OTLP_URL DASH0_AUTH_TOKEN DASH0_DATASET; bash "$REPO/install-amp.sh" < /dev/null ) >/dev/null
python3 - "$HOME/.amp/dash0-agent-plugin.local.md" <<'PY'
from pathlib import Path
import sys
_, front, body = Path(sys.argv[1]).read_text().split('---', 2)
assert 'auth_token: "amp-contract-token"' in front, 're-run blanked token'
assert 'export_usage: false' in front, 're-run dropped export_usage'
assert 'team_name: "platform"' in front, 're-run dropped unquoted team'
assert front.count('auth_token:') == 1, 're-run duplicated token'
assert front.count('export_usage:') == 1, 're-run promoted body settings into frontmatter'
assert 'Notes stay' not in front, 're-run promoted prose into frontmatter'
assert body == '\nNotes stay outside config.\nexport_usage: true\nauth_token: not-a-token', \
    're-run promoted or changed the Markdown body'
PY
echo "PASS: the re-run preserved the token and the user's own keys"

echo "== interactive re-runs can rotate the token, keep defaults, and clear a team =="
python3 - "$REPO/install-amp.sh" "$HOME/.amp/dash0-agent-plugin.local.md" <<'PY'
import errno
import os
from pathlib import Path
import pty
import select
import sys
import time

installer, config = sys.argv[1:]
for token, team in (('rotated-contract-token', ''), ('', '-')):
    pid, fd = pty.fork()
    if pid == 0:
        for key in ('DASH0_OTLP_URL', 'DASH0_AUTH_TOKEN', 'DASH0_DATASET', 'DASH0_TEAM_NAME'):
            os.environ.pop(key, None)
        os.execvp('bash', ['bash', installer])
    pending = b''
    transcript = b''
    answers = iter([b'\n', (token + '\n').encode(), b'\n', (team + '\n').encode()])
    deadline = time.monotonic() + 20
    try:
        while time.monotonic() < deadline:
            if not select.select([fd], [], [], 1)[0]:
                continue
            try:
                data = os.read(fd, 4096)
            except OSError as error:
                if error.errno == errno.EIO:
                    break
                raise
            if not data:
                break
            transcript += data
            pending += data
            if pending.endswith(b': '):
                os.write(fd, next(answers))
                pending = b''
        else:
            os.kill(pid, 9)
            raise AssertionError('installer did not finish its prompts')
    finally:
        os.close(fd)
        _, status = os.waitpid(pid, 0)
    assert status == 0, 'interactive installer failed'
    assert next(answers, None) is None, 'existing values skipped interactive prompts'
    assert b'rotated-contract-token' not in transcript, 'secret echoed in terminal'
    front = Path(config).read_text().split('---', 2)[1]
    assert 'auth_token: "rotated-contract-token"' in front, 'token not rotated or kept'
    assert 'otlp_url: "http://localhost:4319"' in front, 'Enter blanked endpoint'
    assert 'dataset: "amp-contract-ds"' in front, 'Enter blanked dataset'
    if team == '-':
        assert 'team_name:' not in front, 'team not cleared'
    else:
        assert 'team_name: "platform"' in front, 'Enter blanked team'
    assert 'export_usage: false' in front, 'interactive edit dropped other settings'
PY
echo "PASS: interactive re-run rotates or keeps a hidden token and clears a team"

echo "== --project installs into the workspace instead of the user directory =="
WORKSPACE=$(mktemp -d)
( cd "$WORKSPACE" && bash "$REPO/install-amp.sh" --project < /dev/null )
[ -x "$WORKSPACE/.amp/plugins/dash0/amp-on-event" ] \
  || { echo "ERROR: --project did not install into $WORKSPACE/.amp/plugins/dash0"; exit 1; }
echo "PASS: --project installed into the workspace"

echo "== --project uninstall leaves the machine-wide config alone =="
# Before the user-level uninstall, which removes that config itself.
( cd "$WORKSPACE" && bash "$REPO/uninstall-amp.sh" --project --yes )
[ ! -e "$WORKSPACE/.amp/plugins/dash0" ] || { echo "ERROR: --project uninstall left the plugin behind"; exit 1; }
[ -f "$HOME/.amp/dash0-agent-plugin.local.md" ] \
  || { echo "ERROR: --project uninstall removed the machine-wide config file"; exit 1; }
[ -x "$PLUGIN_DIR/amp-on-event" ] || { echo "ERROR: --project uninstall removed the user-level plugin"; exit 1; }
echo "PASS: --project uninstall stayed inside the workspace"

echo "== uninstall-amp.sh removes only the dash0 plugin directory =="
mkdir -p "$HOME/.config/amp/plugins/other"; echo keep > "$HOME/.config/amp/plugins/other/index.ts"
bash "$REPO/uninstall-amp.sh" --yes
[ ! -e "$PLUGIN_DIR" ] || { echo "ERROR: $PLUGIN_DIR survived the uninstall"; exit 1; }
[ ! -e "$HOME/.amp/dash0-agent-plugin.local.md" ] || { echo "ERROR: the config file survived the uninstall"; exit 1; }
[ -f "$HOME/.config/amp/plugins/other/index.ts" ] || { echo "ERROR: uninstall removed another plugin"; exit 1; }
echo "PASS: uninstall-amp.sh removed the Dash0 plugin and left the other one alone"
