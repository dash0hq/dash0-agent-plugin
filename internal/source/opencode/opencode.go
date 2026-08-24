// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package opencode normalizes OpenCode events into the pipeline's canonical
// event vocabulary.
//
// Unlike the other runtimes, OpenCode has no hook mechanism that can spawn a
// process, so an in-process TypeScript plugin forwards events here over stdin.
// The plugin filters the event bus and wraps each forwarded event in an
// envelope, but does not rename anything — every OpenCode-to-canonical mapping
// lives in this package so it is unit-testable against golden spans like the
// other runtimes.
//
// The envelope is the plugin's contract with this package:
//
//	{
//	  "kind":            "event" | "hook" | "plugin",
//	  "name":            OpenCode's own event or hook name,
//	  "payload":         that event's or hook's unmodified body,
//	  "cwd":             the project directory,
//	  "root_session_id": the root of this session's parentID chain,
//	  "session_title":   the root session's title, when OpenCode has set one,
//	  "mcp_servers":     the configured MCP server keys,
//	  "assistants":      {"<session id>": {"modelID", "mode", "text", "tokens"}}
//	}
//
// Everything under "payload" is OpenCode's shape; everything beside it is
// context the plugin can only resolve while OpenCode is running.
//
// "assistants" is keyed per session, not per conversation: a delegated sub-agent
// runs in its own OpenCode session with its own model, response text and token
// counts, and folding those into the root session's entry would report the
// sub-agent's usage twice. Each entry is that session's last completed assistant
// message, with tokens summed across the turn's steps. The plugin must supply an
// entry for the session an event concerns whenever that session has produced a
// completed assistant message — a child session's entry is where agent_type
// comes from at SubagentStop, and without it the sub-agent's span is emitted as
// a plain chat span rather than an invoke_agent span.
package opencode

import (
	"encoding/json"
	"strings"
)

// Normalize transforms one envelope from the OpenCode plugin into a canonical
// pipeline event, or returns nil when the event maps to no span.
func Normalize(envelope map[string]any) map[string]any {
	kind, _ := envelope["kind"].(string)
	name, _ := envelope["name"].(string)

	if kind == "plugin" && name == "shutdown" {
		return sessionEnd(envelope)
	}

	payload, _ := envelope["payload"].(map[string]any)
	if payload == nil {
		return nil
	}

	switch kind + " " + name {
	case "event session.created":
		return sessionCreated(envelope, payload)
	case "hook chat.message":
		return chatMessage(envelope, payload)
	case "event message.part.updated":
		return toolPart(envelope, payload)
	case "event session.idle":
		return sessionIdle(envelope, payload)
	case "event session.error":
		return sessionError(envelope, payload)
	}
	return nil
}

// sessionCreated maps a root session to SessionStart and a child session — one
// OpenCode spawned for a delegated sub-task — to SubagentStart, whose agent_id
// is the child session id.
func sessionCreated(envelope, payload map[string]any) map[string]any {
	info := digMap(payload, "properties", "info")
	id := str(info["id"])
	if id == "" {
		return nil
	}

	parentID := str(info["parentID"])
	if parentID == "" {
		event := newEvent(envelope, "SessionStart", id)
		if _, has := event["cwd"]; !has {
			setNonEmpty(event, "cwd", str(info["directory"]))
		}
		setNonEmpty(event, "model", str(assistantOf(envelope, id)["modelID"]))
		return event
	}

	event := newEvent(envelope, "SubagentStart", rootSessionID(envelope, parentID))
	event["agent_id"] = id
	setNonEmpty(event, "agent_type", agentType(envelope, id, info))
	return event
}

// chatMessage maps the root session's prompt to UserPromptSubmit, which is
// where the pipeline allocates the turn's trace. A child session's prompt is
// dropped: the sub-agent's span hangs off the parent turn's trace instead.
func chatMessage(envelope, payload map[string]any) map[string]any {
	input := digMap(payload, "input")
	message := digMap(payload, "output", "message")

	sessionID := firstNonEmpty(str(input["sessionID"]), str(message["sessionID"]))
	if sessionID == "" || sessionID != rootSessionID(envelope, sessionID) {
		return nil
	}

	event := newEvent(envelope, "UserPromptSubmit", sessionID)
	setNonEmpty(event, "prompt", promptText(payload))
	setNonEmpty(event, "model", firstNonEmpty(
		str(digMap(input, "model")["modelID"]),
		str(digMap(message, "model")["modelID"]),
		str(assistantOf(envelope, sessionID)["modelID"]),
	))
	return event
}

