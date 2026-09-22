// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sitespec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeBundleBeside writes an (unvalidated) bundle file next to a site spec so
// the load-time existence check passes. Its content is the command's concern.
func writeBundleBeside(t *testing.T, siteSpecPath string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(siteSpecPath), "bundle.yaml")
	if err := os.WriteFile(path, []byte("images:\n  controller: x:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_BundleFileRelativeToSiteSpecDir(t *testing.T) {
	path := writeTemp(t, validYAML+"\nbootstrapBundleFile: bundle.yaml\n")
	writeBundleBeside(t, path)

	inv, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if inv.BootstrapBundleFile != "bundle.yaml" {
		t.Errorf("BootstrapBundleFile = %q; the loader must not rewrite what the file says", inv.BootstrapBundleFile)
	}
	if got, want := inv.BootstrapBundlePath(path), filepath.Join(filepath.Dir(path), "bundle.yaml"); got != want {
		t.Errorf("BootstrapBundlePath = %q, want %q", got, want)
	}
}

func TestBundlePath_AbsoluteNotRejoined(t *testing.T) {
	inv := &SiteSpec{BootstrapBundleFile: "/srv/nke/bundle.yaml"}
	if got := inv.BootstrapBundlePath("/elsewhere/sitespec.yaml"); got != "/srv/nke/bundle.yaml" {
		t.Errorf("BootstrapBundlePath = %q", got)
	}
	if got := (&SiteSpec{}).BootstrapBundlePath("/elsewhere/sitespec.yaml"); got != "" {
		t.Errorf("BundlePath with no bundle = %q, want empty", got)
	}
}

func TestLoad_MissingBundleFileIsReportedWithTheField(t *testing.T) {
	path := writeTemp(t, validYAML+"\nbootstrapBundleFile: nope.yaml\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted a bootstrapBundleFile that does not exist")
	}
	if !strings.Contains(err.Error(), pathBundleFile) || !strings.Contains(err.Error(), "nope.yaml") {
		t.Errorf("error does not name the field and the resolved path: %v", err)
	}
}

func TestLoad_BundleFileMustNotBeADirectory(t *testing.T) {
	path := writeTemp(t, validYAML+"\nbootstrapBundleFile: .\n")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("Load error = %v, want a directory complaint", err)
	}
}

// TestLoad_RegistryMirrorIsNotASiteSpecField: the mirror lives in the bundle.
// A block here is a typo-class error like any other unknown field.
func TestLoad_RegistryMirrorIsNotASiteSpecField(t *testing.T) {
	_, err := Load(writeTemp(t, validYAML+"\nregistryMirror:\n  endpoint: m:5000\n"))
	// viper reports the key lower-cased.
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "registrymirror") {
		t.Errorf("Load error = %v, want it to reject registryMirror as unknown", err)
	}
}

func TestLoad_BundleFileEnvBinding(t *testing.T) {
	path := writeTemp(t, validYAML)
	bundlePath := writeBundleBeside(t, path)
	t.Setenv("BOOTSTRAP_BUNDLE_FILE", bundlePath)

	inv, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if inv.BootstrapBundleFile != bundlePath {
		t.Errorf("BootstrapBundleFile = %q", inv.BootstrapBundleFile)
	}
}

// TestBundleFileRoundTrip: --set bootstrapBundleFile reaches the rendered file in
// place of its commented example, between spire's region and registry:.
func TestBundleFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	siteSpecPath := filepath.Join(dir, "sitespec.yaml")
	bundlePath := writeBundleBeside(t, siteSpecPath)
	built, err := Build("my-tier0-site", threeNodes(), []string{"bootstrapBundleFile=" + bundlePath})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rendered, err := built.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := os.WriteFile(siteSpecPath, rendered, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(siteSpecPath)
	if err != nil {
		t.Fatalf("the rendered site spec does not load: %v\n%s", err, rendered)
	}
	if diff := specDiff(loaded, built); diff != "" {
		t.Errorf("round trip changed the site spec%s", diff)
	}
	out := string(rendered)
	if !strings.Contains(out, "Bootstrap bundle. Optional.") {
		t.Errorf("rendered file lost the bundle prose:\n%s", out)
	}
	if strings.Contains(out, "# bootstrapBundleFile:") {
		t.Errorf("the commented example survived alongside the real value:\n%s", out)
	}
	for _, order := range [][2]string{
		{"cluster:", "\nbootstrapBundleFile:"},
		{"\nbootstrapBundleFile:", "\nregistry:"},
	} {
		before, after := strings.Index(out, order[0]), strings.Index(out, order[1])
		if before < 0 || after < 0 || before > after {
			t.Errorf("%q should appear before %q:\n%s", order[0], order[1], out)
		}
	}
}

// TestRenderOmitsUnsetBundleFile extends the optional-field rule: absent stays
// a comment.
func TestRenderOmitsUnsetBundleFile(t *testing.T) {
	built, err := Build("my-tier0-site", threeNodes(), nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rendered, err := built.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if line := uncommentedLineFor(string(rendered), "bootstrapBundleFile"); line != "" {
		t.Errorf("rendered site spec sets bootstrapBundleFile as %q; an unset optional field should stay a comment", line)
	}
}
