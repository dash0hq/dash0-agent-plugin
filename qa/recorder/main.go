// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Command recorder captures one hook invocation exactly as the plugin received
// it, and exits. It is registered as a second hook handler alongside the real
// plugin, so a QA session exercises the shipped install unchanged while this
// records what that install was fed.
//
// Two artifacts per invocation:
//
//   - events/<seq>-<HookEvent>.json — the stdin payload, byte for byte
//   - transcripts/<sha256>.jsonl    — the transcript file as it stood at that
//     moment, content-addressed so repeated identical reads cost one copy
//
// The pair is the pipeline's entire input: internal/pipeline reads the event and
// internal/transcript reads the file the event points at. A recorded pair can
// therefore be replayed as a unit-test fixture, and an expectation can be
// computed from it without the plugin's involvement.
//
// It writes nothing to stdout. A hook that emits output can change the host's
// behaviour, and this one must not perturb the session it observes.
//
// Usage (as a hook command):
//
//	QA_RECORD_DIR=qa/runs/<id>/record qa/runs/<id>/recorder [<eventName>]
//
// The event-name argument is for Copilot, whose camelCase payloads carry no
// event-name field at all — the host passes it as an argv, and the plugin's own
// entrypoint reads it the same way. Claude Code and Codex both put
// hook_event_name in the payload, so they pass no argument and the payload wins.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// record is one line of index.jsonl. Everything needed to replay the invocation,
// plus what it would take to notice that a replay is not faithful.
type record struct {
	Seq            int64  `json:"seq"`
	RecordedAt     string `json:"recorded_at"`
	HookEvent      string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	Cwd            string `json:"cwd"`
	EventFile      string `json:"event_file"`
	EventBytes     int    `json:"event_bytes"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	TranscriptSHA  string `json:"transcript_sha256,omitempty"`
	TranscriptSize int64  `json:"transcript_bytes,omitempty"`
	// TranscriptAbsent records that the path in the payload does not exist yet.
	// Claude Code names the transcript before it writes it, so SessionStart,
	// InstructionsLoaded, and UserPromptSubmit all arrive with a path to nothing.
	// internal/transcript reads the same path and sees the same absence, so this
	// is part of a faithful fixture rather than a recorder failure.
	TranscriptAbsent bool   `json:"transcript_absent,omitempty"`
	TranscriptErr    string `json:"transcript_error,omitempty"`
	SubagentPath     string `json:"agent_transcript_path,omitempty"`
	SubagentSHA      string `json:"agent_transcript_sha256,omitempty"`
}

func main() {
	// Any failure here is silent to the host on purpose: a recorder that breaks
	// a hook would change the behaviour it exists to observe. Failures land in
	// errors.log instead.
	if err := run(); err != nil {
		if dir := os.Getenv("QA_RECORD_DIR"); dir != "" {
			f, ferr := os.OpenFile(filepath.Join(dir, "errors.log"),
				os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if ferr == nil {
				fmt.Fprintf(f, "%s %v\n", time.Now().UTC().Format(time.RFC3339Nano), err)
				_ = f.Close()
			}
		}
	}
}

func run() error {
	dir := os.Getenv("QA_RECORD_DIR")
	if dir == "" {
		return fmt.Errorf("QA_RECORD_DIR is not set")
	}
	// The nanosecond clock is the sequence: hooks for one event can run
	// concurrently, so a counter would need a lock across processes, and the
	// order that matters is wall-clock order anyway.
	seq := time.Now().UTC().UnixNano()

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	var event map[string]any
	// A payload that does not parse is still recorded. It is the most
	// interesting thing that can arrive here.
	_ = json.Unmarshal(raw, &event)

	hookEvent, _ := event["hook_event_name"].(string)
	// The payload wins when it names the event, so a Claude or Codex recording is
	// exactly what it was before. Copilot names it only in argv.
	if hookEvent == "" && len(os.Args) > 1 {
		hookEvent = os.Args[1]
	}
	if hookEvent == "" {
		hookEvent = "unknown"
	}
	sessionID, _ := event["session_id"].(string)
	if sessionID == "" {
		// Copilot's camelCase payloads carry sessionId. Reading it here rather
		// than in the comparison keeps the index one shape across runtimes: every
		// consumer filters on session_id, and a Copilot recording with an empty
		// one reads as a total recording failure.
		sessionID, _ = event["sessionId"].(string)
	}
	cwd, _ := event["cwd"].(string)

	for _, sub := range []string{"events", "transcripts"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}

	rec := record{
		Seq:        seq,
		RecordedAt: time.Now().UTC().Format(time.RFC3339Nano),
		HookEvent:  hookEvent,
		SessionID:  sessionID,
		Cwd:        cwd,
		EventFile:  filepath.Join("events", fmt.Sprintf("%d-%s.json", seq, hookEvent)),
		EventBytes: len(raw),
	}
	if err := os.WriteFile(filepath.Join(dir, rec.EventFile), raw, 0o644); err != nil {
		return fmt.Errorf("writing event: %w", err)
	}

	transcriptPath, _ := event["transcript_path"].(string)
	if transcriptPath == "" {
		// Copilot's transcriptPath points at its own events.jsonl rather than a
		// Claude transcript, and the plugin deliberately drops it. Snapshotting it
		// anyway costs one content-addressed copy and gives a Copilot run the same
		// per-hook "what the session looked like then" record the others have.
		transcriptPath, _ = event["transcriptPath"].(string)
	}
	if path := transcriptPath; path != "" {
		rec.TranscriptPath = path
		sha, size, err := snapshot(path, filepath.Join(dir, "transcripts"))
		switch {
		case err == nil:
			rec.TranscriptSHA, rec.TranscriptSize = sha, size
		case os.IsNotExist(err):
			rec.TranscriptAbsent = true
		default:
			rec.TranscriptErr = err.Error()
		}
	}
	// A sub-agent event carries its own transcript, and the sub-agent's usage
	// lives only there. Missing it is how invoke_agent spans go unexplained.
	if path, _ := event["agent_transcript_path"].(string); path != "" {
		rec.SubagentPath = path
		if sha, _, err := snapshot(path, filepath.Join(dir, "transcripts")); err == nil {
			rec.SubagentSHA = sha
		}
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	index, err := os.OpenFile(filepath.Join(dir, "index.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	// One write of one line: O_APPEND makes it atomic against the other hook
	// processes writing the same file.
	_, writeErr := index.Write(append(line, '\n'))
	closeErr := index.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// snapshot copies path into dir under the hex sha256 of its contents, and
// returns that digest and the byte count. An identical file already present is
// left alone, so a session whose transcript did not change between two hooks
// stores one copy.
func snapshot(path, dir string) (string, int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	dest := filepath.Join(dir, sha+".jsonl")
	if _, err := os.Stat(dest); err == nil {
		return sha, int64(len(data)), nil
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return "", 0, err
	}
	return sha, int64(len(data)), nil
}
