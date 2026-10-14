// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package redact masks credentials in text and files that nvkectl prints or saves.
//
// Credentials are registered as the process generates them, so every caller
// masks the same values even when they are in different engines.
package redact

import (
	"encoding/base64"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Placeholder replaces a credential wherever it would otherwise be printed or saved.
const Placeholder = "<redacted>"

// secrets holds the credentials this process generated. It is process-wide
// because the engine that installs the releases is not the one that later
// collects diagnostics, and both must mask the same values. The process is a
// short-lived CLI, so the set is never pruned.
var secrets = struct {
	sync.Mutex
	values []string
}{}

// RegisterSecret records a credential so that Text and Tree mask it.
// An empty value is ignored: masking "" would mangle every string.
func RegisterSecret(value string) {
	if value == "" {
		return
	}
	secrets.Lock()
	defer secrets.Unlock()
	for _, v := range secrets.values {
		if v == value {
			return
		}
	}
	secrets.values = append(secrets.values, value)
}

// connectionURLPassword matches the password in scheme://user:password@host.
// Pod specs carry the database URL this way.
var connectionURLPassword = regexp.MustCompile(`(postgres(?:ql)?://[^:/\s@"]+:)[^@/\s"]+(@)`)

// Text masks every registered credential and any password in a connection URL
// in text.
func Text(text string) string {
	secrets.Lock()
	values := append([]string(nil), secrets.values...)
	secrets.Unlock()
	for _, v := range values {
		text = strings.ReplaceAll(text, v, Placeholder)
	}
	return connectionURLPassword.ReplaceAllString(text, "${1}"+Placeholder+"${2}")
}

// Tree rewrites, in place, every file under dir that holds a credential.
// It is best-effort and returns the paths it could not scrub: files it could
// not rewrite and directories it could not read.
func Tree(dir string) []string {
	var failed []string
	unreachable := walk(dir, func(path, named string, d fs.DirEntry) {
		body, err := os.ReadFile(path)
		if err != nil {
			failed = append(failed, named)
			return
		}
		if clean := Text(string(body)); clean != string(body) {
			info, statErr := d.Info()
			if statErr != nil || os.WriteFile(path, []byte(clean), info.Mode().Perm()) != nil {
				failed = append(failed, named)
			}
		}
	})
	return append(failed, unreachable...)
}

// Finding is a credential that Verify found, or a path it could not check. It
// names where and in what form, and never carries the credential.
type Finding struct {
	Path string
	// Form is "raw", "base64", "url-encoded", "database URL" or "unreadable".
	Form string
}

func (f Finding) String() string { return fmt.Sprintf("%s (%s)", f.Path, f.Form) }

// Verify reports every file under dir that holds a registered credential, in
// its raw, base64 or URL-escaped form, or a password in a database URL other
// than Placeholder. A file or directory it cannot read is reported too, since
// it cannot be checked. It changes nothing, and returns nil for a clean dir.
func Verify(dir string) []Finding {
	var found []Finding
	unreachable := walk(dir, func(path, named string, _ fs.DirEntry) {
		body, err := os.ReadFile(path)
		if err != nil {
			found = append(found, Finding{Path: named, Form: "unreadable"})
			return
		}
		if form := heldForm(string(body)); form != "" {
			found = append(found, Finding{Path: named, Form: form})
		}
	})
	for _, path := range unreachable {
		found = append(found, Finding{Path: path, Form: "unreadable"})
	}
	return found
}

// heldForm returns how text holds a credential, or "" when it does not.
func heldForm(text string) string {
	secrets.Lock()
	values := append([]string(nil), secrets.values...)
	secrets.Unlock()
	for _, v := range values {
		if strings.Contains(text, v) {
			return "raw"
		}
	}
	for _, v := range values {
		for _, f := range base64Forms(v) {
			if strings.Contains(text, f) {
				return "base64"
			}
		}
		for _, f := range urlForms(v) {
			if strings.Contains(text, f) {
				return "url-encoded"
			}
		}
	}
	if connectionURLPassword.ReplaceAllString(text, "${1}"+Placeholder+"${2}") != text {
		return "database URL"
	}
	return ""
}

// minEncodedLen is the shortest value whose encoded forms are searched for. A
// shorter one would match unrelated text.
const minEncodedLen = 8

// base64Forms returns the base64 text that value encodes to wherever it sits in
// a longer string. Which characters it produces depends on its offset mod 3, so
// all three are covered, and in both the standard and URL-safe alphabets.
func base64Forms(value string) []string {
	if len(value) < minEncodedLen {
		return nil
	}
	urlSafe := strings.NewReplacer("+", "-", "/", "_")
	var forms []string
	for skew := 0; skew < 3; skew++ {
		enc := base64.StdEncoding.EncodeToString(append(make([]byte, skew), value...))
		// Keep the characters made only of value bytes: not the ones that share
		// bits with the skew bytes before it, nor with whatever follows.
		first, last := (skew*8+5)/6, (skew+len(value))*8/6
		if first < last {
			forms = append(forms, enc[first:last], urlSafe.Replace(enc[first:last]))
		}
	}
	return forms
}

// urlForms returns the percent-encoded spellings of value that differ from it.
func urlForms(value string) []string {
	if len(value) < minEncodedLen {
		return nil
	}
	var forms []string
	for _, f := range []string{url.QueryEscape(value), url.PathEscape(value)} {
		if f != value {
			forms = append(forms, f)
		}
	}
	return forms
}

// walk calls visit for each regular file under dir, with the path to open and
// the same path spelled under dir as the caller gave it. It returns the paths it
// could not reach, such as an unreadable directory, or dir itself when it does
// not resolve to a directory.
func walk(dir string, visit func(path, named string, d fs.DirEntry)) (unreachable []string) {
	// WalkDir does not follow a symlink, the root included, so a symlinked dir
	// would be skipped with nothing visited and nothing reported.
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return []string{dir}
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return []string{dir}
	}
	named := func(path string) string {
		if rel, err := filepath.Rel(root, path); err == nil {
			return filepath.Join(dir, rel)
		}
		return path
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory hides every file under it.
			unreachable = append(unreachable, named(path))
			return nil
		}
		if d.Type().IsRegular() {
			visit(path, named(path), d)
		}
		return nil
	})
	return unreachable
}
