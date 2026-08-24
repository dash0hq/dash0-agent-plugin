// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Replays the recorded OpenCode session that the Go normalizer is also tested
// against, so both sides of the contract are pinned by the same fixture.

import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"
import type { Forwarded } from "../src/translate.ts"
import { Translator } from "../src/translate.ts"

export const FIXTURE = fileURLToPath(
  new URL("../../internal/source/opencode/testdata/captured_events.jsonl", import.meta.url),
)

/** The MCP server key the capture harness configured. */
export const CAPTURE_MCP_SERVERS = ["capture"]

export interface CapturedLine {
  kind: string
  name: string
  payload: Record<string, unknown>
}

export function readCapture(): CapturedLine[] {
  return readFileSync(FIXTURE, "utf8")
    .split("\n")
    .filter((line) => line !== "")
    .map((line) => JSON.parse(line) as CapturedLine)
}

/** feed drives one captured line into the translator the way the hooks do. */
export function feed(translator: Translator, line: CapturedLine): Forwarded | null {
  if (line.kind === "event") return translator.event(line.payload)
  if (line.kind === "hook" && line.name === "chat.message") {
    return translator.chatMessage(line.payload.input, line.payload.output)
  }
  return null
}

export function replay(lines: CapturedLine[] = readCapture()): { translator: Translator; forwarded: Forwarded[] } {
  const translator = new Translator("/tmp/project")
  translator.setMcpServers(CAPTURE_MCP_SERVERS)
  const forwarded: Forwarded[] = []
  for (const line of lines) {
    const result = feed(translator, line)
    if (result) forwarded.push(result)
  }
  return { translator, forwarded }
}
