// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package pipeline is the source-agnostic engine that turns normalized hook
// events into OTLP spans. Both the Claude Code and Cursor entrypoints feed
// already-normalized events into Process; this package owns trace context
// lifecycle, span emission, and per-session scratch state.
package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/filelog"
	"github.com/dash0hq/dash0-agent-plugin/internal/harness"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/sessionurl"
	"github.com/dash0hq/dash0-agent-plugin/internal/source/claude"
	"github.com/dash0hq/dash0-agent-plugin/internal/transcript"
	"github.com/dash0hq/dash0-agent-plugin/internal/version"
)

// Result is the structured output of Process. Source-specific entrypoints render
// Messages via their own hook output contract (Claude Code emits JSON on stdout
// with systemMessage/additionalContext; Cursor uses a different contract).
type Result struct {
	Messages []Message
}

// Message carries text destined for both user-facing display and (optionally)
// model context injection.
type Message struct {
	UserText     string
	ModelContext string
}

// ReadEvent decodes one hook event from r, which every entrypoint feeds from
// os.Stdin. Each coding agent delivers its payload as a single JSON object on
// stdin, so the read is source-agnostic; per-agent differences start at
// normalization, not here.
//
// It takes an io.Reader rather than reading os.Stdin directly so callers and
// tests can supply their own input.
func ReadEvent(r io.Reader) (map[string]any, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading stdin: %w", err)
	}
	// Drop a UTF-8 byte-order mark. Windows tooling adds one readily — a
	// PowerShell pipeline into a native command writes whatever encoding the
	// console carries, and Encoding.UTF8 emits a BOM — and the JSON decoder
	// rejects it as `invalid character 'ï'`. internal/config skips one for the
	// same reason.
	raw = bytes.TrimPrefix(raw, []byte("\ufeff"))

	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return nil, fmt.Errorf("parsing JSON from stdin: %w", err)
	}
	if event == nil {
		return nil, fmt.Errorf("hook event payload is JSON null, not an object")
	}
	return event, nil
}

// ChdirToEventCwd switches to the working directory named in a hook payload, so
// repository detection and relative config lookups resolve against the user's
// project and not wherever the agent started the binary. A missing or unusable
// cwd is ignored: the chdir is an improvement, not a requirement.
//
// It belongs beside ReadEvent: both prepare one event before Process consumes it,
// and neither depends on which agent sent it.
func ChdirToEventCwd(event map[string]any) {
	cwd, ok := event["cwd"].(string)
	if !ok || cwd == "" {
		return
	}
	_ = os.Chdir(cwd)
}

