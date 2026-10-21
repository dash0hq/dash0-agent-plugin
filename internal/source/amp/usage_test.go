// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package amp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsageKeepsModelsSeparateAndSelectsExactIDs(t *testing.T) {
	data := []byte(`{"v":60,"id":"T-test","messages":[
		{"role":"assistant","messageId":1,"protocolMessageID":"M-old","state":{"type":"complete"},"usage":{"model":"wrong","totalInputTokens":999}},
		{"role":"assistant","messageId":2,"protocolMessageID":"M-a","state":{"type":"complete"},"usage":{"model":"gpt-6-astra","inputTokens":0,"totalInputTokens":42657,"cacheReadInputTokens":42368,"cacheCreationInputTokens":289,"outputTokens":20}},
		{"role":"assistant","messageId":3,"protocolMessageID":"M-b","state":{"type":"complete"},"usage":{"model":"claude-sonnet-4-6","inputTokens":7,"totalInputTokens":31,"cacheReadInputTokens":13,"cacheCreationInputTokens":11,"outputTokens":17}}
	]}`)
	calls, status := ReadUsage(data, "T-test", []json.RawMessage{json.RawMessage(`"M-a"`), json.RawMessage(`"M-b"`)})
	require.Equal(t, "matched", status)
	require.Len(t, calls, 2)
	require.Equal(t, "gpt-6-astra", calls[0].Model)
	require.Equal(t, int64(42657), calls[0].Attributes["gen_ai.usage.input_tokens"])
	require.Equal(t, int64(20), calls[0].Attributes["gen_ai.usage.output_tokens"])
	require.Equal(t, "claude-sonnet-4-6", calls[1].Model)
	require.Equal(t, int64(31), calls[1].Attributes["gen_ai.usage.input_tokens"])
	require.Equal(t, int64(17), calls[1].Attributes["gen_ai.usage.output_tokens"])
}

func TestExportRevisionIsNotASchemaVersion(t *testing.T) {
	// Repeated live exports of the same Orb changed v from 60 to 211 as
	// messages arrived. Rejecting everything except v60 disables real usage.
	data := []byte(`{"v":211,"id":"T-test","messages":[{"role":"assistant","messageId":2,"protocolMessageID":"M-a","state":{"type":"complete"},"usage":{"model":"gpt-6-astra","outputTokens":0}}]}`)
	calls, status := ReadUsage(data, "T-test", []json.RawMessage{json.RawMessage(`"M-a"`)})
	require.Equal(t, "matched", status)
	require.Len(t, calls, 1)
	require.Equal(t, int64(0), calls[0].Attributes["gen_ai.usage.output_tokens"])
	require.NotContains(t, calls[0].Attributes, "gen_ai.usage.input_tokens")
}

func TestUsageRejectsAmbiguousOrMalformedRecords(t *testing.T) {
	for name, raw := range map[string]string{
		"negative":            `{"model":"m","outputTokens":-1}`,
		"fraction":            `{"model":"m","outputTokens":1.5}`,
		"string":              `{"model":"m","outputTokens":"9"}`,
		"overflow":            `{"model":"m","outputTokens":9223372036854775808}`,
		"unsafe JS integer":   `{"model":"m","outputTokens":9007199254740992}`,
		"cache exceeds total": `{"model":"m","totalInputTokens":10,"cacheReadInputTokens":7,"cacheCreationInputTokens":4}`,
		"inconsistent total":  `{"model":"m","totalInputTokens":10,"inputTokens":1,"cacheReadInputTokens":2,"cacheCreationInputTokens":3}`,
		"missing model":       `{"outputTokens":5}`,
		"missing usage":       `null`,
		"unknown shape":       `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			data := []byte(`{"id":"T-test","messages":[{"messageId":2,"role":"assistant","state":{"type":"complete"},"usage":` + raw + `}]}`)
			calls, status := ReadUsage(data, "T-test", []json.RawMessage{json.RawMessage(`2`)})
			require.Empty(t, calls)
			require.Equal(t, "partial", status)
		})
	}
}

func TestUsagePreservesPartialCountsWithoutInventingTotals(t *testing.T) {
	data := []byte(`{"id":"T-test","messages":[{"messageId":2,"role":"assistant","state":{"type":"complete"},"usage":{"model":"custom-model","totalInputTokens":null,"cacheReadInputTokens":7,"outputTokens":0}}]}`)
	calls, status := ReadUsage(data, "T-test", []json.RawMessage{json.RawMessage(`2`), json.RawMessage(`2`), json.RawMessage(`3`)})
	require.Equal(t, "partial", status)
	require.Len(t, calls, 1)
	require.Equal(t, map[string]any{"gen_ai.usage.cache_read.input_tokens": int64(7), "gen_ai.usage.output_tokens": int64(0)}, calls[0].Attributes)
}

func TestUsageSelectionBoundaries(t *testing.T) {
	message := `{"messageId":2,"protocolMessageID":"M-a","role":"assistant","state":{"type":"complete"},"usage":{"model":"m","outputTokens":17}}`
	for _, tc := range []struct {
		name, data, id, status string
		count                  int
	}{
		{"numeric", `{"id":"T-test","messages":[` + message + `]}`, `2`, "matched", 1},
		{"actor", `{"id":"T-test","messages":[` + message + `]}`, `"M-a"`, "matched", 1},
		{"numeric string is distinct", `{"id":"T-test","messages":[` + message + `]}`, `"2"`, "partial", 0},
		{"duplicate", `{"id":"T-test","messages":[` + message + `,` + message + `]}`, `2`, "partial", 0},
		{"wrong thread", `{"id":"T-other","messages":[` + message + `]}`, `2`, "unsupported", 0},
		{"truncated", `{"id":"T-test","messages":[`, `2`, "unsupported", 0},
		{"null", `null`, `2`, "unsupported", 0},
		{"bad ID", `{"id":"T-test","messages":[]}`, `null`, "invalid", 0},
		{"user usage", `{"id":"T-test","messages":[{"role":"user","messageId":2,"usage":{"model":"m","outputTokens":5}}]}`, `2`, "partial", 0},
		{"streaming", `{"id":"T-test","messages":[{"role":"assistant","messageId":2,"state":{"type":"streaming"},"usage":{"model":"m","outputTokens":5}}]}`, `2`, "partial", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, status := ReadUsage([]byte(tc.data), "T-test", []json.RawMessage{json.RawMessage(tc.id)})
			require.Equal(t, tc.status, status)
			require.Len(t, calls, tc.count)
		})
	}
}

func TestUsageCountsEachExportRecordOnceAcrossAliases(t *testing.T) {
	for _, messageID := range []string{`2`, `"M-a"`} {
		data := []byte(`{"id":"T-test","messages":[{"messageId":` + messageID + `,"protocolMessageID":"M-a","role":"assistant","state":{"type":"complete"},"usage":{"model":"m","outputTokens":17}}]}`)
		calls, status := ReadUsage(data, "T-test", []json.RawMessage{json.RawMessage(messageID), json.RawMessage(`"M-a"`)})
		require.Equal(t, "matched", status)
		require.Len(t, calls, 1)
		require.Equal(t, int64(17), calls[0].Attributes["gen_ai.usage.output_tokens"])
	}
}

func TestUsageWithoutSelectedIDsIsUnavailable(t *testing.T) {
	// An empty selection matches nothing, so the status must not claim "matched".
	calls, status := ReadUsage([]byte(`{"id":"T-test","messages":[]}`), "T-test", nil)
	require.Equal(t, "unavailable", status)
	require.Empty(t, calls)
}
