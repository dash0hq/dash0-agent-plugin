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
	"flag"
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
	// Install-time subcommand: emit the managed config.toml block (hook
	// registrations + reproduced trust hashes) for install-codex.sh to append.
	// Kept in the binary so the trust-hash logic has one tested home.
	if len(os.Args) > 1 && os.Args[1] == "emit-codex-hooks" {
		if err := emitCodexHooks(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "codex-on-event: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Codex, like Cursor, can treat a non-zero hook exit as a blocking failure.
	// We always exit 0 so a broken exporter never breaks the user's session —
	// errors land on stderr only.
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "codex-on-event: %v\n", err)
	}
}

// emitCodexHooks prints the marker-delimited config.toml block the installer
// appends: --command is the exact hook command string, --config is the absolute
// path of the config file it will be written into (part of each trust key).
// existing config content is read from --config (if present) so pre-existing
// user hook groups are counted for correct trust-key indices.
func emitCodexHooks(args []string) error {
	fs := flag.NewFlagSet("emit-codex-hooks", flag.ContinueOnError)
	command := fs.String("command", "", "exact hook command string Codex will run")
	configPath := fs.String("config", "", "absolute path of the config.toml the block is written into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *command == "" || *configPath == "" {
		return fmt.Errorf("emit-codex-hooks requires --command and --config")
	}

	// Codex keys hook trust on its RESOLVED config path (it realpath's the file),
	// so the trust-state key must use the canonical path or Codex won't find our
	// entry and will treat the hook as untrusted. Resolve symlinks on the parent
	// dir (the file itself may not exist yet) and rejoin the filename.
	keyPath := *configPath
	if resolvedDir, err := filepath.EvalSymlinks(filepath.Dir(*configPath)); err == nil {
		keyPath = filepath.Join(resolvedDir, filepath.Base(*configPath))
	}

	// Read existing config minus any prior managed block, so re-installs count
	// only the user's own hook groups.
	var existing string
	if data, err := os.ReadFile(*configPath); err == nil {
		existing = codex.StripManagedBlock(string(data))
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", *configPath, err)
	}

	block, err := codex.RenderManagedBlock(keyPath, *command, existing)
	if err != nil {
		return err
	}
	fmt.Print(block)
	return nil
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

	// Codex ignores stdout for observational hooks and does not surface hook
	// stderr in the TUI or any documented log, so this is best-effort diagnostic
	// output only (visible when running the binary directly or in the e2e
	// harness); it never affects the agent loop.
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