// Process consumes a single normalized hook event and produces side effects
// (filelog write, trace context update, OTLP export) plus a Result with any
// messages the source should render. dataDir is the per-source scratch root
// (e.g. CLAUDE_PLUGIN_DATA); session-scoped state lives at dataDir/<session_id>.
//
// Process never returns an error caused by telemetry export — those are logged
// to stderr and swallowed so the agent loop never breaks. It only returns an
// error for fatal local issues (missing dataDir, filesystem failures).
func Process(event map[string]any, cfg otlp.Config, dataDir string, now time.Time) (Result, error) {
	var res Result

	event["timestamp"] = now.Format(time.RFC3339Nano)

	hookEvent, _ := event["hook_event_name"].(string)
	agentID, _ := event["agent_id"].(string)

	sessionID, _ := event["session_id"].(string)
	if !sessionIDPattern.MatchString(sessionID) {
		// sessionID names the per-session directory under dataDir, so anything
		// that is not filename-safe (path separators, dots) must not reach
		// filepath.Join — fall back to a random ID just like a missing one.
		warning := "session_id was missing from hook payload"
		if sessionID != "" {
			warning = "session_id from hook payload was not a safe path segment"
		}
		fmt.Fprintf(os.Stderr, "on-event: session_id missing or invalid in %s event, using random ID\n", hookEvent)
		randID, err := otlp.GenerateTraceID()
		if err != nil {
			return res, err
		}
		event["session_id"] = randID[:16]
		sessionID = event["session_id"].(string)
		event["dash0.warning"] = warning
	}

	sessionDir := filepath.Join(dataDir, sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return res, fmt.Errorf("creating session directory: %w", err)
	}

	startedFile := filepath.Join(sessionDir, "started")
	sessionAlreadyStarted := false
	if hookEvent == "SessionStart" {
		if _, err := os.Stat(startedFile); err == nil {
			sessionAlreadyStarted = true
		} else {
			// Merge, don't overwrite. Most runtimes deliver SessionStart first, so
			// there is no context yet and this builds a fresh one. But an agent may
			// deliver UserPromptSubmit before SessionStart (e.g. Copilot's
			// nondeterministic startup ordering), which has already established this
			// turn's TraceID/SpanID — preserve them rather than blanking the context.
			ctx, _ := otlp.LoadTraceContext(sessionDir)
			if ctx == nil {
				ctx = &otlp.TraceContext{}
			}
			ctx.SessionID = sessionID
			if model, _ := event["model"].(string); model != "" {
				ctx.Model = model
			}
			if err := otlp.SaveTraceContext(*ctx, sessionDir); err != nil {
				return res, err
			}
			_ = os.WriteFile(startedFile, nil, 0o644)
		}
	}

	if hookEvent == "UserPromptSubmit" && agentID == "" {
		traceID, err := otlp.GenerateTraceID()
		if err != nil {
			return res, err
		}
		chatSpanID, err := otlp.GenerateSpanID()
		if err != nil {
			return res, err
		}
		event["chat_span_id"] = chatSpanID

		model := ""
		previousTraceID := ""
		if ctx, err := otlp.LoadTraceContext(sessionDir); err == nil && ctx != nil {
			model = ctx.Model
			previousTraceID = ctx.TraceID
		}

		if err := otlp.SaveTraceContext(otlp.TraceContext{
			TraceID:   traceID,
			SpanID:    chatSpanID,
			SessionID: sessionID,
			Model:     model,
		}, sessionDir); err != nil {
			return res, err
		}
		if previousTraceID != "" {
			otlp.ClearToolModels(sessionDir, previousTraceID)
		}
	}

	if err := filelog.WriteEvent(event, sessionDir); err != nil {
		return res, err
	}

	if hookEvent == "SessionStart" && !sessionAlreadyStarted {
		switch cfg.OTLPUrl {
		case "":
			res.Messages = append(res.Messages, Message{
				UserText: "dash0: telemetry is not active — configure the plugin to start sending data.",
			})
		default:
			if err := otlp.CheckConnectivity(cfg); err != nil {
				res.Messages = append(res.Messages, Message{
					UserText: fmt.Sprintf("dash0: connectivity check failed — %v", err),
				})
			} else {
				text := fmt.Sprintf("dash0: connected (v%s)", version.Version)
				if link := sessionurl.SessionURL(cfg.OTLPUrl, sessionID, cfg.Dataset); link != "" {
					text += " → " + link
				}
				res.Messages = append(res.Messages, Message{
					UserText: text,
				})
				if msg, ok := setupNudge(cfg); ok {
					res.Messages = append(res.Messages, msg)
				}
			}
		}
	}

	switch hookEvent {
	case "PostToolUse", "PostToolUseFailure":
		if err := sendToolTrace(event, cfg, now, sessionDir, hookEvent == "PostToolUseFailure"); err != nil {
			fmt.Fprintf(os.Stderr, "on-event: trace export: %v\n", err)
		}
	case "Stop", "StopFailure":
		if err := sendLLMTrace(event, cfg, now, sessionDir, hookEvent == "StopFailure"); err != nil {
			fmt.Fprintf(os.Stderr, "on-event: trace export: %v\n", err)
		}
		if ctx, err := otlp.LoadTraceContext(sessionDir); err == nil && ctx != nil {
			otlp.ClearToolModels(sessionDir, ctx.TraceID)
		}
		otlp.ClearTraceContext(sessionDir)
	case "SubagentStart":
		// Snapshot the current trace context for this agent so its
		// SubagentStop still finds the spawning turn's trace even when it
		// arrives after Stop (context cleared) or after the next prompt
		// (context replaced). StartTime is recorded here so the subagent span
		// is anchored to when the agent was launched, not when it stopped.
		if agentID != "" {
			if ctx, err := otlp.LoadTraceContext(sessionDir); err == nil && ctx != nil && ctx.TraceID != "" {
				snap := *ctx
				snap.StartTime = now.Format(time.RFC3339Nano)
				if err := otlp.SaveAgentTraceContext(snap, sessionDir, agentID); err != nil {
					fmt.Fprintf(os.Stderr, "on-event: saving agent trace context: %v\n", err)
				}
			}
		}
	case "SubagentStop":
		markedConsumed := false
		if agentID != "" && agentStopEndsTheAgent(cfg.HarnessName) {
			if err := otlp.MarkAgentTraceContextConsumed(sessionDir, agentID); err != nil {
				fmt.Fprintf(os.Stderr, "on-event: marking agent trace context consumed: %v\n", err)
			} else {
				markedConsumed = true
			}
		}
		if err := sendLLMTrace(event, cfg, now, sessionDir, false); err != nil {
			fmt.Fprintf(os.Stderr, "on-event: trace export (subagent): %v\n", err)
		}
		if markedConsumed {
			otlp.ClearAgentTraceContext(sessionDir, agentID)
		} else if agentID != "" {
			// The agent lives on (Codex), so its snapshot has to move with it.
			// sendLLMTrace starts an invoke_agent span at ctx.StartTime, which
			// SubagentStart set once, so leaving it alone gave every task of a
			// reused agent the same start instant: measured on
			// qa/runs/spec-codex-agent-reuse, two spans both starting at 0.00s
			// with the second running 16.9s for a task that took 8, fully
			// overlapping the first and swallowing the idle gap between them.
			// This stop is where the next task begins.
			advanceAgentTraceContext(sessionDir, agentID, now)
		}
	case "SessionEnd":
		if ctx, err := otlp.LoadTraceContext(sessionDir); err == nil && ctx != nil && ctx.TraceID != "" {
			event["error"] = "session ended before completion"
			if err := sendLLMTrace(event, cfg, now, sessionDir, true); err != nil {
				fmt.Fprintf(os.Stderr, "on-event: trace export (session end fallback): %v\n", err)
			}
		}
	}

	if hookEvent == "SessionEnd" {
		_ = os.RemoveAll(sessionDir)
	}

	return res, nil
}

