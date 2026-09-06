// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import "strings"

// Level is how much of one privacy dimension's content reaches the exporter.
type Level int

// The three privacy levels. LevelLimited is the zero value on purpose: a Config
// built without setting a dimension must behave like the shipped default rather
// than silently exporting nothing or everything.
const (
	// LevelLimited exports a dimension's structural and derived attributes with
	// its content redacted.
	LevelLimited Level = iota
	// LevelDisabled exports nothing for the dimension — the attribute, or the
	// whole span, is omitted.
	LevelDisabled
	// LevelFull exports the dimension's content, subject to MaxContentBytes.
	LevelFull
)

// String returns the configuration spelling of the level.
func (l Level) String() string {
	switch l {
	case LevelDisabled:
		return "disabled"
	case LevelFull:
		return "full"
	default:
		return "limited"
	}
}

// ParseLevel maps a configured value to a Level, reporting whether it was
// recognized. An unrecognized value — including the empty string — yields
// LevelLimited and never LevelFull, so a typo cannot widen what is exported.
func ParseLevel(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "disabled":
		return LevelDisabled, true
	case "limited":
		return LevelLimited, true
	case "full":
		return LevelFull, true
	default:
		return LevelLimited, false
	}
}
