// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// The filter and the envelope builder. OpenCode's bus is chatty — a streaming
// delta fires an event per token — so this module decides which events reach
// the wrapper at all, and attaches the context the Go normalizer cannot see
// from a single event: the root session, the session title, the configured MCP
// servers, and the turn's accumulated usage.
//
// Nothing here renames an OpenCode field. `internal/source/opencode` owns every
// OpenCode-to-canonical mapping so it stays unit-testable against golden spans
// like the other four runtimes; the canonical name this module computes is the
// filter's own reason to forward, not a second copy of that mapping.

/** Tokens mirrors OpenCode's `AssistantMessage.tokens`, summed across a turn. */
export interface Tokens {
  input: number
  output: number
  reasoning: number
  cache: { read: number; write: number }
}

/** AssistantSummary is one session's last completed assistant message. */
export interface AssistantSummary {
  modelID?: string
  mode?: string
  text?: string
  tokens: Tokens
}

/** Envelope is the plugin's contract with `internal/source/opencode`. */
export interface Envelope {
  kind: "event" | "hook" | "plugin"
  name: string
  payload: unknown
  cwd: string
  root_session_id: string
  session_title?: string
  mcp_servers?: string[]
  assistants: Record<string, AssistantSummary>
}

/**
 * Forwarded is one envelope plus the canonical event it will become. The
 * canonical name is what the spawn-count and replay tests assert against, and
 * is never sent to the wrapper.
 */
export interface Forwarded {
  canonical: string
  envelope: Envelope
}

interface SessionState {
  parentID?: string
  title?: string
  modelID?: string
  mode?: string
  text?: string
  tokens: Tokens
  assistantMessages: Set<string>
  countedMessages: Set<string>
}

function zeroTokens(): Tokens {
  return { input: 0, output: 0, reasoning: 0, cache: { read: 0, write: 0 } }
}

function record(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null ? (value as Record<string, unknown>) : undefined
}

function text(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined
}

function count(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0
}

export class Translator {
  #cwd: string
  #mcpServers: string[] = []
  #sessions = new Map<string, SessionState>()
  #terminalCalls = new Set<string>()

  constructor(cwd: string) {
    this.#cwd = cwd
  }

  /**
   * setMcpServers records the configured MCP server keys, which is what lets
   * the normalizer split OpenCode's flat `<server>_<tool>` name back apart.
   */
  setMcpServers(keys: string[]): void {
    this.#mcpServers = keys
  }

  /** event translates one OpenCode bus event, or returns null to drop it. */
  event(busEvent: unknown): Forwarded | null {
    const event = record(busEvent)
    const type = text(event?.type)
    if (!event || !type) return null
    const properties = record(event.properties) ?? {}

    switch (type) {
      case "session.created":
      case "session.updated": {
        const info = record(properties.info)
        const id = text(info?.id)
        if (!info || !id) return null
        const state = this.#state(id)
        state.parentID = text(info.parentID) ?? state.parentID
        state.title = text(info.title) ?? state.title
        if (type !== "session.created") return null
        return this.#forward(state.parentID ? "SubagentStart" : "SessionStart", "event", type, event, id)
      }

      case "message.updated":
        this.#accumulate(record(properties.info))
        return null

      case "message.part.updated":
        return this.#part(event, record(properties.part))

      case "session.idle": {
        const id = text(properties.sessionID)
        if (!id) return null
        const canonical = this.#isRoot(id) ? "Stop" : "SubagentStop"
        const forwarded = this.#forward(canonical, "event", type, event, id)
        this.#flush(id)
        return forwarded
      }

      case "session.error": {
        // A session-less error belongs to no conversation. Reporting it would
        // make the pipeline invent a session id and leave a scratch directory
        // that no SessionEnd ever removes.
        const id = text(properties.sessionID)
        if (!id) return null
        const canonical = this.#isRoot(id) ? "StopFailure" : "SubagentStop"
        const forwarded = this.#forward(canonical, "event", type, event, id)
        this.#flush(id)
        return forwarded
      }

      default:
        return null
    }
  }