// SessionDir returns the per-session scratch directory under dataDir for the
// given session ID. Source entrypoints can use this to look up state that
// outlives a single hook invocation (e.g. for cross-event correlation).
func SessionDir(dataDir, sessionID string) string {
	return filepath.Join(dataDir, sessionID)
}

func sendToolTrace(event map[string]any, cfg otlp.Config, ts time.Time, dataDir string, failed bool) error {
	agentID, _ := event["agent_id"].(string)

	// For a sub-agent's tool call, prefer the snapshot taken at SubagentStart,
	// for the same reason sendLLMTrace does: the Task tool returns as soon as the
	// agent is launched, so the spawning turn's Stop — which clears the session
	// context — routinely lands while the sub-agent is still working. Reading
	// only the session context dropped every tool call made after that point,
	// which is most of them.
	var ctx *otlp.TraceContext
	if agentID != "" {
		consumed, err := otlp.AgentTraceContextConsumed(dataDir, agentID)
		if err != nil || consumed {
			return fmt.Errorf("no trace context available for tool span")
		}
		ctx, _ = otlp.LoadAgentTraceContext(dataDir, agentID)
	}
	if ctx == nil || ctx.TraceID == "" {
		var err error
		ctx, err = otlp.LoadTraceContext(dataDir)
		if err != nil || ctx == nil || ctx.TraceID == "" {
			return fmt.Errorf("no trace context available for tool span")
		}
	}

	traceID := ctx.TraceID
	parentSpanID := ctx.SpanID

	// An actor-scoped turn model wins over the session's startup model. Keeping
	// parent and subagent caches separate avoids mixing models between actors,
	// which read different transcripts: see modelTranscript.
	if _, hasModel := event["model"]; !hasModel {
		if model, err := otlp.LoadToolModel(dataDir, traceID, agentID); err == nil && model != "" {
			event["model"] = model
		}
	}

	if _, hasModel := event["model"]; !hasModel {
		if tp := modelTranscript(event, agentID); tp != "" {
			// Resolve once per turn, then remember it for the rest of the turn. The
			// transcript flushes asynchronously, so reading it per tool call put the
			// model on some of a turn's tool spans and not others, decided by which
			// call happened to run after the flush. Remembering the answer makes every
			// later tool span in the turn carry it, and waiting covers the first one.
			//
			// A sub-agent's tool call after the spawning turn's Stop caches nothing,
			// because the session context it would write to is gone by then. Those
			// calls each read the transcript, which is correct but not free.
			if m := waitForModel(tp); m != "" {
				event["model"] = m
				rememberModel(dataDir, traceID, agentID, m)
			}
		}
	}

	// ctx.Model is the model the session started with, so it answers for the main
	// actor only — a sub-agent gets no model rather than the parent's.
	if _, hasModel := event["model"]; !hasModel && agentID == "" && ctx.Model != "" {
		event["model"] = ctx.Model
	}

	startTime := ts
	if durationMs, ok := event["duration_ms"].(float64); ok && durationMs > 0 {
		startTime = ts.Add(-time.Duration(durationMs) * time.Millisecond)
	}

	toolName, _ := event["tool_name"].(string)

	var spanID string
	if strings.EqualFold(toolName, "Agent") {
		// The Agent tool's own span id is derived from the sub-agent it launched,
		// so the sub-agent's own spans can name it as their parent without
		// sharing any state.
		if resultAgentID := extractAgentIDFromResponse(event["tool_response"]); resultAgentID != "" {
			spanID = otlp.SpanIDFromAgentID(resultAgentID)
			event["agent_id"] = resultAgentID
		}
	}
	if spanID == "" {
		generated, err := otlp.GenerateSpanID()
		if err != nil {
			return err
		}
		spanID = generated
	}

	// A tool call made inside a sub-agent parents under that agent's span rather
	// than under the turn's chat span. This includes an Agent call made by a
	// sub-agent — a nested spawn — because a top-level Agent call carries no
	// agent_id at all: the id of the agent it launches arrives in the response,
	// and is read above. So agent_id here always names the caller, never the
	// callee, and nesting survives instead of being flattened onto the turn.
	if agentID != "" {
		parentSpanID = otlp.SpanIDFromAgentID(agentID)
	}

	EnrichToolEvent(event)

	span := otlp.NewToolSpan(traceID, spanID, parentSpanID, startTime, ts, event, failed, cfg)
	return otlp.SendTrace(span, event, cfg)
}

