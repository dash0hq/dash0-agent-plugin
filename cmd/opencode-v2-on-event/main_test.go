// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOversizedLineIsDroppedAndReadingContinues(t *testing.T) {
	var got []string
	input := "first\r\n" + strings.Repeat("x", 200_000) + "\n\nsecond\n" + strings.Repeat("y", 11) + "\nlast"
	require.NoError(t, eachLine(strings.NewReader(input), 10, func(line []byte) { got = append(got, string(line)) }))
	assert.Equal(t, []string{"first", "second", "last"}, got)
}

// The limit is on the payload, whichever terminator follows it.
func TestLineLimitExcludesTheTerminator(t *testing.T) {
	var got []string
	input := "0123456789\r\n0123456789\nABCDEFGHIJK\r\nabcdefghijk\n"
	require.NoError(t, eachLine(strings.NewReader(input), 10, func(line []byte) { got = append(got, string(line)) }))
	assert.Equal(t, []string{"0123456789", "0123456789"}, got)
}
