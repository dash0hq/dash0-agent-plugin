// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package demo

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// vcsProvider is the provider all demo repositories live on.
const vcsProvider = "github"

// emitVCSMetrics generates synthetic dash0.gen_ai.vcs.* metrics for the turn,
// matching the ADR #93 emission contract. These metrics join with the plugin's
// agent spans on the same VCS dimensions (repository.url.full, ref.head.name) —
// the repository and branch come straight from the turn, so they match the
// spans exactly.
func (t turn) emitVCSMetrics(cfg otlp.Config, ts time.Time) error {
	payload, err := buildVCSMetricsPayload(t.repo, t.branch, ts)
	if err != nil {
		return fmt.Errorf("building VCS metrics payload: %w", err)
	}
	return otlp.SendRawMetrics(payload, cfg)
}

func buildVCSMetricsPayload(r repo, branch string, ts time.Time) ([]byte, error) {
	tsNano := strconv.FormatInt(ts.UnixNano(), 10)
	startNano := strconv.FormatInt(ts.Add(-1*time.Hour).UnixNano(), 10)

	// Shared dimensions — the join surface with plugin spans.
	dims := []attribute{
		{Key: "dash0.gen_ai.vcs.repository.url.full", Value: strVal(r.URLFull)},
		{Key: "dash0.gen_ai.vcs.repository.name", Value: strVal(r.Name)},
		{Key: "dash0.gen_ai.vcs.owner.name", Value: strVal(r.Owner)},
		{Key: "dash0.gen_ai.vcs.provider.name", Value: strVal(vcsProvider)},
		{Key: "dash0.gen_ai.vcs.ref.head.name", Value: strVal(branch)},
	}

	var metrics []metric

	// 1. dash0.gen_ai.vcs.change.count — changes merged
	changeCount := randomBetween(1, 6)
	metrics = append(metrics, metric{
		Name: "dash0.gen_ai.vcs.change.count",
		Unit: "{change}",
		Sum: &sumMetric{
			DataPoints: []sumDataPoint{{
				Attributes:        append(dims, attribute{Key: "dash0.gen_ai.vcs.change.state", Value: strVal("merged")}),
				StartTimeUnixNano: startNano,
				TimeUnixNano:      tsNano,
				AsInt:             fmt.Sprintf("%d", changeCount),
			}},
			AggregationTemporality: 2, // cumulative
			IsMonotonic:            true,
		},
	})

	// 2. dash0.gen_ai.vcs.change.time_to_merge — histogram
	mergeSeconds := randomBetween(3600, 172800) // 1h – 48h
	metrics = append(metrics, metric{
		Name: "dash0.gen_ai.vcs.change.time_to_merge",
		Unit: "s",
		Histogram: &histogramMetric{
			DataPoints: []histogramDataPoint{{
				Attributes:        append(dims, attribute{Key: "dash0.gen_ai.vcs.change.cycle_anchor", Value: strVal("first_commit")}),
				StartTimeUnixNano: startNano,
				TimeUnixNano:      tsNano,
				Count:             "1",
				Sum:               f64(float64(mergeSeconds)),
				ExplicitBounds:    []float64{300, 900, 1800, 3600, 7200, 14400, 28800, 86400, 172800, 604800, 1209600, 2419200},
				BucketCounts:      histogramBuckets(float64(mergeSeconds), []float64{300, 900, 1800, 3600, 7200, 14400, 28800, 86400, 172800, 604800, 1209600, 2419200}),
			}},
			AggregationTemporality: 2,
		},
	})

	// 3. dash0.gen_ai.vcs.change.time_to_approval — histogram
	approvalSeconds := randomBetween(1800, 86400) // 30min – 24h
	metrics = append(metrics, metric{
		Name: "dash0.gen_ai.vcs.change.time_to_approval",
		Unit: "s",
		Histogram: &histogramMetric{
			DataPoints: []histogramDataPoint{{
				Attributes:        dims,
				StartTimeUnixNano: startNano,
				TimeUnixNano:      tsNano,
				Count:             "1",
				Sum:               f64(float64(approvalSeconds)),
				ExplicitBounds:    []float64{300, 900, 1800, 3600, 7200, 14400, 28800, 86400},
				BucketCounts:      histogramBuckets(float64(approvalSeconds), []float64{300, 900, 1800, 3600, 7200, 14400, 28800, 86400}),
			}},
			AggregationTemporality: 2,
		},
	})

	// 4. dash0.gen_ai.vcs.ref.lines_added
	linesAdded := randomBetween(20, 500)
	metrics = append(metrics, metric{
		Name: "dash0.gen_ai.vcs.ref.lines_added",
		Unit: "{line}",
		Sum: &sumMetric{
			DataPoints: []sumDataPoint{{
				Attributes:        dims,
				StartTimeUnixNano: startNano,
				TimeUnixNano:      tsNano,
				AsInt:             fmt.Sprintf("%d", linesAdded),
			}},
			AggregationTemporality: 2,
			IsMonotonic:            true,
		},
	})

	// 5. dash0.gen_ai.vcs.ref.lines_deleted
	linesDeleted := randomBetween(5, 200)
	metrics = append(metrics, metric{
		Name: "dash0.gen_ai.vcs.ref.lines_deleted",
		Unit: "{line}",
		Sum: &sumMetric{
			DataPoints: []sumDataPoint{{
				Attributes:        dims,
				StartTimeUnixNano: startNano,
				TimeUnixNano:      tsNano,
				AsInt:             fmt.Sprintf("%d", linesDeleted),
			}},
			AggregationTemporality: 2,
			IsMonotonic:            true,
		},
	})

	req := exportMetricsRequest{
		ResourceMetrics: []resourceMetrics{{
			Resource: resource{
				Attributes: []attribute{
					{Key: "service.name", Value: strVal("github-app")},
					{Key: "dash0.gen_ai.vcs.provider.name", Value: strVal(vcsProvider)},
					{Key: "dash0.gen_ai.vcs.owner.name", Value: strVal(r.Owner)},
				},
			},
			ScopeMetrics: []scopeMetrics{{
				Scope: scope{
					Name:    "dash0-github-app",
					Version: "1.0.0",
				},
				Metrics: metrics,
			}},
		}},
	}

	return json.Marshal(req)
}

