// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pi

import (
	"fmt"
	"strings"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// Subagent is one child that a foreground pi-subagents `subagent` call ran
// inside the Pi process, as the tool result reports it. This is the result
// shape of the pi-subagents extension, not a Pi API.
type Subagent struct {
	// Agent is the pi-subagents agent name, for example "delegate".
	Agent string `json:"agent"`
	// Model is "<provider>/<model>" as pi-subagents reports it.
	Model string `json:"model"`
	// Usage is the total of all model calls of the child.
	Usage Usage `json:"usage"`
	// Turns is the number of model calls of the child.
	Turns int64 `json:"turns"`
	// BillingMode is the parent's billing mode for the child's provider.
	BillingMode string `json:"billing_mode"`
	Failed      bool   `json:"failed"`
}

// maxSubagents bounds the children of one tool call. A parallel call runs
// several.
const maxSubagents = 64

// splitModel splits the "<provider>/<model>" of pi-subagents. A model ID can
// contain a slash itself, so only the first one separates.
func splitModel(s string) (provider, model string) {
	provider, model, ok := strings.Cut(s, "/")
	if !ok {
		return "", s
	}
	return provider, model
}

func (s Subagent) valid() bool {
	provider, model := splitModel(s.Model)
	return namePattern.MatchString(s.Agent) && s.Turns >= 0 &&
		(s.Model == "" || (namePattern.MatchString(provider) && namePattern.MatchString(model))) &&
		validBillingMode(s.BillingMode) &&
		validUsage(s.Usage)
}

// subagentSpans builds the spans of the children that one subagent tool call
// ran. Each child is an invoke_agent span under the tool's span, with one
// chat span under it that carries the child's total tokens. Dash0's
// coding-agent token and cost metrics read only chat spans, so the totals go
// there and not on the agent span, and every token is on exactly one span.
// The chat span covers the whole child: it is an aggregate of the child's
// model calls, not one timed call.
func subagentSpans(traceID, toolSpanID string, tool Tool, cfg otlp.Config, base func() map[string]any) []otlp.Span {
	var spans []otlp.Span
	for n, sub := range tool.Subagents {
		if n >= maxSubagents {
			break
		}
		// As with a bad tool, a bad child loses its own spans, not the turn.
		if !sub.valid() {
			continue
		}
		key := fmt.Sprintf("agent:%s:%s:%d", traceID, tool.ID, n)
		agentID := otlp.SpanIDFromAgentID(key)
		provider, model := splitModel(sub.Model)
		agentCfg := cfg
		if provider != "" {
			agentCfg.Provider = providerName(provider)
		}

		agent := base()
		agent["agent_type"] = sub.Agent
		if model != "" {
			agent["model"] = model
		}
		if sub.Failed {
			agent["error"] = "Pi sub-agent failed"
		}
		spans = append(spans, otlp.NewLLMSpan(traceID, agentID, toolSpanID, tool.Start, tool.End, agent, sub.Failed, agentCfg))

		if model == "" {
			continue
		}
		usage := base()
		// agent_type would make this an invoke_agent span. The configured
		// agent name names the child instead.
		usageCfg := agentCfg
		usageCfg.AgentName = sub.Agent
		callAttributes(usage, Call{Provider: provider, Model: model, BillingMode: sub.BillingMode, Usage: sub.Usage})
		usage["dash0.pi.subagent.timing"] = "aggregate"
		usage["dash0.pi.subagent.turns"] = sub.Turns
		spans = append(spans, otlp.NewLLMSpan(traceID, otlp.SpanIDFromAgentID(key+":usage"), agentID, tool.Start, tool.End, usage, false, usageCfg))
	}
	return spans
}
