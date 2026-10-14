// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package redact

import (
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeValue stands in for a credential. It is deliberately plain.
const fakeValue = "aaaa-bbbb-cccc"

func TestText(t *testing.T) {
	RegisterSecret(fakeValue)
	RegisterSecret("") // ignored: masking "" would mangle every string

	for _, tc := range []struct {
		name, in, want string
	}{
		{"a registered value is masked", "rejected " + fakeValue + " twice " + fakeValue, "rejected <redacted> twice <redacted>"},
		{"a password in a database URL is masked without being registered", "postgres://u:other-pw@h:5432/db", "postgres://u:<redacted>@h:5432/db"},
		{"the postgresql scheme too", "postgresql://u:other-pw@h/db?sslmode=disable", "postgresql://u:<redacted>@h/db?sslmode=disable"},
		{"text with no credential is unchanged", "installing postgres in namespace postgres", "installing postgres in namespace postgres"},
		{"a URL with no password is unchanged", "postgres://h:5432/db", "postgres://h:5432/db"},
		{"the placeholder is left alone", "postgres://u:<redacted>@h/db", "postgres://u:<redacted>@h/db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.in); got != tc.want {
				t.Errorf("Text(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// root skips a case that needs a permission check to fail, which root passes.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: file permissions do not stop it")
	}
}

func TestTree_ScrubsEveryFileUnderTheDirectory(t *testing.T) {
	RegisterSecret(fakeValue)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), "x "+fakeValue+" y")
	write(t, filepath.Join(dir, "deep", "er", "b.json"), `{"u":"postgres://u:`+fakeValue+`@h/db"}`)
	write(t, filepath.Join(dir, "clean.txt"), "nothing here")

	if failed := Tree(dir); len(failed) != 0 {
		t.Fatalf("Tree reported %v", failed)
	}
	for _, f := range []string{"a.txt", "deep/er/b.json"} {
		if got := read(t, filepath.Join(dir, f)); strings.Contains(got, fakeValue) {
			t.Errorf("%s still holds the value: %s", f, got)
		}
	}
	if got := read(t, filepath.Join(dir, "clean.txt")); got != "nothing here" {
		t.Errorf("a clean file changed: %q", got)
	}
}

func TestTree_ReportsAFileItCannotRewrite(t *testing.T) {
	skipIfRoot(t)
	RegisterSecret(fakeValue)
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked.txt")
	write(t, locked, fakeValue)
	if err := os.Chmod(locked, 0o400); err != nil {
		t.Fatal(err)
	}

	failed := Tree(dir)
	if len(failed) != 1 || failed[0] != locked {
		t.Errorf("Tree = %v, want just %s", failed, locked)
	}
}

func TestTree_ReportsADirectoryItCannotRead(t *testing.T) {
	skipIfRoot(t)
	dir := t.TempDir()
	hidden := filepath.Join(dir, "hidden")
	write(t, filepath.Join(hidden, "x.txt"), fakeValue)
	if err := os.Chmod(hidden, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(hidden, 0o700) })

	failed := Tree(dir)
	if len(failed) != 1 || failed[0] != hidden {
		t.Errorf("Tree = %v, want just %s", failed, hidden)
	}
}

// WalkDir does not follow a symlink, the root included. Without resolving it
// first, a symlinked directory was skipped with nothing scrubbed and nothing
// reported.
func TestTree_FollowsASymlinkedRoot(t *testing.T) {
	RegisterSecret(fakeValue)
	target := filepath.Join(t.TempDir(), "real")
	write(t, filepath.Join(target, "sub", "f.txt"), "x "+fakeValue)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}

	if failed := Tree(link); len(failed) != 0 {
		t.Fatalf("Tree reported %v", failed)
	}
	if got := read(t, filepath.Join(target, "sub", "f.txt")); strings.Contains(got, fakeValue) {
		t.Errorf("the file behind the symlink still holds the value: %s", got)
	}
}

func TestTree_ReportsARootThatIsNotADirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file.txt")
	write(t, file, "x")
	missing := filepath.Join(t.TempDir(), "missing")

	for _, root := range []string{file, missing} {
		if failed := Tree(root); len(failed) != 1 || failed[0] != root {
			t.Errorf("Tree(%s) = %v, want just the root", root, failed)
		}
	}
}