// histogramBuckets produces the bucket counts array for a single observation.
func histogramBuckets(value float64, bounds []float64) []string {
	counts := make([]string, len(bounds)+1)
	placed := false
	for i, b := range bounds {
		if !placed && value <= b {
			counts[i] = "1"
			placed = true
		} else {
			counts[i] = "0"
		}
	}
	if !placed {
		counts[len(bounds)] = "1"
	} else {
		counts[len(bounds)] = "0"
	}
	return counts
}

// randomBetween returns a random int in [min, max).
func randomBetween(min, max int) int {
	return min + rand.IntN(max-min)
}

// OTLP metrics JSON wire types (matching the OTLP/JSON spec).

type exportMetricsRequest struct {
	ResourceMetrics []resourceMetrics `json:"resourceMetrics"`
}

type resourceMetrics struct {
	Resource     resource       `json:"resource"`
	ScopeMetrics []scopeMetrics `json:"scopeMetrics"`
}

type resource struct {
	Attributes []attribute `json:"attributes"`
}

type scopeMetrics struct {
	Scope   scope    `json:"scope"`
	Metrics []metric `json:"metrics"`
}

type scope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type metric struct {
	Name      string           `json:"name"`
	Unit      string           `json:"unit,omitempty"`
	Sum       *sumMetric       `json:"sum,omitempty"`
	Histogram *histogramMetric `json:"histogram,omitempty"`
}

type sumMetric struct {
	DataPoints             []sumDataPoint `json:"dataPoints"`
	AggregationTemporality int            `json:"aggregationTemporality"`
	IsMonotonic            bool           `json:"isMonotonic"`
}

type sumDataPoint struct {
	Attributes        []attribute `json:"attributes"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	TimeUnixNano      string      `json:"timeUnixNano"`
	AsInt             string      `json:"asInt"`
}

type histogramMetric struct {
	DataPoints             []histogramDataPoint `json:"dataPoints"`
	AggregationTemporality int                  `json:"aggregationTemporality"`
}

type histogramDataPoint struct {
	Attributes        []attribute `json:"attributes"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	TimeUnixNano      string      `json:"timeUnixNano"`
	Count             string      `json:"count"`
	// Sum is an OTLP `double`; in OTLP/JSON it MUST be encoded as a JSON number,
	// not a string. Encoding it as a string makes the histogram unreadable to
	// Dash0, which is why histogram_sum() returned nothing for the mock metrics.
	Sum            *float64  `json:"sum,omitempty"`
	ExplicitBounds []float64 `json:"explicitBounds"`
	BucketCounts   []string  `json:"bucketCounts"`
}

// f64 returns a pointer to v, for the optional double fields in the OTLP wire types.
func f64(v float64) *float64 { return &v }

type attribute struct {
	Key   string    `json:"key"`
	Value attrValue `json:"value"`
}

type attrValue struct {
	StringValue *string `json:"stringValue,omitempty"`
}

func strVal(s string) attrValue {
	return attrValue{StringValue: &s}
}
