package demo

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// TurnTemplate is a reusable pattern for a single prompt→tools→Stop sequence,
// derived from real captured sessions but with clean synthetic content.
type TurnTemplate struct {
	Theme  string // e.g. "code-review", "debugging", "ci", "planning"
	Prompt string
	Steps  []StepTemplate
}

// StepTemplate defines a tool call within a turn.
type StepTemplate struct {
	Tool       string
	Input      any
	Response   any
	DurationMs [2]int // [min, max] — sampled uniformly
}

// SampleDuration returns a random duration within the step's range.
func (s StepTemplate) SampleDuration() int {
	min, max := s.DurationMs[0], s.DurationMs[1]
	if max <= min {
		return min
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min)))
	if err != nil {
		return min
	}
	return min + int(n.Int64())
}

// TurnLibrary holds categorized turn templates for sampling.
var TurnLibrary = []TurnTemplate{
	// --- Code exploration / reading ---
	{
		Theme:  "explore",
		Prompt: "What does the session handler look like? Show me the main file.",
		Steps: []StepTemplate{
			{Tool: "Read", Input: map[string]any{"file_path": "src/handlers/sessions.ts"}, Response: "export async function handleSession(req: Request) {\n  const id = req.params.id;\n  // ...\n}\n", DurationMs: [2]int{80, 200}},
			{Tool: "Bash", Input: map[string]any{"command": "wc -l src/handlers/sessions.ts"}, Response: map[string]any{"stdout": "142 src/handlers/sessions.ts\n", "stderr": ""}, DurationMs: [2]int{50, 150}},
		},
	},
	{
		Theme:  "explore",
		Prompt: "Find all exported functions in the auth module.",
		Steps: []StepTemplate{
			{Tool: "Bash", Input: map[string]any{"command": "grep -rn 'export function' src/auth/"}, Response: map[string]any{"stdout": "src/auth/token.ts:5:export function verifyToken(t: string): boolean {\nsrc/auth/session.ts:12:export function createSession(user: User): Session {\nsrc/auth/middleware.ts:3:export function authMiddleware(req, res, next) {\n", "stderr": ""}, DurationMs: [2]int{100, 400}},
		},
	},

	// --- Debugging ---
	{
		Theme:  "debugging",
		Prompt: "The API returns 500 on /sessions/:id. Can you check the logs and trace it?",
		Steps: []StepTemplate{
			{Tool: "Bash", Input: map[string]any{"command": "grep -n 'Error\\|error' logs/api.log | tail -20"}, Response: map[string]any{"stdout": "2026-06-13T10:22:01Z ERROR sessions.handler: null pointer at getUser()\n2026-06-13T10:22:01Z ERROR middleware: request failed status=500 path=/sessions/abc123\n", "stderr": ""}, DurationMs: [2]int{200, 800}},
			{Tool: "Read", Input: map[string]any{"file_path": "src/handlers/sessions.ts"}, Response: "export async function getSession(req) {\n  const user = await getUser(req.auth.userId); // can be null\n  return { ...user.preferences }; // NPE here\n}\n", DurationMs: [2]int{60, 150}},
			{Tool: "Bash", Input: map[string]any{"command": "git log --oneline -5 src/handlers/sessions.ts"}, Response: map[string]any{"stdout": "a1b2c3d refactor: remove null check (was redundant)\nf4e5d6c feat: add session preferences\n", "stderr": ""}, DurationMs: [2]int{150, 500}},
		},
	},
	{
		Theme:  "debugging",
		Prompt: "Tests are failing in CI. What's broken?",
		Steps: []StepTemplate{
			{Tool: "Bash", Input: map[string]any{"command": "npm test -- --reporter=summary 2>&1 | tail -15"}, Response: map[string]any{"stdout": "FAIL src/auth/token.test.ts\n  ✕ verifyToken rejects expired tokens (12ms)\n  ✕ verifyToken handles malformed input (3ms)\n\nTests: 2 failed, 47 passed\n", "stderr": ""}, DurationMs: [2]int{3000, 8000}},
			{Tool: "Read", Input: map[string]any{"file_path": "src/auth/token.test.ts"}, Response: "describe('verifyToken', () => {\n  it('rejects expired tokens', () => {\n    expect(verifyToken(expiredToken)).toBe(false); // changed signature\n  });\n});\n", DurationMs: [2]int{60, 150}},
		},
	},

	// --- Editing / implementation ---
	{
		Theme:  "implementation",
		Prompt: "Add a retry wrapper around the HTTP client calls.",
		Steps: []StepTemplate{
			{Tool: "Read", Input: map[string]any{"file_path": "src/lib/http.ts"}, Response: "export async function fetchJSON(url: string) {\n  const res = await fetch(url);\n  return res.json();\n}\n", DurationMs: [2]int{60, 150}},
			{Tool: "Edit", Input: map[string]any{"file_path": "src/lib/http.ts", "old_string": "export async function fetchJSON", "new_string": "export async function fetchJSON"}, Response: map[string]any{"structuredPatch": []any{map[string]any{"lines": []any{"+import { retry } from './retry';", "+", "+export async function fetchJSON(url: string) {", "+  return retry(() => fetch(url).then(r => r.json()));", "+}"}}}}, DurationMs: [2]int{40, 120}},
			{Tool: "Bash", Input: map[string]any{"command": "npm test -- src/lib/http.test.ts"}, Response: map[string]any{"stdout": "PASS src/lib/http.test.ts\nTests: 3 passed\n", "stderr": ""}, DurationMs: [2]int{2000, 5000}},
		},
	},
	{
		Theme:  "implementation",
		Prompt: "Create a new endpoint for exporting sessions as CSV.",
		Steps: []StepTemplate{
			{Tool: "Bash", Input: map[string]any{"command": "ls src/handlers/"}, Response: map[string]any{"stdout": "auth.ts\nsessions.ts\nusers.ts\nwebhooks.ts\n", "stderr": ""}, DurationMs: [2]int{50, 150}},
			{Tool: "Write", Input: map[string]any{"file_path": "src/handlers/export.ts"}, Response: "File created successfully", DurationMs: [2]int{30, 80}},
			{Tool: "Edit", Input: map[string]any{"file_path": "src/routes.ts", "old_string": "// routes", "new_string": "// routes\nimport { exportCSV } from './handlers/export';"}, Response: map[string]any{"structuredPatch": []any{map[string]any{"lines": []any{"+import { exportCSV } from './handlers/export';"}}}}, DurationMs: [2]int{30, 80}},
			{Tool: "Bash", Input: map[string]any{"command": "npm test"}, Response: map[string]any{"stdout": "PASS\nTests: 52 passed\n", "stderr": ""}, DurationMs: [2]int{4000, 8000}},
		},
	},

	// --- CI / git operations ---
	{
		Theme:  "ci",
		Prompt: "Push this branch and open a PR.",
		Steps: []StepTemplate{
			{Tool: "Bash", Input: map[string]any{"command": "git status --short"}, Response: map[string]any{"stdout": "M src/lib/http.ts\nA src/handlers/export.ts\n", "stderr": ""}, DurationMs: [2]int{80, 200}},
			{Tool: "Bash", Input: map[string]any{"command": "git add -A && git commit -m 'feat: add CSV export endpoint'"}, Response: map[string]any{"stdout": "[feat/export-csv abc1234] feat: add CSV export endpoint\n 2 files changed, 45 insertions(+)\n", "stderr": ""}, DurationMs: [2]int{300, 1000}},
			{Tool: "Bash", Input: map[string]any{"command": "git push -u origin HEAD"}, Response: map[string]any{"stdout": "branch 'feat/export-csv' set up to track 'origin/feat/export-csv'\n", "stderr": ""}, DurationMs: [2]int{1500, 3000}},
			{Tool: "Bash", Input: map[string]any{"command": "gh pr create --fill"}, Response: map[string]any{"stdout": "https://github.com/acme/web/pull/145\n", "stderr": ""}, DurationMs: [2]int{2000, 4000}},
		},
	},

	// --- MCP tool calls ---
	{
		Theme:  "mcp-linear",
		Prompt: "Create a Linear issue for the auth token refresh bug.",
		Steps: []StepTemplate{
			{Tool: "mcp__claude_ai_Linear__list_projects", Input: map[string]any{"teamId": "team-eng"}, Response: map[string]any{"projects": []any{"Backend", "Frontend", "Infra"}}, DurationMs: [2]int{800, 2000}},
			{Tool: "mcp__claude_ai_Linear__save_issue", Input: map[string]any{"title": "Auth token refresh returns 401 after expiry", "projectId": "proj-backend", "priority": 2}, Response: map[string]any{"id": "ENG-892", "url": "https://linear.app/acme/issue/ENG-892"}, DurationMs: [2]int{1000, 3000}},
		},
	},
	{
		Theme:  "mcp-dash0",
		Prompt: "Check if there are any error spikes in the last hour.",
		Steps: []StepTemplate{
			{Tool: "mcp__dash0__query_metrics", Input: map[string]any{"query": "sum(rate(http_server_errors_total[5m])) by (service)"}, Response: map[string]any{"result": "api-gateway: 0.02/s, auth-service: 0.8/s (elevated)"}, DurationMs: [2]int{600, 1500}},
			{Tool: "mcp__dash0__query_metrics", Input: map[string]any{"query": "histogram_quantile(0.99, rate(http_server_request_duration_bucket{service=\"auth-service\"}[5m]))"}, Response: map[string]any{"result": "p99 = 4.2s (normal: 0.3s)"}, DurationMs: [2]int{600, 1500}},
		},
	},

	// --- Quick / short turns (1 tool or zero) ---
	{
		Theme:  "quick",
		Prompt: "What Go version are we on?",
		Steps: []StepTemplate{
			{Tool: "Bash", Input: map[string]any{"command": "cat go.mod | head -3"}, Response: map[string]any{"stdout": "module github.com/acme/web\n\ngo 1.22.0\n", "stderr": ""}, DurationMs: [2]int{50, 150}},
		},
	},
	{
		Theme:  "quick",
		Prompt: "How many test files do we have?",
		Steps: []StepTemplate{
			{Tool: "Bash", Input: map[string]any{"command": "find . -name '*_test.*' | wc -l"}, Response: map[string]any{"stdout": "38\n", "stderr": ""}, DurationMs: [2]int{100, 300}},
		},
	},
	{
		Theme:  "quick",
		Prompt: "Show me the package.json dependencies count.",
		Steps: []StepTemplate{
			{Tool: "Bash", Input: map[string]any{"command": "cat package.json | python3 -c \"import json,sys;d=json.load(sys.stdin);print(len(d.get('dependencies',{})),'deps')\""}, Response: map[string]any{"stdout": "24 deps\n", "stderr": ""}, DurationMs: [2]int{100, 400}},
		},
	},

	// --- Agent / subagent ---
	{
		Theme:  "subagent",
		Prompt: "Research how other projects handle rate limiting, then propose an approach.",
		Steps: []StepTemplate{
			{Tool: "Agent", Input: map[string]any{"prompt": "Research rate limiting patterns in Go web services", "subagent_type": "Explore"}, Response: map[string]any{"agentId": "agent-explore-01", "result": "Found 3 common approaches: token bucket, sliding window, leaky bucket."}, DurationMs: [2]int{10000, 30000}},
			{Tool: "Read", Input: map[string]any{"file_path": "src/middleware/ratelimit.ts"}, Response: "// placeholder — rate limiting not yet implemented\n", DurationMs: [2]int{60, 150}},
		},
	},

	// --- Planning ---
	{
		Theme:  "planning",
		Prompt: "Plan the migration from REST to GraphQL for the sessions API.",
		Steps: []StepTemplate{
			{Tool: "Bash", Input: map[string]any{"command": "find src/handlers -name '*.ts' | wc -l"}, Response: map[string]any{"stdout": "12\n", "stderr": ""}, DurationMs: [2]int{80, 200}},
			{Tool: "Read", Input: map[string]any{"file_path": "src/handlers/sessions.ts"}, Response: "export async function listSessions(req, res) { /* 45 lines */ }\nexport async function getSession(req, res) { /* 30 lines */ }\nexport async function deleteSession(req, res) { /* 15 lines */ }\n", DurationMs: [2]int{60, 150}},
			{Tool: "Bash", Input: map[string]any{"command": "grep -rn 'router\\.' src/routes.ts | wc -l"}, Response: map[string]any{"stdout": "18\n", "stderr": ""}, DurationMs: [2]int{80, 200}},
		},
	},
}

