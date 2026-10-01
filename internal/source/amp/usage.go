// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package amp adapts Amp's plugin lifecycle and optional thread export.
package amp

import (
	"encoding/json"
	"strconv"
)

// Usage is one exported assistant message, never a turn or billing total.
type Usage struct {
	Model      string
	Attributes map[string]any
}

type exportMessage struct {
	Role       string          `json:"role"`
	MessageID  json.RawMessage `json:"messageId"`
	ProtocolID string          `json:"protocolMessageID"`
	State      struct {
		Type string `json:"type"`
	} `json:"state"`
	Usage json.RawMessage `json:"usage"`
}

// messageKey preserves the distinction between legacy numeric IDs and actor IDs.
func messageKey(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" && len(s) <= 256 {
		return "s:" + s
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil && n >= 0 && n <= 9007199254740991 && string(raw) != "null" {
		return "n:" + strconv.FormatInt(n, 10)
	}
	return ""
}

// ReadUsage accepts the observed export shape. The export command is public,
// but its schema is not stable. v is a changing thread revision, not a schema
// version. Select by hook-provided IDs, never by recency.
func ReadUsage(data []byte, threadID string, ids []json.RawMessage) ([]Usage, string) {
	var doc struct {
		ID       string          `json:"id"`
		Messages []exportMessage `json:"messages"`
	}
	if json.Unmarshal(data, &doc) != nil || doc.ID != threadID || doc.Messages == nil {
		return nil, "unsupported"
	}
	selected := make(map[string]bool)
	for _, id := range ids {
		key := messageKey(id)
		if key == "" {
			return nil, "invalid"
		}
		selected[key] = true
	}
	matches := make(map[string][]int)
	for i, m := range doc.Messages {
		keys := map[string]bool{messageKey(m.MessageID): true}
		if m.ProtocolID != "" {
			keys["s:"+m.ProtocolID] = true
		}
		for key := range keys {
			if selected[key] {
				matches[key] = append(matches[key], i)
			}
		}
	}
	status := "matched"
	var calls []Usage
	seen := make(map[int]bool)
	for _, id := range ids {
		key := messageKey(id)
		mm := matches[key]
		if len(mm) != 1 {
			status = "partial"
			continue
		}
		if seen[mm[0]] {
			continue
		}
		seen[mm[0]] = true
		m := doc.Messages[mm[0]]
		if m.Role != "assistant" || m.State.Type != "complete" {
			status = "partial"
			continue
		}
		u, ok := parseUsage(m.Usage)
		if !ok {
			status = "partial"
			continue
		}
		calls = append(calls, u)
	}
	if len(calls) == 0 && status == "matched" {
		// No assistant message to look up, so nothing was matched.
		return nil, "unavailable"
	}
	return calls, status
}

func parseUsage(raw json.RawMessage) (Usage, bool) {
	var fields map[string]json.RawMessage
	var u Usage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || json.Unmarshal(fields["model"], &u.Model) != nil || u.Model == "" || len(u.Model) > 256 {
		return u, false
	}
	u.Attributes = make(map[string]any)
	keys := map[string]string{
		"totalInputTokens":         "gen_ai.usage.input_tokens",
		"outputTokens":             "gen_ai.usage.output_tokens",
		"cacheReadInputTokens":     "gen_ai.usage.cache_read.input_tokens",
		"cacheCreationInputTokens": "gen_ai.usage.cache_creation.input_tokens",
		"inputTokens":              "",
	}
	counts := make(map[string]int64)
	for key, attr := range keys {
		v, present := fields[key]
		if !present || string(v) == "null" {
			continue
		}
		var count int64
		if json.Unmarshal(v, &count) != nil || count < 0 || count > 9007199254740991 {
			return Usage{}, false
		}
		counts[key] = count
		if attr != "" {
			u.Attributes[attr] = count
		}
	}
	if total, ok := counts["totalInputTokens"]; ok {
		var sum int64
		complete := true
		for _, key := range []string{"inputTokens", "cacheReadInputTokens", "cacheCreationInputTokens"} {
			n, exists := counts[key]
			complete = complete && exists
			sum += n
		}
		if sum > total || (complete && sum != total) {
			return Usage{}, false
		}
	}
	return u, len(u.Attributes) > 0
}
