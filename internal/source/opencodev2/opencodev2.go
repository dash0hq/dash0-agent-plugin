// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package opencode consumes OpenCode V2.0.22's public durable event vocabulary.
// V1 message/part events are deliberately unsupported. Unlike hook-based hosts,
// V2 has an execution boundary: steered prompts remain inside the active turn.
package opencodev2

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
)

type Model struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
}

// Usage categories in V2 are disjoint, unlike the inclusive GenAI counters.
type Usage struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

type Event struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Created   int64  `json:"created"`
	MCPServer string `json:"mcpServer"`
	Durable   *struct {
		Seq int64 `json:"seq"`
	} `json:"durable"`
	Location struct {
		Directory string `json:"directory"`
	} `json:"location"`
	Prompt *struct {
		Text string `json:"text"`
		Role string `json:"role"`
	} `json:"prompt"`
	Data struct {
		SessionID          string         `json:"sessionID"`
		ParentID           string         `json:"parentID"`
		AssistantMessageID string         `json:"assistantMessageID"`
		ID                 string         `json:"id"`
		Name               string         `json:"name"`
		Agent              string         `json:"agent"`
		Model              Model          `json:"model"`
		Started            int64          `json:"started"`
		Input              map[string]any `json:"input"`
		Metadata           map[string]any `json:"metadata"`
		Content            []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Text   string `json:"text"`
		Tokens *Usage `json:"tokens"`
		Error  struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		Reason string `json:"reason"`
	} `json:"data"`
}

type tool struct {
	Name      string
	CallID    string
	SpanID    string
	Input     map[string]any
	Start     int64
	Model     Model
	Family    string
	MCPServer string
}

type turn struct {
	TraceID        string
	SpanID         string
	ParentSpanID   string
	ConversationID string
	Agent          string
	StepAgent      string // the step's agent, kept until the turn is known to be a sub-agent's
	Start          int64
	Directory      string
	Model          Model
	Usage          *Usage
	Prompt         string
	PromptRole     string
	Response       string
	ResponseID     string
	Skill          string
	Tools          map[string]*tool
	Models         map[string]Model
	Paused         bool // interrupted by shutdown; resumes on the next execution.started
	Exported       bool // a span of this turn was sent, so its trace is fixed
}

type session struct {
	ParentID string
	LastSeq  int64
	LastSeen int64
	Skill    string
	// A prompt delivered before its execution started, held for that turn.
	Prompt     string
	PromptRole string
	Turn       *turn
}

type anchor struct {
	TraceID        string
	SpanID         string
	ConversationID string
	Created        int64
}

const (
	stateFile = "opencode-v2.json"
	// Like Copilot's session sweep: state idle this long is abandoned.
	staleAfter = 24 * time.Hour
)

// Processor persists correlation separately from the hook pipeline. A plugin
// reload restarts the child process, but must not lose an in-flight execution.
// Handle is serial: the V2 subscription preserves each session's event order.
type Processor struct {
	Sessions map[string]*session
	Anchors  map[string]anchor
	path     string
	Cfg      otlp.Config `json:"-"`
}

func Load(dataDir string, cfg otlp.Config) (*Processor, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	p := &Processor{Sessions: map[string]*session{}, Anchors: map[string]anchor{}, path: filepath.Join(dataDir, stateFile), Cfg: cfg}
	data, err := os.ReadFile(p.path)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "opencode-v2-on-event: discarding unreadable V2 correlation state: %v\n", err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, p); err != nil {
			fmt.Fprintf(os.Stderr, "opencode-v2-on-event: discarding unreadable V2 correlation state: %v\n", err)
			p.Sessions, p.Anchors = map[string]*session{}, map[string]anchor{}
		}
	}
	// A privacy setting changed during reload also applies to buffered IO from
	// the previous plugin instance, before any more state is written or exported.
	if cfg.OmitIO && len(data) > 0 {
		for _, s := range p.Sessions {
			s.Prompt = redactIO(s.Prompt)
			if t := s.Turn; t != nil {
				t.Prompt, t.Response = redactIO(t.Prompt), redactIO(t.Response)
				for _, tool := range t.Tools {
					tool.Input = nil
				}
			}
		}
		// Overwrite the plaintext on disk now, not on the next event.
		if err := p.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "opencode-v2-on-event: saving redacted V2 correlation state: %v\n", err)
		}
	}
	return p, nil
}

func (p *Processor) Save() error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	// A sibling instance's sweep can remove an idle instance's directory.
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}

