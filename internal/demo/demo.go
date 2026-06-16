// Package demo generates synthetic Claude Code agent telemetry by replaying
// fabricated hook events through the real pipeline.Process path. Because it
// reuses the production OTLP emission pipeline, the spans it produces are
// byte-for-byte the shape the plugin emits for real sessions — no schema drift.
package demo

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
)

// Run is the entrypoint for `on-event generate`.
func Run(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	otlpURL := fs.String("otlp-url", os.Getenv("DASH0_OTLP_URL"), "Dash0 OTLP endpoint")
	authToken := fs.String("auth-token", os.Getenv("DASH0_AUTH_TOKEN"), "Dash0 ingest auth token")
	dataset := fs.String("dataset", os.Getenv("DASH0_DATASET"), "Dash0 dataset name")
	sessionsPerUser := fs.Int("sessions", 3, "number of sessions per persona")
	dryRun := fs.Bool("dry-run", false, "print the assembled event stream instead of sending")
	debugFile := fs.String("debug-file", "", "append the emitted OTLP payloads to this file (for inspection)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := otlp.Config{
		OTLPUrl:      *otlpURL,
		AuthToken:    *authToken,
		Dataset:      *dataset,
		AgentName:    "claude-code",
		OmitUserInfo: false,
		OmitIO:       false,
		Debug:        *debugFile != "",
		DebugFile:    *debugFile,
	}
	if *debugFile != "" && cfg.OTLPUrl == "" {
		*dryRun = false
	}
	if !*dryRun && cfg.OTLPUrl != "" {
		if !pipeline.ValidateOTLPURL(&cfg) {
			return fmt.Errorf("a valid --otlp-url is required (or set DASH0_OTLP_URL); pass --dry-run to preview")
		}
		if cfg.AuthToken == "" {
			return fmt.Errorf("--auth-token is required (or set DASH0_AUTH_TOKEN); pass --dry-run to preview")
		}
	}

	// Build work units: each persona gets multiple sessions with varied branches.
	units := buildDemoWorkUnits(*sessionsPerUser)

	for i, wu := range units {
		if err := generateSession(cfg, wu, *dryRun); err != nil {
			return fmt.Errorf("session %d (%s): %w", i, wu.Persona.Name, err)
		}
	}
	return nil
}

// buildDemoWorkUnits creates work units: sessionsPerUser sessions for each
// persona, each with a sampled model and rotating branch/PR.
func buildDemoWorkUnits(sessionsPerUser int) []WorkUnit {
	var units []WorkUnit
	prCounter := 140
	for _, p := range DefaultPersonas {
		for s := 0; s < sessionsPerUser; s++ {
			branch := DefaultBranches[(len(units))%len(DefaultBranches)]
			model := DefaultModelPool.Pick()
			p.Model = model
			prCounter++
			units = append(units, WorkUnit{
				Persona: p,
				Repo:    DefaultRepo,
				Branch:  branch,
				PR: &PullRequest{
					Number: prCounter,
					URL:    fmt.Sprintf("%s/pull/%d", DefaultRepo.URL(), prCounter),
				},
			})
		}
	}
	return units
}