// advanceAgentTraceContext moves a surviving agent's snapshot start time to now,
// so the next task that agent runs is timed from this stop rather than from the
// SubagentStart that created it. Best-effort: on any failure the snapshot keeps
// its old start, which is the behaviour this replaces.
func advanceAgentTraceContext(dataDir, agentID string, now time.Time) {
	snap, err := otlp.LoadAgentTraceContext(dataDir, agentID)
	if err != nil || snap == nil {
		return
	}
	snap.StartTime = now.Format(time.RFC3339Nano)
	if err := otlp.SaveAgentTraceContext(*snap, dataDir, agentID); err != nil {
		fmt.Fprintf(os.Stderr, "on-event: advancing agent trace context: %v\n", err)
	}
}

// agentStopEndsTheAgent reports whether a SubagentStop means the agent is done
// for good, which decides whether its trace-context snapshot is consumed and
// deleted there.
//
// It is true everywhere except Codex. A Claude sub-agent stops once, and after
// that anything still arriving for it is stale: the snapshot goes, the consumed
// marker stays, and a late tool hook fails closed rather than falling back to
// whichever session turn is current and inventing a parent. That marker is what
// qa/specs/claude/session/sub-agent-tool-call-produces-a-span.md guards.
//
// Codex reuses an agent. SubagentStop marks the end of a TASK — the same
// agent_id then spawns, runs tools and stops again, with no second
// SubagentStart to re-arm on (measured on qa/runs/probe-codex-nested-anchored
// and probe-codex-two-subagents: one start, two stops, real work in between).
// Consuming there dropped every span of that later work. Keeping the snapshot
// keeps the agent's own anchor available as the parent, which is still the right
// one.
//
// Nothing prunes it, and an earlier version of this comment claimed SessionEnd
// did. Codex exposes ten hook events and SessionEnd is not among them
// (internal/source/codex/trust.go), so a Codex session's scratch directory
// outlives the session either way — the agent snapshots are a few hundred bytes
// each on top of the events log already there, not a new leak, but they are not
// bounded by anything today.
func agentStopEndsTheAgent(harnessName string) bool {
	return harnessName != harness.Codex.Name
}

// Skill invocation routes, reported as dash0.gen_ai.tool.skill.source.
//
// The two are not interchangeable. skillSourceCommand is a person deciding to
// run a skill; skillSourceModel is the model reaching for one. Counting them
// together answers "which skills are used"; keeping them apart answers "who
// chose it", and only the second route was ever observable before.
const (
	skillSourceCommand = "command"
	skillSourceModel   = "model"
)

