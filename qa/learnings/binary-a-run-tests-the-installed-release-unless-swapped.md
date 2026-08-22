# A QA session tests the last published release, not the working tree

`claude/claude-on-event.sh` resolves its binary at
`$CLAUDE_PLUGIN_DATA/bin/on-event-<version>-<os>-<arch>` and downloads that release from
GitHub when the file is absent. It does so quietly, so a session runs release code while
the run's own metadata records the local commit.

This is detectable but only if you look: a release build and a local `go build` of the
same source produce different digests, because the release is built by goreleaser with
different flags. Comparing a recorded digest against a fresh local build is what caught
it.

**Why it matters:** every finding gets attributed to the wrong code. A change that has
not shipped appears to have no effect, and a bug already fixed locally appears to still
exist.

**How to apply:** decide which binary is under test and record it in the run manifest
with its sha256. To test an unreleased change, build the working tree over the path the
bootstrap resolves, and restore the original afterwards including on failure — that cache
is shared with the developer's own live sessions. `QA_SWAP_BINARY=1` does this, and it is
opt-in for that reason.
