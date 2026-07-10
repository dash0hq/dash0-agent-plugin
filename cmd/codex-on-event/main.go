// codex-on-event is the OpenAI Codex-side entrypoint. Codex spawns this binary
// fresh for every hook event (via scripts/codex-on-event.sh, which downloads the
// matching release on first run), pipes the hook JSON in on stdin, and expects a
// clean exit. The binary:
//
//  1. Reads the Codex hook payload from stdin.
//  2. Normalizes it to the pipeline's canonical event vocabulary (see
//     internal/source/codex). Codex's hook events already match that vocabulary
//     almost exactly, so normalization is nearly a passthrough — its only real
//     job is deriving tool-call duration, which Codex omits.
//  3. Hands off to pipeline.Process, which writes scratch state, manages trace
//     context across hook invocations, and emits OTLP spans.
//
// Telemetry failures never break the user's agent loop: errors are logged to
// stderr and the process exits 0.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/dotenv"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
	"github.com/dash0hq/dash0-agent-plugin/internal/source/codex"
)

func main() {
	// Codex, like Cursor, can treat a non-zero hook exit as a blocking failure.
	// We always exit 0 so a broken exporter never breaks the user's session —
	// errors land on stderr only.
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "codex-on-event: %v\n", err)
	}
}

func run() error {
	dotenv.Load(".env")

	dataDir, err := resolveDataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating data directory %s: %w", dataDir, err)
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("parsing JSON from stdin: %w", err)
	}

	// Codex hooks carry the workspace as `cwd`. Codex may spawn the hook with a
	// different process CWD, so chdir into the payload's cwd before normalization
	// so vcs.Detect()'s git commands see the right working tree.
	chdirToCwd(event)

	// Normalization needs the per-session scratch dir to back-calculate tool-call
	// duration from the matching PreToolUse it logged earlier. Compute it the same
	// way pipeline.Process does so both agree on the path.
	sessionID, _ := event["session_id"].(string)
	sessionDir := pipeline.SessionDir(dataDir, sessionID)

	now := time.Now().UTC()

	event = codex.Normalize(event, sessionDir, now)
	if event == nil {
		// Event the pipeline doesn't consume — exit cleanly.
		return nil
	}

	// Provider is set to openai (Codex is single-vendor). The GenAI layer still
	// resolves provider per-event from the model name (e.g. gpt-*/o*/codex-* →
	// openai) and only falls back to this value when a model is absent.
	cfg := otlp.Config{
		OTLPUrl:      dash0Env("OTLP_URL"),
		AuthToken:    pluginOptionSecure("AUTH_TOKEN"),
		Dataset:      dash0Env("DATASET"),
		AgentName:    agentName(),
		HarnessName:  "codex",
		Provider:     "openai",
		TeamName:     dash0Env("TEAM_NAME"),
		OmitUserInfo: dash0EnvBool("OMIT_USER_INFO", false),
		OmitIO:       dash0EnvBool("OMIT_IO", true),
		Debug:        dash0EnvBool("DEBUG", false),
		DebugFile:    dash0Env("DEBUG_FILE"),
	}
	pipeline.ValidateOTLPURL(&cfg)

	result, err := pipeline.Process(event, cfg, dataDir, now)
	if err != nil {
		return err
	}

	// Codex ignores stdout for observational hooks; surface status on stderr so
	// it appears in Codex's hook log without affecting the agent loop.
	for _, msg := range result.Messages {
		if msg.UserText != "" {
			fmt.Fprintln(os.Stderr, msg.UserText)
		}
	}

	return nil
}

// resolveDataDir picks the per-source scratch root for Codex sessions.
// Precedence: DASH0_PLUGIN_DATA env override > XDG_STATE_HOME > ~/.local/state.
// All sit under a dash0-agent-plugin/codex subdirectory so we don't collide with
// other tools or the Claude Code / Cursor plugins.
func resolveDataDir() (string, error) {
	if v := os.Getenv("DASH0_PLUGIN_DATA"); v != "" {
		return v, nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving HOME: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "dash0-agent-plugin", "codex"), nil
}

// agentName picks the service.name / gen_ai.agent.name attribute for spans.
// Defaults to "codex"; can be overridden via DASH0_AGENT_NAME.
func agentName() string {
	if v := os.Getenv("DASH0_AGENT_NAME"); v != "" {
		return v
	}
	return "codex"
}

// dash0Env reads DASH0_<key>. Used for non-sensitive options; the bootstrap
// script exports these from the config file, and DASH0_* env vars also work as a
// fallback (useful for CI/dev).
//
// Note: sensitive values (AUTH_TOKEN) must use pluginOptionSecure instead to
// prevent env var leakage into tool-spawned shells.
func dash0Env(key string) string {
	return os.Getenv("DASH0_" + key)
}

// pluginOptionSecure reads only from CODEX_PLUGIN_OPTION_<key> without falling
// back to DASH0_<key>. Use for sensitive values like auth tokens that must not
// leak into tool-spawned shell environments — Codex agents can spawn arbitrary
// processes (Bash, MCP, etc.) which inherit our env, and other Dash0 tools look
// for DASH0_AUTH_TOKEN specifically.
func pluginOptionSecure(key string) string {
	return os.Getenv("CODEX_PLUGIN_OPTION_" + key)
}

// chdirToCwd moves the process into the hook payload's cwd. Best-effort: if the
// field is missing or chdir fails, we keep the original CWD and let vcs.Detect
// produce what it can.
func chdirToCwd(event map[string]any) {
	cwd, ok := event["cwd"].(string)
	if !ok || cwd == "" {
		return
	}
	_ = os.Chdir(cwd)
}

func dash0EnvBool(key string, defaultVal bool) bool {
	v := strings.ToLower(strings.TrimSpace(dash0Env(key)))
	if v == "" {
		return defaultVal
	}
	return v == "true" || v == "1"
}