// EnrichToolEvent applies the source-agnostic extractor rules to a tool event
// whose tool_name/tool_input/tool_response are already populated in the
// pipeline's canonical shape. It derives the semantic attributes (PR/issue/commit
// URLs, line counts, bash command family, skill name, MCP server) and normalizes
// the MCP tool name in place. Tool-name matching is case-insensitive so every
// runtime shares one rule set (Claude emits "Bash"/"Skill", Copilot "bash"/"skill").
func EnrichToolEvent(event map[string]any) {
	resp := event["tool_response"]
	if prURL := ExtractPRURL(resp); prURL != "" {
		event["pr_url"] = prURL
	}
	if issueURL := ExtractIssueURL(resp); issueURL != "" {
		event["issue_url"] = issueURL
	}
	if sha := ExtractCommitSHA(resp); sha != "" {
		event["commit_sha"] = sha
	}
	if added, removed := ExtractLinesCounts(resp); added > 0 || removed > 0 {
		event["lines_added"] = int64(added)
		event["lines_removed"] = int64(removed)
	}

	toolName, _ := event["tool_name"].(string)
	toolInput := event["tool_input"]
	if strings.EqualFold(toolName, "Bash") {
		if family := ExtractBashCommandFamily(toolInput); family != "" {
			event["bash_command_family"] = family
		}
	}
	if strings.EqualFold(toolName, "Skill") {
		if skill := ExtractSkillName(toolInput); skill != "" {
			event["skill_name"] = skill
		}
		// The name can also arrive pre-set: Copilot ships no arguments for the
		// skill tool and names the skill in a vendor attribute instead, so its
		// source fills skill_name in before this runs. Either way the route is
		// the same — the model chose this skill. The other route, a person
		// typing the slash command, is attributed on the chat span by
		// sendLLMTrace, so both carry the route and a count can separate
		// deliberate use from the model's own reaching.
		if _, has := event["skill_name"]; has {
			event["skill_source"] = skillSourceModel
		}
	}
	if server := ExtractMCPServer(toolName); server != "" {
		event["mcp_server"] = server
	}
	if normalized := NormalizeMCPToolName(toolName); normalized != toolName {
		event["tool_name"] = normalized
	}
}

func sendLLMTrace(event map[string]any, cfg otlp.Config, ts time.Time, dataDir string, failed bool) error {
	agentID, _ := event["agent_id"].(string)

	// For subagent spans, prefer the snapshot taken at SubagentStart: by the
	// time a SubagentStop arrives the session context may already be cleared
	// (Stop) or belong to the next turn (UserPromptSubmit).
	var ctx *otlp.TraceContext
	if agentID != "" {
		ctx, _ = otlp.LoadAgentTraceContext(dataDir, agentID)
	}
	if ctx == nil {
		var err error
		ctx, err = otlp.LoadTraceContext(dataDir)
		if err != nil || ctx == nil || ctx.TraceID == "" {
			return fmt.Errorf("no trace context available for LLM span")
		}
	}

	traceID := ctx.TraceID
	spanID := ctx.SpanID

	startTime := ts
	if ctx.StartTime != "" {
		// Agent snapshot carries the SubagentStart hook timestamp: use it so the
		// span is anchored to when the agent was launched, not when it stopped.
		if parsed, parseErr := time.Parse(time.RFC3339Nano, ctx.StartTime); parseErr == nil {
			startTime = parsed
		}
	} else {
		// For session-level spans, the span starts when the user submitted the
		// prompt, not when Stop fires.
		promptEvent, _ := filelog.FindEvent(dataDir, func(e map[string]any) bool {
			name, _ := e["hook_event_name"].(string)
			return name == "UserPromptSubmit"
		})
		if promptEvent != nil {
			if raw, ok := promptEvent["timestamp"].(string); ok {
				if parsed, parseErr := time.Parse(time.RFC3339Nano, raw); parseErr == nil {
					startTime = parsed
				}
			}
			if prompt, ok := promptEvent["prompt"]; ok {
				if _, hasPrompt := event["prompt"]; !hasPrompt {
					event["prompt"] = prompt
				}
			}
		}
		// A source may mark the prompt's role (e.g. an agent-injected turn that is
		// not user input); carry it to the chat span so the input message renders
		// with that role instead of the default "user".
		if role, ok := promptEvent["prompt_role"].(string); ok && role != "" {
			if _, has := event["prompt_role"]; !has {
				event["prompt_role"] = role
			}
		}
	}

	parentSpanID := ""
	if agentID != "" {
		parentSpanID = otlp.SpanIDFromAgentID(agentID)
		newSpanID, err := otlp.GenerateSpanID()
		if err != nil {
			return fmt.Errorf("generating sub-agent span ID: %w", err)
		}
		spanID = newSpanID
	}

	transcriptPath, _ := event["transcript_path"].(string)
	if agentID != "" {
		if atp, ok := event["agent_transcript_path"].(string); ok && atp != "" {
			transcriptPath = atp
		}
	}
	if transcriptPath != "" {
		// Token usage is sourced two ways across agents. Some (Codex, Cursor)
		// inject gen_ai.usage.* upstream in their normalizer, before Process runs;
		// others (Claude Code) leave usage on the transcript for us to read here.
		// Only take the transcript path when usage isn't already present — this
		// also keeps Codex/Cursor out of the Claude-format read and its wait below.
		if _, usagePresent := event["gen_ai.usage.input_tokens"]; !usagePresent {
			// The transcript (Claude Code format) is flushed asynchronously, so a
			// completed turn (failed==false) may still end at a mid-turn tool_use
			// entry when this hook fires — dropping the final, often cache-heavy,
			// API call's usage. Wait briefly for the terminal entry to land.
			if !failed {
				waitForTurnComplete(transcriptPath)
			}
			usage, err := transcript.ReadTurnUsage(transcriptPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "on-event: reading transcript: %v\n", err)
			}
			if usage != nil {
				injectTurnUsage(event, usage)
				// Alongside the usage it qualifies: billing mode exists to say what
				// a cost figure means, so on a turn that reported no tokens it would
				// annotate nothing.
				injectClaudeBilling(event, cfg)
			}
		}

		if title := transcript.ReadSessionTitle(transcriptPath); title != "" {
			event["gen_ai.conversation.name"] = title
		}

		// A skill invoked by its slash command runs no tool, so it produces no
		// execute_tool span. The invocation is recorded on the turn's chat span
		// instead, under the same key the Skill tool uses, so one query counts
		// both routes and skill_source separates them.
		if agentID == "" {
			if _, has := event["skill_name"]; !has {
				if skill := transcript.ReadTurnSkillCommand(transcriptPath); skill != "" {
					event["skill_name"] = skill
					event["skill_source"] = skillSourceCommand
				}
			}
		}

		if _, hasModel := event["model"]; !hasModel {
			if m := transcript.ReadModel(transcriptPath); m != "" {
				event["model"] = m
			}
		}
	}

	if _, hasModel := event["model"]; !hasModel && ctx.Model != "" {
		event["model"] = ctx.Model
	}

	span := otlp.NewLLMSpan(traceID, spanID, parentSpanID, startTime, ts, event, failed, cfg)
	return otlp.SendTrace(span, event, cfg)
}

