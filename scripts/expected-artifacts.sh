#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Every binary name a release must contain, one per line, derived from
# .goreleaser.yaml rather than counted. A count is the thing that rots: adding
# Windows turned "expected 16" into a number nobody updated, and a count can
# never say WHICH target went missing.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# One block per `builds:` entry — the binary name template and its goos/goarch.
# awk, because the runners have no yq and a YAML parser for three fields is more
# to go wrong than the parse. Each `- id:` flushes the block before it.
awk '
  function flush(   i, j, n) {
    if (name == "" || no == 0 || na == 0) return
    for (i = 0; i < no; i++) for (j = 0; j < na; j++) {
      n = name
      gsub(/{{ *\.Os *}}/, os[i], n)
      gsub(/{{ *\.Arch *}}/, arch[j], n)
      print n (os[i] == "windows" ? ".exe" : "")
    }
    name = ""
  }
  /^builds:/       { inb = 1; next }
  /^[a-z]/         { flush(); inb = 0 }
  !inb             { next }
  /^  - id:/       { flush(); delete os; delete arch; no = 0; na = 0; sec = ""; next }
  /^    binary:/   { sub(/^ *binary: */, ""); gsub(/"/, ""); name = $0; sec = ""; next }
  /^    goos:/     { sec = "os";   next }
  /^    goarch:/   { sec = "arch"; next }
  /^      - /      { if (sec == "os") os[no++] = $2; else if (sec == "arch") arch[na++] = $2; next }
  /^    [a-z_]+:/  { sec = "" }
  END              { flush() }
' .goreleaser.yaml