// generateSession drives a single 1-turn session derived from a WorkUnit.
func generateSession(cfg otlp.Config, wu WorkUnit, dryRun bool) error {
	dataDir, err := os.MkdirTemp("", "dash0-demo-data-")
	if err != nil {
		return fmt.Errorf("creating data dir: %w", err)
	}
	defer os.RemoveAll(dataDir)

	ownerRepo := wu.Repo.Owner + "/" + wu.Repo.Name
	repoDir, _, err := setupPersonaRepo(wu.Persona, ownerRepo, wu.Branch)
	if err != nil {
		return fmt.Errorf("setting up persona repo: %w", err)
	}
	defer os.RemoveAll(repoDir)

	commitSHA, _ := gitOut(repoDir, "rev-parse", "HEAD")
	wu.Commits = []string{commitSHA}

	restore, err := chdir(repoDir)
	if err != nil {
		return err
	}
	defer restore()

	transcriptPath := filepath.Join(dataDir, "transcript.jsonl")
	sessionID := "demo-" + nonce()
	cwd := repoDir
	model := wu.Persona.Model

	prURL := ""
	if wu.PR != nil {
		prURL = wu.PR.URL
	}

	// Pick 1–3 turns from the library for this session.
	turns := PickTurnsForSession()

	scale := TokenScale(model)
	clock := time.Now().UTC().Add(-15 * time.Minute)
	tick := func(d time.Duration) time.Time { clock = clock.Add(d); return clock }

	var stream []timedEvent
	var transcriptEntries []transcriptEntry

	stream = append(stream, timedEvent{tick(0), map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      sessionID,
		"model":           model,
		"cwd":             cwd,
		"source":          "startup",
		"transcript_path": transcriptPath,
	}})

	for _, turn := range turns {
		// Inter-turn gap (user thinking time).
		tick(4 * time.Second)

		stream = append(stream, timedEvent{clock, map[string]any{
			"hook_event_name": "UserPromptSubmit",
			"session_id":      sessionID,
			"prompt":          turn.Prompt,
			"cwd":             cwd,
			"permission_mode": "default",
			"transcript_path": transcriptPath,
		}})

		steps := InjectWorkUnitIntoSteps(turn.Steps, wu)
		for _, s := range steps {
			dur := s.SampleDuration()
			end := tick(time.Duration(dur)*time.Millisecond + 1500*time.Millisecond)
			stream = append(stream, timedEvent{end, map[string]any{
				"hook_event_name": "PostToolUse",
				"session_id":      sessionID,
				"tool_name":       s.Tool,
				"tool_input":      s.Input,
				"tool_response":   s.Response,
				"duration_ms":     float64(dur),
				"cwd":             cwd,
				"permission_mode": "default",
				"transcript_path": transcriptPath,
			}})
		}

		stopAt := tick(3 * time.Second)
		lastMsg := "Done."
		if prURL != "" {
			lastMsg = "Opened " + prURL + "."
		}
		stream = append(stream, timedEvent{stopAt, map[string]any{
			"hook_event_name":        "Stop",
			"session_id":             sessionID,
			"cwd":                    cwd,
			"permission_mode":        "default",
			"transcript_path":        transcriptPath,
			"last_assistant_message": lastMsg,
		}})

		// Per-turn token usage (scaled by model).
		baseInput := int64(math.Round(float64(8000+len(steps)*3500) * scale))
		baseOutput := int64(math.Round(float64(800+len(steps)*400) * scale))
		transcriptEntries = append(transcriptEntries, transcriptEntry{
			prompt: turn.Prompt,
			usage: turnUsage{
				input:       baseInput,
				output:      baseOutput,
				cacheCreate: int64(math.Round(4096 * scale)),
				cacheRead:   int64(math.Round(float64(baseInput) * 0.7)),
			},
		})
	}

	stream = append(stream, timedEvent{tick(2 * time.Second), map[string]any{
		"hook_event_name": "SessionEnd",
		"session_id":      sessionID,
		"cwd":             cwd,
		"transcript_path": transcriptPath,
	}})

	if err := writeMultiTurnTranscript(transcriptPath, model, transcriptEntries); err != nil {
		return fmt.Errorf("writing transcript: %w", err)
	}

	if dryRun {
		fmt.Printf("session %s — %s <%s> — model %s — %s @ %s — PR %s\n",
			sessionID, wu.Persona.Name, wu.Persona.Email, model,
			ownerRepo, wu.Branch, prURL)
		for _, te := range stream {
			b, _ := json.Marshal(te.ev)
			fmt.Printf("  %s  %s\n", te.at.Format(time.RFC3339), string(b))
		}
		fmt.Println()
		return nil
	}

	for _, te := range stream {
		if _, err := pipeline.Process(te.ev, cfg, dataDir, te.at); err != nil {
			return fmt.Errorf("processing %v: %w", te.ev["hook_event_name"], err)
		}
	}

	// Emit matching VCS metrics (GitHub App side) for the same WorkUnit so the
	// join keys align in the demo dataset.
	if err := EmitVCSMetrics(wu, cfg, clock); err != nil {
		fmt.Fprintf(os.Stderr, "warning: VCS metrics: %v\n", err)
	}

	fmt.Printf("sent session %s (%s, %s, %s @ %s, PR %s)\n",
		sessionID, wu.Persona.Name, model, ownerRepo, wu.Branch, prURL)
	return nil
}

type timedEvent struct {
	at time.Time
	ev map[string]any
}

type turnUsage struct {
	input, output, cacheCreate, cacheRead int64
}

type transcriptEntry struct {
	prompt string
	usage  turnUsage
}

// writeMultiTurnTranscript emits a transcript JSONL with one user+assistant
// pair per turn so the pipeline reads the correct per-turn token usage.
func writeMultiTurnTranscript(path, model string, entries []transcriptEntry) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		user := map[string]any{
			"type": "user",
			"message": map[string]any{
				"role":    "user",
				"content": []map[string]any{{"type": "text", "text": e.prompt}},
			},
		}
		if err := enc.Encode(user); err != nil {
			return err
		}
		assistant := map[string]any{
			"type":      "assistant",
			"requestId": "req_" + nonce(),
			"message": map[string]any{
				"role":  "assistant",
				"model": model,
				"usage": map[string]any{
					"input_tokens":                e.usage.input,
					"output_tokens":               e.usage.output,
					"cache_creation_input_tokens": e.usage.cacheCreate,
					"cache_read_input_tokens":     e.usage.cacheRead,
				},
			},
		}
		if err := enc.Encode(assistant); err != nil {
			return err
		}
	}
	return nil
}

// setupPersonaRepo creates a throwaway git repo on the given branch with the
// persona's identity and an origin remote.
func setupPersonaRepo(p Persona, ownerRepo, branch string) (string, string, error) {
	dir, err := os.MkdirTemp("", "dash0-demo-repo-")
	if err != nil {
		return "", "", err
	}
	cmds := [][]string{
		{"init", "-q", "-b", branch},
		{"config", "user.name", p.Name},
		{"config", "user.email", p.Email},
		{"remote", "add", "origin", "https://github.com/" + ownerRepo + ".git"},
		{"commit", "--allow-empty", "-q", "-m", "wip: " + branch},
	}
	for _, c := range cmds {
		if _, err := gitOut(dir, c...); err != nil {
			os.RemoveAll(dir)
			return "", "", fmt.Errorf("git %v: %w", c, err)
		}
	}
	return dir, branch, nil
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(trimNL(out)), nil
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func chdir(dir string) (func(), error) {
	prev, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if err := os.Chdir(dir); err != nil {
		return nil, err
	}
	return func() { _ = os.Chdir(prev) }, nil
}

func nonce() string {
	id, err := otlp.GenerateSpanID()
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return id
}
