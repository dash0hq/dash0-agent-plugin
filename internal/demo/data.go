// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package demo

// Closed lists of mock data. One element of each is chosen at random per
// generated turn so the resulting telemetry looks like many different users
// working across a handful of repositories.

// repo describes a mock VCS repository. The working directory for a turn is
// derived from the user handle and the repository name.
type repo struct {
	Owner   string
	Name    string
	URLFull string
}

// repos is the closed list of 6 repositories turns can be attributed to.
var repos = []repo{
	{Owner: "dash0hq", Name: "dash0", URLFull: "https://github.com/dash0hq/dash0"},
	{Owner: "dash0hq", Name: "dash0-agent-plugin", URLFull: "https://github.com/dash0hq/dash0-agent-plugin"},
	{Owner: "dash0hq", Name: "dash0-operator", URLFull: "https://github.com/dash0hq/dash0-operator"},
	{Owner: "dash0hq", Name: "terraform-dash0", URLFull: "https://github.com/dash0hq/terraform-dash0"},
	{Owner: "dash0hq", Name: "otel-collector-builder", URLFull: "https://github.com/dash0hq/otel-collector-builder"},
	{Owner: "dash0hq", Name: "dash0-docs", URLFull: "https://github.com/dash0hq/dash0-docs"},
}

// teams is the closed list of 6 teams users are organized into.
var teams = []string{
	"Platform",
	"Observability",
	"Ingest",
	"Frontend",
	"Developer Experience",
	"SRE",
}

// user is a mock contributor and the team they belong to. Team membership is
// fixed (hard-coded) so the same person always reports under the same team.
type user struct {
	Name string
	Team string
}

// users is the closed list of 40 mock users. Names are entirely fictional and
// do not correspond to real contributors. Each user belongs to one of teams.
var users = []user{
	{"Avery Whitlock", "Platform"},
	{"Soren Kallenberg", "Platform"},
	{"Mira Delacroix", "Platform"},
	{"Tobin Ashgrove", "Platform"},
	{"Lena Fairweather", "Platform"},
	{"Caspian Holt", "Platform"},
	{"Delphine Marsh", "Platform"},
	{"Roan Pemberton", "Observability"},
	{"Isolde Varga", "Observability"},
	{"Bram Castellano", "Observability"},
	{"Niamh Oakhurst", "Observability"},
	{"Dario Lindqvist", "Observability"},
	{"Saffron Belmont", "Observability"},
	{"Tariq Esveld", "Observability"},
	{"Wren Calloway", "Ingest"},
	{"Magnus Thornberry", "Ingest"},
	{"Esme Ravensworth", "Ingest"},
	{"Kael Brightwater", "Ingest"},
	{"Yara Montenegro", "Ingest"},
	{"Florian Achterberg", "Ingest"},
	{"Petra Sandoval", "Ingest"},
	{"Linnea Hargrove", "Frontend"},
	{"Otto Vandermeer", "Frontend"},
	{"Ravenna Sturlson", "Frontend"},
	{"Cyrus Aldenmark", "Frontend"},
	{"Tessa Wickwood", "Frontend"},
	{"Idris Falkenrath", "Frontend"},
	{"Marlowe Ashby", "Developer Experience"},
	{"Sable Korhonen", "Developer Experience"},
	{"Quill Bergstrom", "Developer Experience"},
	{"Verity Lindholm", "Developer Experience"},
	{"Anselm Drache", "Developer Experience"},
	{"Juniper Vael", "Developer Experience"},
	{"Crispin Mowbray", "SRE"},
	{"Orla Steinbeck", "SRE"},
	{"Thaddeus Wren", "SRE"},
	{"Coraline Vesper", "SRE"},
	{"Lucan Ostervald", "SRE"},
	{"Briony Halloran", "SRE"},
	{"Emrys Tolliver", "SRE"},
}

// branchTitles is the closed list of kebab-case titles used to build branch
// names of the form ENG-<number>-<title>.
var branchTitles = []string{
	"add-slo-catalog-ordering",
	"fix-cardinality-visibility",
	"vcs-metrics-full-retention",
	"improve-span-ingest-throughput",
	"refactor-otlp-exporter",
	"add-dashboard-templates",
	"reduce-query-latency",
	"support-exemplars",
	"fix-trace-context-propagation",
	"add-metric-rollups",
	"investigate-cursor-integration",
	"harden-auth-token-handling",
	"add-resource-detection",
	"migrate-to-go-1-25",
	"optimize-log-pipeline",
	"add-session-replay-links",
	"fix-flaky-integration-tests",
	"add-prometheus-remote-write",
	"improve-error-messages",
	"add-multi-tenant-datasets",
}