  /**
   * chatMessage translates the `chat.message` hook, which is where the pipeline
   * allocates the turn's trace. A child session's prompt is dropped: the
   * sub-agent's work hangs off the parent turn's trace instead.
   */
  chatMessage(input: unknown, output: unknown): Forwarded | null {
    const id = text(record(input)?.sessionID) ?? text(record(record(output)?.message)?.sessionID)
    if (!id || !this.#isRoot(id)) return null
    return this.#forward("UserPromptSubmit", "hook", "chat.message", { input, output }, id)
  }

  /**
   * shutdown produces one SessionEnd per root session so the pipeline frees the
   * scratch directory it keyed by that session id.
   */
  shutdown(): Forwarded[] {
    const out: Forwarded[] = []
    for (const [id, state] of this.#sessions) {
      if (state.parentID) continue
      out.push(this.#forward("SessionEnd", "plugin", "shutdown", { sessionID: id }, id))
    }
    return out
  }

  /**
   * A tool part reaching a terminal state is the single source for both the
   * success and the failure path: `tool.execute.after` does not run when a tool
   * throws. OpenCode reports each terminal status once per call, but the dedupe
   * set keeps a repeat from spawning twice.
   */
  #part(event: Record<string, unknown>, part: Record<string, unknown> | undefined): Forwarded | null {
    if (!part) return null
    const sessionID = text(part.sessionID)
    if (!sessionID) return null

    if (part.type === "text") {
      const messageID = text(part.messageID)
      const body = text(part.text)
      const state = this.#state(sessionID)
      if (body && messageID && state.assistantMessages.has(messageID)) state.text = body
      return null
    }
    if (part.type !== "tool") return null

    const status = text(record(part.state)?.status)
    if (status !== "completed" && status !== "error") return null

    const callID = text(part.callID) ?? text(part.id)
    const seen = `${callID}:${status}`
    if (this.#terminalCalls.has(seen)) return null
    this.#terminalCalls.add(seen)

    const canonical = status === "completed" ? "PostToolUse" : "PostToolUseFailure"
    return this.#forward(canonical, "event", "message.part.updated", event, sessionID)
  }

  /**
   * A turn is several assistant messages, one per tool-use step, and only one
   * chat span may be emitted for it — `Stop` clears the trace context. So usage
   * is summed per session and flushed when that session goes idle. Summing is
   * keyed by message id because OpenCode reports the same completed message
   * more than once.
   */
  #accumulate(info: Record<string, unknown> | undefined): void {
    const sessionID = text(info?.sessionID)
    const messageID = text(info?.id)
    if (!info || !sessionID || !messageID || info.role !== "assistant") return

    const state = this.#state(sessionID)
    state.assistantMessages.add(messageID)
    state.modelID = text(info.modelID) ?? state.modelID
    state.mode = text(info.mode) ?? state.mode

    if (!record(info.time)?.completed) return
    if (state.countedMessages.has(messageID)) return
    state.countedMessages.add(messageID)

    const tokens = record(info.tokens)
    if (!tokens) return
    const cache = record(tokens.cache) ?? {}
    state.tokens.input += count(tokens.input)
    state.tokens.output += count(tokens.output)
    state.tokens.reasoning += count(tokens.reasoning)
    state.tokens.cache.read += count(cache.read)
    state.tokens.cache.write += count(cache.write)
  }

  #flush(sessionID: string): void {
    const state = this.#sessions.get(sessionID)
    if (!state) return
    state.tokens = zeroTokens()
    state.text = undefined
    state.countedMessages.clear()
  }

  #forward(canonical: string, kind: Envelope["kind"], name: string, payload: unknown, sessionID: string): Forwarded {
    const rootID = this.#root(sessionID)
    const envelope: Envelope = {
      kind,
      name,
      payload,
      cwd: this.#cwd,
      root_session_id: rootID,
      assistants: this.#assistants(),
    }
    const title = this.#sessions.get(rootID)?.title
    if (title) envelope.session_title = title
    if (this.#mcpServers.length > 0) envelope.mcp_servers = this.#mcpServers
    return { canonical, envelope }
  }

  #assistants(): Record<string, AssistantSummary> {
    const out: Record<string, AssistantSummary> = {}
    for (const [id, state] of this.#sessions) {
      if (!state.modelID && !state.mode && !state.text) continue
      const summary: AssistantSummary = { tokens: { ...state.tokens, cache: { ...state.tokens.cache } } }
      if (state.modelID) summary.modelID = state.modelID
      if (state.mode) summary.mode = state.mode
      if (state.text) summary.text = state.text
      out[id] = summary
    }
    return out
  }

  /**
   * The pipeline keys all scratch state by one session id per conversation and
   * expresses sub-agent structure through agent_id instead, so every event
   * collapses onto the root of its parentID chain.
   *
   * The chain is resolved from what `session.created` already reported rather
   * than from the SDK: a child session is always created inside the run that
   * observes it, and a resumed root session is its own root anyway.
   */
  #root(sessionID: string): string {
    const seen = new Set<string>()
    let id = sessionID
    for (;;) {
      const parentID = this.#sessions.get(id)?.parentID
      if (!parentID || seen.has(parentID)) return id
      seen.add(parentID)
      id = parentID
    }
  }

  #isRoot(sessionID: string): boolean {
    return this.#root(sessionID) === sessionID
  }

  #state(sessionID: string): SessionState {
    let state = this.#sessions.get(sessionID)
    if (!state) {
      state = { tokens: zeroTokens(), assistantMessages: new Set(), countedMessages: new Set() }
      this.#sessions.set(sessionID, state)
    }
    return state
  }
}
