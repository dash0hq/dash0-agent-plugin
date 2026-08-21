// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package config reads the per-agent configuration file,
// dash0-agent-plugin.local.md, whose settings live in YAML frontmatter.
//
// The file is read on each lookup rather than memoized. A hook process is
// short-lived and the file is a few lines, so the syscalls do not matter, and
// keeping it stateless means tests can rewrite the file between assertions.
package config

import (
	"os"
	"strings"
)

// Name is the file each agent looks for inside its own configuration directory.
const Name = "dash0-agent-plugin.local.md"

// bom is the UTF-8 byte-order mark. PowerShell 5.1 writes one by default, and
// a value parsed with it still attached silently fails to match anything.
const bom = "\ufeff"

// File holds the settings parsed from one configuration file. A File is valid
// when no file was found: every lookup returns empty, which leaves the plugin
// unconfigured and inactive.
type File struct {
	Path   string
	values map[string]string
}

// Load returns the first file that exists among paths, parsed. A missing file
// is not an error: the plugin then runs unconfigured.
//
// The first existing file wins outright and values are never merged across
// paths. So a project file that omits a key does not inherit that key from the global
// file.
func Load(paths ...string) *File {
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		return &File{Path: path, values: parse(string(content))}
	}
	return &File{values: map[string]string{}}
}

// Get returns the value for key, or empty when the key is absent.
func (f *File) Get(key string) string {
	return f.values[key]
}

// Enabled reports whether the file permits the plugin to run. Only an explicit
// "false" disables it: a file without an enabled key, and an absent file, both
// mean enabled.
func (f *File) Enabled() bool {
	return f.values["enabled"] != "false"
}

// parse reads the YAML frontmatter, meaning the lines between the first `---`
// and the next one. Anything after the closing marker is prose and ignored.
//
// Three tolerances are deliberate, because the file is written by a shell
// installer on POSIX and by PowerShell on Windows, and read here:
//
//   - a leading UTF-8 BOM is skipped, since PowerShell 5.1's Set-Content writes
//     one by default;
//   - a trailing CR is stripped, so a CRLF file does not yield values ending in
//     "\r" — which would corrupt an auth token in a way nothing reports;
//   - surrounding double quotes are stripped, matching what the wrappers did.
//
// The first occurrence of a key wins.
func parse(content string) map[string]string {
	values := map[string]string{}
	inFrontmatter := false

	for _, raw := range strings.Split(strings.TrimPrefix(content, bom), "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "---" {
			if inFrontmatter {
				break
			}
			inFrontmatter = true
			continue
		}
		if !inFrontmatter {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, seen := values[key]; seen {
			continue
		}
		values[key] = unquote(strings.TrimSpace(value))
	}
	return values
}

// unquote strips one layer of surrounding double quotes, the only quoting the
// installers and the configure skills emit.
func unquote(value string) string {
	if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		return value[1 : len(value)-1]
	}
	return value
}