// models is the closed list of model identifiers a turn can run on.
var models = []string{
	"claude-opus-4-8",
	"claude-sonnet-4-6",
	"claude-haiku-4-5",
}

// effortLevels is the closed list of effort levels reported on spans.
var effortLevels = []string{"low", "medium", "high", "xhigh"}

// messagePair is a user prompt paired with a plausible assistant response.
type messagePair struct {
	Input  string
	Output string
}

// messagePairs is the closed list of 20 prompt/response pairs.
var messagePairs = []messagePair{
	{"Add a unit test for the OTLP trace exporter covering the retry path.", "Added a test that simulates a 503 then a 200 and asserts the request is retried exactly once."},
	{"Why is the cardinality of this metric so high?", "The label set includes the raw request URL. I extracted a path template instead, which collapses the series count by ~95%."},
	{"Refactor the span builder to reduce duplication between tool and LLM spans.", "Extracted a shared newBaseSpan helper; both constructors now delegate to it, removing ~40 duplicated lines."},
	{"Can you fix the flaky integration test in the ingest pipeline?", "The flake was a race on the shared clock. I injected a fixed clock into the test and the run is now deterministic."},
	{"Implement exponential backoff for the collector's export retries.", "Implemented capped exponential backoff with jitter (base 500ms, max 30s) and added a test for the delay sequence."},
	{"Summarize what changed in the last 10 commits on main.", "The recent commits add VCS metric retention, fix a SessionStart double-init, and ship the v0.1.10 release."},
	{"Add a dashboard template for p99 query latency.", "Added a Perses dashboard JSON with a p99 latency panel grouped by dataset and a 7-day time range default."},
	{"The auth token is leaking into subprocess env, can you fix it?", "Switched the secure read path to avoid exporting the token as an env var; it's now passed only via the request header."},
	{"Write a migration to add a dataset column to the spans table.", "Added a forward migration with a backfill default of 'default' and a matching down migration; both are idempotent."},
	{"Optimize the log parsing hot loop, it's showing up in profiles.", "Replaced the per-line regex with a prefix scan and reused the buffer; throughput improved ~3x in the benchmark."},
	{"Add support for OTLP exemplars on histogram metrics.", "Wired exemplar attachment through the aggregator and added trace-id correlation; covered by a new round-trip test."},
	{"Investigate why Cursor sessions aren't grouped correctly.", "Cursor reports model='default'; I rewrite it to 'cursor-auto' before export so sessions now group as expected."},
	{"Add resource detection for the working directory and git branch.", "Added a vcs.Detect call that populates repository, owner, branch, and revision attributes on every span."},
	{"Reduce p99 query latency on the SLO catalog endpoint.", "Added a covering index and removed an N+1 query; p99 dropped from 820ms to 140ms in load tests."},
	{"Make the error messages from the exporter more actionable.", "Errors now include the endpoint, HTTP status, and a hint about the auth token when a 401 is returned."},
	{"Add a prometheus remote-write receiver to the collector.", "Added the receiver with sample-to-datapoint conversion and a config validation step; documented the new endpoint."},
	{"Migrate the build to Go 1.25 and fix any deprecations.", "Bumped the module to go 1.25.0, replaced the deprecated rand.Seed usage with math/rand/v2, and the suite is green."},
	{"Add multi-tenant dataset routing based on a header.", "Routing now reads Dash0-Dataset and falls back to 'default'; added validation rejecting unknown datasets with a 400."},
	{"Trace why SessionStart initialization runs twice.", "Two hooks fired on resume; I guarded init with a once-per-session marker file so it now runs exactly once."},
	{"Add session replay deep-links to the Stop hook output.", "The Stop hook now derives the app URL from the OTLP endpoint and prints a deep-link to the session details view."},
}

// bashCommand is a mock Bash tool invocation.
type bashCommand struct {
	Family    string
	Arguments string // JSON object as sent in gen_ai.tool.call.arguments
	Result    string // JSON object as sent in gen_ai.tool.call.result
}