// injectTurnUsage writes the turn's token counts onto the event as gen_ai.usage.*
// attributes, which the span builder emits verbatim. Callers skip it when the
// transcript yielded nothing, so the span carries no token attributes at all
// rather than a row of zeros.
func injectTurnUsage(event map[string]any, usage *transcript.Usage) {
	event["gen_ai.usage.input_tokens"] = usage.InputTokens
	event["gen_ai.usage.output_tokens"] = usage.OutputTokens
	event["gen_ai.usage.cache_creation.input_tokens"] = usage.CacheCreationInputTokens
	event["gen_ai.usage.cache_read.input_tokens"] = usage.CacheReadInputTokens
	event["dash0.gen_ai.usage.cache_creation.ephemeral_5m.input_tokens"] = usage.CacheCreation5mInputTokens
	event["dash0.gen_ai.usage.cache_creation.ephemeral_1h.input_tokens"] = usage.CacheCreation1hInputTokens
	// Emitted only when the turn did some thinking, matching Copilot's
	// emission of the same key (cmd/copilot-on-event/main.go). The two
	// runtimes share the key so one query spans both, and a key that is
	// always present in one and conditional in the other would defeat
	// that. Unlike the counts above, a zero here carries no information:
	// thinking tokens are a subset of output_tokens, so absence and zero
	// mean the same thing for every total and every cost.
	if usage.ReasoningOutputTokens > 0 {
		event["gen_ai.usage.reasoning.output_tokens"] = usage.ReasoningOutputTokens
	}
}

// injectClaudeBilling records whether a Claude Code session is billed per token,
// so the consumer knows whether the cost figure is spend or a list-price
// equivalent. See DEVELOPMENT.md for the attribute contract.
//
// Harness-guarded because this function sits in the shared LLM-span path: Codex
// derives its own billing mode from the rollout, and stamping Claude's account
// state onto a Codex span would silently overwrite it with the wrong answer.
//
// Billing is account state rather than turn state, so it does not read the
// transcript — but the caller gates it on usage being present, since the mode
// exists to qualify a cost figure.
func injectClaudeBilling(event map[string]any, cfg otlp.Config) {
	if cfg.HarnessName != "claude-code" {
		return
	}

	info := claude.ReadBilling()
	// Who meters the session is a separate dimension from whether it is metered at
	// all, so a consumer can read either attribute alone without being misled.
	if info.Provider != "" {
		event["dash0.gen_ai.billing_provider"] = info.Provider
	}
	// Stated even when "unknown": alongside a cost figure, recording that we
	// looked and could not tell differs from never having looked.
	event["dash0.gen_ai.billing_mode"] = info.BillingMode
	if info.PlanType != "" {
		event["dash0.gen_ai.plan_type"] = info.PlanType
	}
}