// toolPart maps a tool part that has reached a terminal state to PostToolUse or
// PostToolUseFailure. Non-terminal statuses are dropped, which is also what
// makes the mapping idempotent: OpenCode repeats `running` for a call but
// reports each terminal status exactly once.
//
// OpenCode emits no PreToolUse equivalent, and needs none — it reports the
// tool's own start and end times, so the span's duration is exact.
func toolPart(envelope, payload map[string]any) map[string]any {
	part := digMap(payload, "properties", "part")
	if str(part["type"]) != "tool" {
		return nil
	}

	state := digMap(part, "state")
	var hookEvent string
	switch str(state["status"]) {
	case "completed":
		hookEvent = "PostToolUse"
	case "error":
		hookEvent = "PostToolUseFailure"
	default:
		return nil
	}

	sessionID := str(part["sessionID"])
	rootID := rootSessionID(envelope, sessionID)
	event := newEvent(envelope, hookEvent, rootID)
	if sessionID != "" && sessionID != rootID {
		// A tool run inside a child session parents to that session's
		// invoke_agent span rather than to the turn's chat span.
		event["agent_id"] = sessionID
	}

	setNonEmpty(event, "tool_use_id", str(part["callID"]))
	if input, ok := state["input"]; ok {
		event["tool_input"] = input
	}
	if hookEvent == "PostToolUseFailure" {
		setNonEmpty(event, "error", str(state["error"]))
	} else if output, ok := state["output"]; ok {
		event["tool_response"] = output
	}
	if d := duration(state); d > 0 {
		event["duration_ms"] = d
	}

	toolName := str(part["tool"])
	if childID := str(digMap(state, "metadata")["sessionId"]); childID != "" {
		// A delegation. The pipeline only allocates the sub-agent's parent span
		// for a tool named "Agent" whose response carries agentId, so present
		// OpenCode's delegation tool in that shape — otherwise the child's own
		// spans parent to a span id that was never emitted.
		toolName = "Agent"
		event["tool_response"] = map[string]any{"agentId": childID, "output": state["output"]}
	} else {
		toolName = rewriteMCPToolName(envelope, toolName)
	}
	setNonEmpty(event, "tool_name", toolName)

	return event
}

// sessionIdle closes a turn: the root session's idle emits the chat span, a
// child session's emits that sub-agent's span.
func sessionIdle(envelope, payload map[string]any) map[string]any {
	sessionID := str(digMap(payload, "properties")["sessionID"])
	if sessionID == "" {
		return nil
	}

	rootID := rootSessionID(envelope, sessionID)
	var event map[string]any
	if sessionID == rootID {
		event = newEvent(envelope, "Stop", rootID)
	} else {
		event = newEvent(envelope, "SubagentStop", rootID)
		event["agent_id"] = sessionID
		setNonEmpty(event, "agent_type", agentType(envelope, sessionID, nil))
	}

	attachAssistant(event, envelope, sessionID)
	return event
}

// sessionError maps a reported error to StopFailure for the root session and to
// SubagentStop for a child one. A child's failure must not become StopFailure:
// the pipeline clears the turn's trace context for every Stop/StopFailure
// regardless of agent_id, so a failing sub-agent would take the rest of the
// parent turn's spans with it.
//
// OpenCode declares session.error's sessionID as optional, and an error with no
// session belongs to no conversation: reporting it anyway would make the
// pipeline invent a session id, export a chat span into a conversation that does
// not exist, and leave a scratch directory that no SessionEnd ever removes.
func sessionError(envelope, payload map[string]any) map[string]any {
	properties := digMap(payload, "properties")
	sessionID := str(properties["sessionID"])
	rootID := rootSessionID(envelope, sessionID)
	if rootID == "" {
		return nil
	}

	var event map[string]any
	if sessionID == "" || sessionID == rootID {
		event = newEvent(envelope, "StopFailure", rootID)
	} else {
		event = newEvent(envelope, "SubagentStop", rootID)
		event["agent_id"] = sessionID
		setNonEmpty(event, "agent_type", agentType(envelope, sessionID, nil))
	}
	setNonEmpty(event, "error", errorText(properties["error"]))
	attachAssistant(event, envelope, firstNonEmpty(sessionID, rootID))
	return event
}

// sessionEnd frees the session's scratch directory when the plugin shuts down.
func sessionEnd(envelope map[string]any) map[string]any {
	payload, _ := envelope["payload"].(map[string]any)
	sessionID := rootSessionID(envelope, str(payload["sessionID"]))
	if sessionID == "" {
		return nil
	}
	return newEvent(envelope, "SessionEnd", sessionID)
}

func newEvent(envelope map[string]any, hookEvent, sessionID string) map[string]any {
	event := map[string]any{
		"hook_event_name": hookEvent,
		"session_id":      sessionID,
	}
	setNonEmpty(event, "cwd", str(envelope["cwd"]))
	return event
}