func (p *Processor) Handle(e Event) error {
	sid := e.Data.SessionID
	if !pipeline.IsSafeSessionID(sid) || e.Created <= 0 {
		return nil
	}
	s := p.Sessions[sid]
	if s == nil {
		s = &session{LastSeq: -1}
		p.Sessions[sid] = s
	}
	if e.Durable != nil {
		if e.Durable.Seq <= s.LastSeq {
			return nil
		}
		s.LastSeq = e.Durable.Seq
	}
	s.LastSeen = e.Created
	if e.Type == "session.created" {
		s.ParentID = e.Data.ParentID
		p.prune(e.Created - staleAfter.Milliseconds())
	}
	if e.Type == "session.skill.activated" {
		if s.Turn != nil {
			s.Turn.Skill = e.Data.Name
		} else {
			s.Skill = e.Data.Name
		}
	}
	if e.Type == "session.deleted" {
		delete(p.Sessions, sid)
		delete(p.Anchors, sid)
		return nil
	}
	if e.Type == "session.execution.started" {
		if s.Turn != nil && s.Turn.Paused {
			s.Turn.Paused = false
			return nil
		}
		// Any other open turn lost its terminal event; start a new one.
		traceID, err := otlp.GenerateTraceID()
		if err != nil {
			return err
		}
		spanID, err := otlp.GenerateSpanID()
		if err != nil {
			return err
		}
		t := &turn{TraceID: traceID, SpanID: spanID, ConversationID: sid, Start: e.Created, Directory: e.Location.Directory, Tools: map[string]*tool{}, Models: map[string]Model{}}
		t.Skill, s.Skill = s.Skill, ""
		t.Prompt, t.PromptRole, s.Prompt, s.PromptRole = s.Prompt, s.PromptRole, "", ""
		s.Turn = t
	}
	t := s.Turn
	if t == nil {
		if e.Type == "session.inbox.delivered" {
			p.addPrompt(&s.Prompt, &s.PromptRole, e)
		}
		return nil
	}
	// The parent's subagent progress can arrive after the child started.
	if a, ok := p.Anchors[sid]; ok && t.ParentSpanID == "" && !t.Exported {
		t.TraceID, t.ParentSpanID, t.ConversationID = a.TraceID, a.SpanID, a.ConversationID
	}
	if t.Agent == "" && (s.ParentID != "" || t.ParentSpanID != "") {
		t.Agent = t.StepAgent
	}
	// Providers can reuse a tool call ID in different assistant messages. The
	// message ID is part of both the pairing key and the exported span identity.
	key := e.Data.AssistantMessageID + ":" + e.Data.ID
	switch e.Type {
	case "session.inbox.delivered":
		p.addPrompt(&t.Prompt, &t.PromptRole, e)
	case "session.step.started":
		t.Model = e.Data.Model
		t.Models[e.Data.AssistantMessageID] = e.Data.Model
		t.StepAgent = e.Data.Agent
		if s.ParentID != "" || t.ParentSpanID != "" {
			t.Agent = e.Data.Agent
		}
	case "session.step.ended", "session.step.failed":
		if u := e.Data.Tokens; u != nil {
			if t.Usage == nil {
				t.Usage = &Usage{}
			}
			t.Usage.Input += u.Input
			t.Usage.Output += u.Output
			t.Usage.Reasoning += u.Reasoning
			t.Usage.Cache.Read += u.Cache.Read
			t.Usage.Cache.Write += u.Cache.Write
		}
	case "session.text.ended":
		if p.Cfg.OmitIO {
			t.Response = redacted
		} else {
			if t.ResponseID != e.Data.AssistantMessageID {
				t.Response, t.ResponseID = "", e.Data.AssistantMessageID
			}
			t.Response += e.Data.Text
		}
	case "session.tool.input.started":
		t.Tools[key] = &tool{Name: e.Data.Name, CallID: e.Data.ID, SpanID: otlp.SpanIDFromAgentID(sid + ":tool:" + key), Start: e.Created, Model: t.Models[e.Data.AssistantMessageID], MCPServer: e.MCPServer}
	case "session.tool.called":
		if tool := t.Tools[key]; tool != nil {
			if !p.Cfg.OmitIO {
				tool.Input = e.Data.Input
			}
			if tool.Name == "shell" {
				tool.Family = pipeline.ExtractBashCommandFamily(e.Data.Input)
			}
			tool.Start = e.Created
		}
	case "session.tool.progress":
		if tool := t.Tools[key]; tool != nil && tool.Name == "subagent" {
			if child, ok := e.Data.Metadata["sessionID"].(string); ok && pipeline.IsSafeSessionID(child) {
				p.Anchors[child] = anchor{TraceID: t.TraceID, SpanID: tool.SpanID, ConversationID: t.ConversationID, Created: e.Created}
			}
		}
	case "session.tool.success", "session.tool.failed":
		tool := t.Tools[key]
		if tool == nil {
			return nil
		}
		event := p.baseEvent(t, sid)
		event["model"] = tool.Model.ID
		event["tool_name"] = tool.Name
		event["tool_use_id"] = e.Data.ID
		event["tool_input"] = tool.Input
		if tool.Family != "" {
			event["bash_command_family"] = tool.Family
		}
		if tool.MCPServer != "" {
			event["mcp_server"] = tool.MCPServer
		}
		if name, ok := e.Data.Metadata["name"].(string); tool.Name == "skill" && ok && name != "" {
			event["skill_name"] = name
		}
		var text []string
		for _, c := range e.Data.Content {
			if c.Type == "text" {
				text = append(text, c.Text)
			}
		}
		event["tool_response"] = strings.Join(text, "\n")
		if e.Type == "session.tool.failed" {
			event["error"] = errorText(e, p.Cfg.OmitIO)
		}
		pipeline.EnrichToolEvent(event)
		cfg := p.Cfg
		cfg.Provider = tool.Model.ProviderID
		span := otlp.NewToolSpan(t.TraceID, tool.SpanID, t.SpanID, time.UnixMilli(tool.Start), time.UnixMilli(e.Created), event, e.Type == "session.tool.failed", cfg)
		delete(t.Tools, key)
		t.Exported = true
		return otlp.SendTrace(span, event, cfg)
	case "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted":
		if e.Type == "session.execution.interrupted" && e.Data.Reason == "shutdown" {
			t.Paused = true
			return nil
		}
		event := p.baseEvent(t, sid)
		event["model"] = t.Model.ID
		if t.Skill != "" {
			event["skill_name"] = t.Skill
			event["skill_source"] = "user"
		}
		if t.Prompt != "" {
			event["prompt"] = t.Prompt
			event["prompt_role"] = t.PromptRole
		}
		if t.Response != "" {
			event["last_assistant_message"] = t.Response
		}
		if u := t.Usage; u != nil {
			event["gen_ai.usage.input_tokens"] = u.Input + u.Cache.Read + u.Cache.Write
			event["gen_ai.usage.output_tokens"] = u.Output + u.Reasoning
			event["gen_ai.usage.cache_read.input_tokens"] = u.Cache.Read
			event["gen_ai.usage.cache_creation.input_tokens"] = u.Cache.Write
			if u.Reasoning > 0 {
				event["gen_ai.usage.reasoning.output_tokens"] = u.Reasoning
			}
		}
		failed := e.Type != "session.execution.succeeded"
		if failed {
			event["error"] = errorText(e, p.Cfg.OmitIO)
		}
		cfg := p.Cfg
		cfg.Provider = t.Model.ProviderID
		span := otlp.NewLLMSpan(t.TraceID, t.SpanID, t.ParentSpanID, time.UnixMilli(t.Start), time.UnixMilli(e.Created), event, failed, cfg)
		s.Turn = nil
		return otlp.SendTrace(span, event, cfg)
	}
	return nil
}