// bashCommands is the closed list of mock Bash tool calls.
var bashCommands = []bashCommand{
	{"git", `{"command":"git status --short && git log --oneline -5"}`, `{"interrupted":false,"isImage":false,"stdout":" M internal/otlp/trace.go\ne1a46f0 fix: show session URL at SessionStart\n91d37bc release: v0.1.10","stderr":""}`},
	{"go", `{"command":"go test ./internal/... -count=1"}`, `{"interrupted":false,"isImage":false,"stdout":"ok  github.com/dash0hq/dash0-agent-plugin/internal/otlp 0.412s\nok  github.com/dash0hq/dash0-agent-plugin/internal/pipeline 0.301s","stderr":""}`},
	{"grep", `{"command":"grep -rn \"gen_ai.usage\" internal/ | head"}`, `{"interrupted":false,"isImage":false,"stdout":"internal/otlp/otlp.go:191: gen_ai.usage.input_tokens","stderr":""}`},
	{"ls", `{"command":"ls -la internal/demo/spans"}`, `{"interrupted":false,"isImage":false,"stdout":"chat.json\nmcp_server.json\nskill.json\ntool.json","stderr":""}`},
	{"go", `{"command":"go build ./... && echo built"}`, `{"interrupted":false,"isImage":false,"stdout":"built","stderr":""}`},
	{"npm", `{"command":"npm run lint"}`, `{"interrupted":false,"isImage":false,"stdout":"> lint\n> eslint .\n\nNo problems found.","stderr":""}`},
	{"docker", `{"command":"docker compose up -d collector"}`, `{"interrupted":false,"isImage":false,"stdout":"Container collector  Started","stderr":""}`},
	{"cat", `{"command":"cat go.mod"}`, `{"interrupted":false,"isImage":false,"stdout":"module github.com/dash0hq/dash0-agent-plugin\n\ngo 1.25.0","stderr":""}`},
	{"find", `{"command":"find . -name '*.go' | wc -l"}`, `{"interrupted":false,"isImage":false,"stdout":"42","stderr":""}`},
	{"make", `{"command":"make test"}`, `{"interrupted":false,"isImage":false,"stdout":"go test ./...\nPASS","stderr":""}`},
}

// mcpTool is a mock MCP tool invocation.
type mcpTool struct {
	Server    string // dash0.gen_ai.tool.mcp_server
	ToolName  string // gen_ai.tool.name (mcp__<server>__<tool>)
	Arguments string
	Result    string
}

// mcpTools is the closed list of mock MCP tool calls.
var mcpTools = []mcpTool{
	{"claude_ai_Slack", "mcp__claude_ai_Slack__slack_search_public_and_private", `{"query":"cardinality visibility prd"}`, `{"messages":[{"channel":"#eng-observability","text":"PRD draft is in Notion, feedback by Friday"}]}`},
	{"claude_ai_Linear", "mcp__claude_ai_Linear__search_issues", `{"query":"slo catalog ordering","limit":5}`, `{"issues":[{"id":"ENG-265","title":"SLO catalog additional ordering keys","state":"In Progress"}]}`},
	{"claude_ai_GitHub", "mcp__claude_ai_GitHub__get_pull_request", `{"owner":"dash0hq","repo":"dash0","number":4821}`, `{"title":"feat: vcs metrics full retention","state":"open","additions":214,"deletions":38}`},
	{"claude_ai_Sentry", "mcp__claude_ai_Sentry__list_issues", `{"project":"ingest","query":"is:unresolved"}`, `{"issues":[{"culprit":"pipeline.Process","count":17,"level":"error"}]}`},
	{"claude_ai_Notion", "mcp__claude_ai_Notion__search", `{"query":"cardinality visibility PRD"}`, `{"results":[{"title":"PRD: Cardinality Visibility","url":"https://notion.so/..."}]}`},
	{"claude_ai_PostHog", "mcp__claude_ai_PostHog__query", `{"event":"plugin_installed","period":"7d"}`, `{"result":{"count":312,"trend":"+18%"}}`},
}

// skillCall is a mock Skill tool invocation.
type skillCall struct {
	Name      string // dash0.gen_ai.tool.skill.name
	Arguments string
	Result    string
}

// skillCalls is the closed list of mock Skill tool calls.
var skillCalls = []skillCall{
	{"simplify", `{"args":""}`, `{"summary":"Applied 3 simplifications across 2 files, removing 28 lines."}`},
	{"code-review", `{"args":"--effort high"}`, `{"summary":"Reviewed diff: 1 correctness finding, 2 cleanups. Applied the cleanups."}`},
	{"ce-commit", `{"args":""}`, `{"summary":"Created commit: refactor: extract shared span builder."}`},
	{"deep-research", `{"args":"otlp exemplar best practices"}`, `{"summary":"Synthesized a cited report from 9 sources on exemplar attachment."}`},
	{"verify", `{"args":""}`, `{"summary":"Ran the app and confirmed the session deep-link renders correctly."}`},
	{"init", `{"args":""}`, `{"summary":"Generated CLAUDE.md documenting build, test, and release commands."}`},
}
