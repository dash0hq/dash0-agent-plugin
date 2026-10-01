// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Keeps `internal/source/opencode/testdata/forwarded_envelopes.jsonl` in sync
// with what the translator actually produces for the recorded session. The Go
// golden test replays that file, so the two sides of the envelope contract are
// pinned by one artifact that only the real translator can write.
//
// Regenerate with: UPDATE_FIXTURES=1 node --test 'test/*.test.ts'

import assert from "node:assert/strict"
import { readFileSync, writeFileSync } from "node:fs"
import { test } from "node:test"
import { fileURLToPath } from "node:url"
import { replay } from "./replay.ts"

const ENVELOPES = fileURLToPath(
  new URL("../../internal/source/opencode/testdata/forwarded_envelopes.jsonl", import.meta.url),
)

test("the recorded session's envelopes match the committed Go fixture", () => {
  const { translator, forwarded } = replay()
  const envelopes = [...forwarded, ...translator.shutdown()].map((f) => f.envelope)
  const rendered = envelopes.map((envelope) => JSON.stringify(envelope)).join("\n") + "\n"

  if (process.env.UPDATE_FIXTURES) {
    writeFileSync(ENVELOPES, rendered)
    return
  }

  assert.equal(
    readFileSync(ENVELOPES, "utf8"),
    rendered,
    "forwarded_envelopes.jsonl is stale — regenerate with UPDATE_FIXTURES=1 and re-bless the Go golden",
  )
})