// sessionIDPattern restricts session IDs to filename-safe characters. Session
// IDs come from hook input and are used as directory names under dataDir, so
// path separators or dots must not reach filepath.Join. Claude Code generates
// session IDs as UUIDs; the random fallback IDs are 16 hex characters — both
// are covered by this allowlist.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// turnCompleteWaitBudget caps how long sendLLMTrace waits for the turn's final
// assistant entry to finish flushing to the transcript. The flush normally wins
// within tens of milliseconds; on timeout we read best-effort.
const turnCompleteWaitBudget = 500 * time.Millisecond

// turnCompletePollInterval is the gap between transcript readiness checks.
const turnCompletePollInterval = 50 * time.Millisecond

// modelWaitBudget caps how long sendToolTrace waits for the assistant entry
// that requested a tool to reach the transcript. A turn's first tool call can
// fire before that flush lands — measured at ~900ms behind the PreToolUse hook —
// and without the wait the model is absent from that one span and present on
// every later one. A resolved model is then cached in a trace-and-actor-keyed
// sidecar, so the wait is normally paid once per actor per turn.
const modelWaitBudget = 1 * time.Second

// modelTranscript names the transcript that says which model this actor is
// running. A sub-agent runs its own — an agent definition may pin one and the
// Agent tool takes an override — but its PostToolUse carries only
// transcript_path, which is the main session's, so its own file is derived.
// Returning "" leaves the model absent, which beats reading the wrong actor's.
func modelTranscript(event map[string]any, agentID string) string {
	sessionTranscript, _ := event["transcript_path"].(string)
	if agentID == "" {
		return sessionTranscript
	}
	// SubagentStop is the one event that carries it outright.
	if atp, _ := event["agent_transcript_path"].(string); atp != "" {
		return atp
	}
	sessionID, _ := event["session_id"].(string)
	return transcript.SubagentPath(sessionTranscript, sessionID, agentID)
}

// waitForModel returns the model named by the transcript, waiting only while an
// assistant entry could still be on its way.
//
// Two early exits keep the budget off the hot path. A transcript that already
// has an assistant entry naming no model belongs to a source that does not
// record models, so waiting would add latency to every tool call for nothing.
// And a transcript_path pointing at no file at all is not a flush in progress:
// nothing is coming, and polling it burned the entire budget on every tool call
// of such a session (measured at 1.6s per call before this check).
func waitForModel(transcriptPath string) string {
	if _, err := os.Stat(transcriptPath); err != nil {
		return ""
	}
	deadline := time.Now().Add(modelWaitBudget)
	for {
		model, hasAssistant := transcript.ReadCurrentTurnModel(transcriptPath)
		if model != "" {
			return model
		}
		if hasAssistant || time.Now().After(deadline) {
			return ""
		}
		time.Sleep(turnCompletePollInterval)
	}
}

// rememberModel caches a transcript-resolved model in a trace-and-actor-keyed
// sidecar only while that trace is the active session turn. The second context
// check closes the Stop/new-prompt race around the sidecar write without ever
// rewriting trace_context.json.
func rememberModel(dataDir, traceID, actorID, model string) {
	ctx, err := otlp.LoadTraceContext(dataDir)
	if err != nil || ctx == nil || ctx.TraceID != traceID {
		return
	}
	if err := otlp.SaveToolModel(dataDir, traceID, actorID, model); err != nil {
		fmt.Fprintf(os.Stderr, "on-event: remembering model: %v\n", err)
		return
	}
	ctx, err = otlp.LoadTraceContext(dataDir)
	if err != nil || ctx == nil || ctx.TraceID != traceID {
		otlp.ClearToolModel(dataDir, traceID, actorID)
	}
}