func TestVerify_FindsACredentialInEveryForm(t *testing.T) {
	const (
		plain   = fakeValue
		special = "pa ss/word+1234" // URL-escapes differently as a query and as a path
		urlSafe = "???>>>???>>>"    // base64 of it uses + and /
	)
	for _, v := range []string{plain, special, urlSafe} {
		RegisterSecret(v)
	}
	b64 := base64.StdEncoding.EncodeToString
	b64url := base64.URLEncoding.EncodeToString

	for _, tc := range []struct {
		name, content, form string
	}{
		{"raw", "env DB=" + plain, "raw"},
		{"base64 of the whole value", "data: " + b64([]byte(plain)), "base64"},
		{"base64 of a longer string, value at offset 0", "data: " + b64([]byte(plain+"@host/db")), "base64"},
		{"base64 of a longer string, value at offset 1", "data: " + b64([]byte("x"+plain+"y")), "base64"},
		{"base64 of a longer string, value at offset 2", "data: " + b64([]byte("xy"+plain+"!")), "base64"},
		{"base64 of a URL holding the value", "data: " + b64([]byte("postgresql://u:"+plain+"@h/db")), "base64"},
		{"URL-safe base64", "data: " + b64url([]byte("pre-"+urlSafe+"-post")), "base64"},
		{"query-escaped", "dsn=" + url.QueryEscape(special), "url-encoded"},
		{"path-escaped", "dsn=" + url.PathEscape(special), "url-encoded"},
		{"a password in a database URL nobody registered", "postgres://u:other-pw@h/db", "database URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "sub", "f.txt")
			write(t, path, tc.content)

			found := Verify(dir)
			if len(found) != 1 || found[0].Path != path || found[0].Form != tc.form {
				t.Errorf("Verify = %v, want %s (%s)", found, path, tc.form)
			}
			if got := read(t, path); got != tc.content {
				t.Errorf("Verify changed the file: %q", got)
			}
			for _, f := range found {
				if strings.Contains(f.String(), plain) || strings.Contains(f.String(), special) {
					t.Errorf("a finding carries the credential: %s", f)
				}
			}
		})
	}
}

func TestVerify_PassesCleanOutput(t *testing.T) {
	RegisterSecret(fakeValue)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), "installing postgres, then temporal")
	write(t, filepath.Join(dir, "b.json"), `{"env":[{"name":"DATABASE_URL","valueFrom":{"secretKeyRef":{"name":"c-db"}}}]}`)
	write(t, filepath.Join(dir, "c.txt"), "postgres://u:<redacted>@h/db")
	write(t, filepath.Join(dir, "d.txt"), "short base64 look-alikes: YWFh Zm9v")

	if found := Verify(dir); len(found) != 0 {
		t.Errorf("Verify = %v, want nothing", found)
	}
}

// A value too short to search for in encoded form would match unrelated text.
func TestVerify_IgnoresEncodedFormsOfAShortValue(t *testing.T) {
	RegisterSecret("abc")
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), base64.StdEncoding.EncodeToString([]byte("abc")))

	if found := Verify(dir); len(found) != 0 {
		t.Errorf("Verify = %v, want nothing", found)
	}
}

func TestVerify_ReportsWhatItCannotRead(t *testing.T) {
	skipIfRoot(t)
	dir := t.TempDir()
	hidden := filepath.Join(dir, "hidden")
	write(t, filepath.Join(hidden, "x.txt"), "x")
	locked := filepath.Join(dir, "locked.txt")
	write(t, locked, "x")
	for _, p := range []string{hidden, locked} {
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(hidden, 0o700) })

	found := Verify(dir)
	if len(found) != 2 {
		t.Fatalf("Verify = %v, want the directory and the file", found)
	}
	for _, f := range found {
		if f.Form != "unreadable" {
			t.Errorf("%s: form = %q, want unreadable", f.Path, f.Form)
		}
	}
}

func TestVerify_FollowsASymlinkedRootAndReportsANonDirectory(t *testing.T) {
	RegisterSecret(fakeValue)
	target := filepath.Join(t.TempDir(), "real")
	write(t, filepath.Join(target, "f.txt"), fakeValue)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	if found := Verify(link); len(found) != 1 || found[0].Form != "raw" {
		t.Errorf("Verify(symlink) = %v, want the file behind it", found)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	if found := Verify(missing); len(found) != 1 || found[0].Path != missing || found[0].Form != "unreadable" {
		t.Errorf("Verify(missing) = %v, want the root reported", found)
	}
}
