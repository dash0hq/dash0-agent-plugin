// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package demo

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildVCSMetricsPayload(t *testing.T) {
	r := repo{Owner: "dash0hq", Name: "dash0", URLFull: "https://github.com/dash0hq/dash0"}
	branch := "ENG-265-add-slo-catalog-ordering"
	ts := time.Now().UTC()

	raw, err := buildVCSMetricsPayload(r, branch, ts)
	require.NoError(t, err)

	// histogram sum is an OTLP double: it must appear unquoted on the wire.
	assert.Regexp(t, `"sum":\d`, string(raw))
	assert.NotContains(t, string(raw), `"sum":"`)

	var req exportMetricsRequest
	require.NoError(t, json.Unmarshal(raw, &req))
	require.Len(t, req.ResourceMetrics, 1)
	metrics := req.ResourceMetrics[0].ScopeMetrics[0].Metrics

	// The five VCS metrics from the emission contract are all present.
	names := map[string]bool{}
	for _, m := range metrics {
		names[m.Name] = true
	}
	for _, want := range []string{
		"dash0.gen_ai.vcs.change.count",
		"dash0.gen_ai.vcs.change.time_to_merge",
		"dash0.gen_ai.vcs.change.time_to_approval",
		"dash0.gen_ai.vcs.ref.lines_added",
		"dash0.gen_ai.vcs.ref.lines_deleted",
	} {
		assert.True(t, names[want], "missing metric %s", want)
	}

	// Every data point carries the repo + branch join dimensions matching the spans.
	assertDim := func(attrs []attribute, key, want string) {
		for _, a := range attrs {
			if a.Key == key {
				require.NotNil(t, a.Value.StringValue)
				assert.Equal(t, want, *a.Value.StringValue)
				return
			}
		}
		t.Errorf("missing dimension %s", key)
	}
	for _, m := range metrics {
		var attrs []attribute
		switch {
		case m.Sum != nil:
			attrs = m.Sum.DataPoints[0].Attributes
		case m.Histogram != nil:
			dp := m.Histogram.DataPoints[0]
			attrs = dp.Attributes
			// The histogram sum must be a JSON number (OTLP double), not a
			// string — otherwise histogram_sum() reads nothing in Dash0.
			require.NotNil(t, dp.Sum, "%s: histogram sum missing", m.Name)
			assert.Greater(t, *dp.Sum, 0.0)
			assert.Len(t, dp.BucketCounts, len(dp.ExplicitBounds)+1)
		}
		assertDim(attrs, "dash0.gen_ai.vcs.repository.url.full", r.URLFull)
		assertDim(attrs, "dash0.gen_ai.vcs.repository.name", r.Name)
		assertDim(attrs, "dash0.gen_ai.vcs.ref.head.name", branch)
	}
}

func TestHistogramBucketsSingleObservation(t *testing.T) {
	bounds := []float64{300, 900, 1800}

	// A value falls into exactly the first bucket whose bound it does not exceed.
	counts := histogramBuckets(1000, bounds)
	require.Len(t, counts, len(bounds)+1)
	assert.Equal(t, []string{"0", "0", "1", "0"}, counts)

	// A value larger than every bound lands in the overflow bucket.
	counts = histogramBuckets(5000, bounds)
	assert.Equal(t, []string{"0", "0", "0", "1"}, counts)
}