// attachAssistant carries the model, response text, session title and token
// usage of one session onto an event that becomes a chat span.
func attachAssistant(event, envelope map[string]any, sessionID string) {
	assistant := assistantOf(envelope, sessionID)
	setNonEmpty(event, "model", str(assistant["modelID"]))
	setNonEmpty(event, "last_assistant_message", str(assistant["text"]))
	setNonEmpty(event, "gen_ai.conversation.name", sessionTitle(envelope))

	tokens, ok := assistant["tokens"].(map[string]any)
	if !ok {
		return
	}
	cache := digMap(tokens, "cache")
	event["gen_ai.usage.input_tokens"] = intOf(tokens["input"])
	event["gen_ai.usage.output_tokens"] = intOf(tokens["output"])
	event["gen_ai.usage.cache_read.input_tokens"] = intOf(cache["read"])
	event["gen_ai.usage.cache_creation.input_tokens"] = intOf(cache["write"])
	if reasoning := intOf(tokens["reasoning"]); reasoning > 0 {
		event["gen_ai.usage.reasoning.output_tokens"] = reasoning
	}
}

// sessionTitle returns the session's title, or "" when OpenCode has not named
// it yet. A new session is not untitled but carries the literal placeholder
// "New session - <timestamp>", which is not worth reporting as a name.
func sessionTitle(envelope map[string]any) string {
	title := str(envelope["session_title"])
	if strings.HasPrefix(title, "New session - ") {
		return ""
	}
	return title
}

// rewriteMCPToolName turns OpenCode's flat `<server>_<tool>` name for an
// MCP-provided tool into the canonical `mcp__<server>__<tool>` the shared
// extractors parse. Both halves may contain underscores, so the server is
// resolved against the configured server keys — longest first — rather than by
// splitting on the first one.
func rewriteMCPToolName(envelope map[string]any, toolName string) string {
	longest := ""
	for _, server := range strSlice(envelope["mcp_servers"]) {
		prefix := server + "_"
		if len(server) > len(longest) && len(toolName) > len(prefix) && strings.HasPrefix(toolName, prefix) {
			longest = server
		}
	}
	if longest == "" {
		return toolName
	}
	return "mcp__" + longest + "__" + toolName[len(longest)+1:]
}

// rootSessionID resolves the session whose id keys all of this conversation's
// scratch state and spans. Sub-agent structure is expressed through agent_id,
// never through a second session id.
func rootSessionID(envelope map[string]any, fallback string) string {
	if id := str(envelope["root_session_id"]); id != "" {
		return id
	}
	return fallback
}

// assistantOf returns the plugin-resolved assistant message for one session, or
// nil when the plugin reported none for it.
func assistantOf(envelope map[string]any, sessionID string) map[string]any {
	if sessionID == "" {
		return nil
	}
	return digMap(envelope, "assistants", sessionID)
}

// agentType names the sub-agent. The session's own `info.agent` wins over its
// last assistant message's mode: session.created carries the authoritative agent
// for that session, while a mode may have been resolved before the session had
// any message of its own.
func agentType(envelope map[string]any, sessionID string, info map[string]any) string {
	return firstNonEmpty(str(info["agent"]), str(assistantOf(envelope, sessionID)["mode"]))
}

func promptText(payload map[string]any) string {
	parts, _ := digMap(payload, "output")["parts"].([]any)
	var texts []string
	for _, raw := range parts {
		part, _ := raw.(map[string]any)
		if str(part["type"]) != "text" {
			continue
		}
		if text := str(part["text"]); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

func duration(state map[string]any) float64 {
	times := digMap(state, "time")
	start, end := numOf(times["start"]), numOf(times["end"])
	if start <= 0 || end <= start {
		return 0
	}
	return end - start
}

func errorText(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case map[string]any:
		if message := str(digMap(val, "data")["message"]); message != "" {
			return message
		}
		if message := str(val["message"]); message != "" {
			return message
		}
		if name := str(val["name"]); name != "" {
			return name
		}
		if encoded, err := json.Marshal(val); err == nil {
			return string(encoded)
		}
	}
	return ""
}

func digMap(m map[string]any, path ...string) map[string]any {
	for _, key := range path {
		next, ok := m[key].(map[string]any)
		if !ok {
			return nil
		}
		m = next
	}
	return m
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strSlice(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s := str(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func setNonEmpty(event map[string]any, key, value string) {
	if value != "" {
		event[key] = value
	}
}

// intOf coerces a JSON-decoded token count to int64, so the OTLP layer renders
// it as an integer attribute rather than a stringified double.
func intOf(v any) int64 {
	switch val := v.(type) {
	case int64:
		return val
	case float64:
		return int64(val)
	case json.Number:
		n, _ := val.Int64()
		return n
	}
	return 0
}

func numOf(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int64:
		return float64(val)
	case json.Number:
		n, _ := val.Float64()
		return n
	}
	return 0
}