// PickTurns selects n random turn templates from the library.
func PickTurns(n int) []TurnTemplate {
	if n >= len(TurnLibrary) {
		return TurnLibrary
	}
	picked := make([]TurnTemplate, 0, n)
	used := make(map[int]bool)
	for len(picked) < n {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(TurnLibrary))))
		if err != nil {
			picked = append(picked, TurnLibrary[len(picked)%len(TurnLibrary)])
			continue
		}
		i := int(idx.Int64())
		if used[i] {
			continue
		}
		used[i] = true
		picked = append(picked, TurnLibrary[i])
	}
	return picked
}

// PickTurnsForSession returns 1–3 turns (randomly chosen count).
func PickTurnsForSession() []TurnTemplate {
	n, err := rand.Int(rand.Reader, big.NewInt(3))
	if err != nil {
		return PickTurns(2)
	}
	count := int(n.Int64()) + 1 // 1, 2, or 3
	return PickTurns(count)
}

// InjectWorkUnitIntoSteps replaces placeholder PR URLs and branch names in
// tool responses with values from the WorkUnit so extraction fires correctly.
func InjectWorkUnitIntoSteps(steps []StepTemplate, wu WorkUnit) []StepTemplate {
	out := make([]StepTemplate, len(steps))
	for i, s := range steps {
		out[i] = s
		if resp, ok := s.Response.(map[string]any); ok {
			newResp := copyMap(resp)
			if stdout, ok := newResp["stdout"].(string); ok {
				if wu.PR != nil {
					// If the step creates a PR, inject the real URL
					if s.Tool == "Bash" && containsAny(stdout, "gh pr create", "pull/") {
						newResp["stdout"] = fmt.Sprintf("Creating pull request for %s\n%s\n", wu.Branch, wu.PR.URL)
					}
				}
			}
			out[i].Response = newResp
		}
	}
	return out
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