// prune uses event time, not the wall clock, so it is immune to clock skew.
func (p *Processor) prune(cutoff int64) {
	for id, s := range p.Sessions {
		if s.LastSeen < cutoff {
			delete(p.Sessions, id)
		}
	}
	for id, a := range p.Anchors {
		if a.Created < cutoff {
			delete(p.Anchors, id)
		}
	}
}

// SweepInstances removes other plugin instances' state that has not been
// written for staleAfter. Only directories holding OpenCode state qualify,
// because the data directory can be shared with the other agents.
func SweepInstances(base, keep string, now time.Time) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == keep {
			continue
		}
		dir := filepath.Join(base, entry.Name())
		if info, err := os.Stat(filepath.Join(dir, stateFile)); err == nil && now.Sub(info.ModTime()) > staleAfter {
			_ = os.RemoveAll(dir)
		}
	}
}

func (p *Processor) addPrompt(prompt, role *string, e Event) {
	if e.Prompt == nil {
		return
	}
	if p.Cfg.OmitIO {
		*prompt = redacted
	} else {
		if *prompt != "" {
			*prompt += "\n"
		}
		*prompt += e.Prompt.Text
	}
	// A turn is exported as one message: user input if any part was.
	if *role != "user" {
		*role = e.Prompt.Role
	}
}

func (p *Processor) baseEvent(t *turn, sid string) map[string]any {
	event := map[string]any{"session_id": t.ConversationID, "cwd": t.Directory}
	if sid != t.ConversationID {
		event["agent_id"] = sid
	}
	// A sub-agent's tool spans name the sub-agent, as its invoke_agent span does;
	// without agent_type the exporter falls back to the configured agent name.
	if t.Agent != "" {
		event["agent_type"] = t.Agent
		event["agent_id"] = sid
	}
	return event
}

// With omit_io, prompt and response keep a placeholder instead of being
// dropped: the shared exporter emits redacted gen_ai.input/output.messages only
// for present fields, and the conversation view needs them to separate turns.
const redacted = "<REDACTED>"

func redactIO(s string) string {
	if s == "" {
		return ""
	}
	return redacted
}

func errorText(e Event, omitIO bool) string {
	// V2 validation and MCP errors can contain entire arguments/results. The
	// shared exporter also puts this string in status.message, outside its IO
	// redaction path, so redact here before constructing the span.
	if !omitIO && e.Data.Error.Message != "" {
		return e.Data.Error.Message
	}
	if e.Data.Error.Type != "" {
		return e.Data.Error.Type
	}
	if e.Data.Reason != "" {
		return "execution interrupted: " + e.Data.Reason
	}
	return "execution failed"
}