// waitForTurnComplete blocks until the transcript's current turn shows a
// terminal assistant entry or the budget elapses. It returns immediately once
// the turn is complete, so the common case adds no measurable latency.
func waitForTurnComplete(transcriptPath string) {
	deadline := time.Now().Add(turnCompleteWaitBudget)
	for {
		complete, err := transcript.TurnComplete(transcriptPath)
		if err != nil || complete {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(turnCompletePollInterval)
	}
}

var prURLPattern = regexp.MustCompile(`https?://[^\s"'<>\x60\])]+/(?:pull/\d+|pull-requests/\d+|-/merge_requests/\d+)`)

var issueURLPattern = regexp.MustCompile(`https?://[^\s"'<>\x60\])]+/issues/\d+`)

var commitSHAPattern = regexp.MustCompile(`^\[[\w/.-]+ ([0-9a-f]{7,40})\]`)

// ToolResponseText extracts the scannable text from a tool response.
// Bash tool responses are dicts with stdout/stderr; other responses may be
// plain strings or arbitrary dicts.
func ToolResponseText(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case map[string]any:
		var parts []string
		if stdout, ok := val["stdout"].(string); ok && stdout != "" {
			parts = append(parts, stdout)
		}
		if stderr, ok := val["stderr"].(string); ok && stderr != "" {
			parts = append(parts, stderr)
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
		b, err := json.Marshal(val)
		if err != nil {
			return ""
		}
		return string(b)
	default:
		b, err := json.Marshal(val)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// ExtractPRURL scans a tool response for a pull/merge request URL.
func ExtractPRURL(v any) string {
	return prURLPattern.FindString(ToolResponseText(v))
}

// ExtractIssueURL scans a tool response for an issue URL.
func ExtractIssueURL(v any) string {
	return issueURLPattern.FindString(ToolResponseText(v))
}

// ExtractCommitSHA scans a tool response for a git commit SHA from the
// standard git commit output format: [branch SHA] message
func ExtractCommitSHA(v any) string {
	text := ToolResponseText(v)
	for _, line := range strings.Split(text, "\n") {
		if m := commitSHAPattern.FindStringSubmatch(line); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

// ExtractLinesCounts returns the number of lines added and removed from a tool
// response that contains a structuredPatch (Edit/Write/MultiEdit tools).
func ExtractLinesCounts(v any) (added, removed int) {
	m, ok := v.(map[string]any)
	if !ok {
		return 0, 0
	}

	patches, ok := m["structuredPatch"].([]any)
	if !ok || len(patches) == 0 {
		return 0, 0
	}

	for _, p := range patches {
		patch, ok := p.(map[string]any)
		if !ok {
			continue
		}
		lines, ok := patch["lines"].([]any)
		if !ok {
			continue
		}
		for _, l := range lines {
			line, ok := l.(string)
			if !ok || len(line) == 0 {
				continue
			}
			switch line[0] {
			case '+':
				added++
			case '-':
				removed++
			}
		}
	}
	return added, removed
}

// ExtractBashCommandFamily extracts the leading binary name from a Bash tool
// input, skipping environment variable assignments (KEY=val prefixes).
// Input may be a string ("git status") or a map with a "command" field.
func ExtractBashCommandFamily(v any) string {
	var cmd string
	switch val := v.(type) {
	case string:
		cmd = val
	case map[string]any:
		cmd, _ = val["command"].(string)
	default:
		return ""
	}
	if cmd == "" {
		return ""
	}
	for _, token := range strings.Fields(cmd) {
		if strings.Contains(token, "=") && !strings.HasPrefix(token, "-") {
			continue
		}
		binary := filepath.Base(token)
		if binary == "." || binary == "/" {
			return ""
		}
		return binary
	}
	return ""
}

// ExtractSkillName parses the skill name from a Skill tool's input.
// Input may be a JSON string or an already-decoded map with a "skill" field.
func ExtractSkillName(v any) string {
	switch val := v.(type) {
	case string:
		if val == "" {
			return ""
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(val), &m); err != nil {
			return ""
		}
		name, _ := m["skill"].(string)
		return name
	case map[string]any:
		name, _ := val["skill"].(string)
		return name
	default:
		return ""
	}
}

// NormalizeMCPToolName strips the mcp__<server>__ prefix from an MCP tool name,
// returning just the tool portion (e.g. "send_message"). For non-MCP tools it
// returns the input unchanged.
func NormalizeMCPToolName(toolName string) string {
	if !strings.HasPrefix(toolName, "mcp__") {
		return toolName
	}
	parts := strings.SplitN(toolName, "__", 3)
	if len(parts) < 3 || parts[2] == "" {
		return toolName
	}
	return parts[2]
}

// ExtractMCPServer parses the server name from an MCP tool name
// (format: mcp__<server>__<tool>). Returns empty string for non-MCP tools
// and for UUIDs (which are not meaningful server names).
func ExtractMCPServer(toolName string) string {
	if !strings.HasPrefix(toolName, "mcp__") {
		return ""
	}
	parts := strings.SplitN(toolName, "__", 3)
	if len(parts) < 2 || parts[1] == "" {
		return ""
	}
	if isUUID(parts[1]) {
		return ""
	}
	return parts[1]
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool {
	return uuidPattern.MatchString(s)
}

// ExtractAgentIDFromResponse is exported for use by source-specific entrypoints
// that need to materialize an agent_id before handing the event to Process.
func ExtractAgentIDFromResponse(v any) string {
	return extractAgentIDFromResponse(v)
}

func extractAgentIDFromResponse(v any) string {
	var m map[string]any
	switch val := v.(type) {
	case string:
		if err := json.Unmarshal([]byte(val), &m); err != nil {
			return ""
		}
	case map[string]any:
		m = val
	default:
		return ""
	}
	id, _ := m["agentId"].(string)
	return id
}
