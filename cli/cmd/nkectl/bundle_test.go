// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nke/cli/internal/bootstrap/bundle"
)

// exampleBundle is the checked-in example: the template with its three values
// filled, a complete production form.
const exampleBundle = "../../examples/bootstrap-bundle.yaml"

// siteSpecWithBundle writes validSiteSpec plus a bootstrapBundleFile pointing
// at a copy of the example beside it, with an optional registryMirror block
// replacing the example's.
func siteSpecWithBundle(t *testing.T, mirrorBlock string) string {
	t.Helper()
	fixture, err := os.ReadFile(exampleBundle)
	if err != nil {
		t.Fatal(err)
	}
	return siteSpecWithBundleDoc(t, withMirror(string(fixture), mirrorBlock))
}

// siteSpecWithBundleDoc writes validSiteSpec plus a bootstrapBundleFile pointing
// at doc beside it. An empty doc leaves the file absent.
func siteSpecWithBundleDoc(t *testing.T, doc string) string {
	t.Helper()
	dir := t.TempDir()
	if doc != "" {
		if err := os.WriteFile(filepath.Join(dir, "bundle.yaml"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "sitespec.yaml")
	if err := os.WriteFile(path, []byte(validSiteSpec+"\nbootstrapBundleFile: bundle.yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// withMirror replaces the example's registryMirror block with block, and moves
// the example's images onto that block's endpoint. Empty leaves the example
// alone.
func withMirror(bundleDoc, block string) string {
	if block == "" {
		return bundleDoc
	}
	out := regexp.MustCompile(`(?m)^registryMirror:\n(?:  .*\n)*`).ReplaceAllString(bundleDoc, block)
	if m := regexp.MustCompile(`endpoint: "?([^"\n]+)"?`).FindStringSubmatch(block); len(m) == 2 {
		out = strings.ReplaceAll(out, "registry.customer.internal/", m[1]+"/")
	}
	return out
}

const mirrorBlock = `registryMirror:
  endpoint: "mirror.internal:5000"
  ociNamespaces:
    - registry: "docker.io"
      namespace: "dockerhub"
`

// TestRunBootstrapSite_DryRunTable: with a bundle and a mirror, --dry-run
// prints where every artifact would be pulled from, and nothing is run.
func TestRunBootstrapSite_DryRunTable(t *testing.T) {
	var out bytes.Buffer
	runner := &recordingRunner{next: fakeBootstrapRunner{}}
	err := runBootstrapSite(&out, &bootstrapSiteOptions{
		siteSpec:        siteSpecWithBundle(t, mirrorBlock),
		dryRun:          true,
		bootstrapRunner: runner,
	})
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out.String())
	}
	if len(runner.calls) != 0 {
		t.Errorf("dry run executed commands: %v", runner.calls)
	}
	for _, want := range []string{
		"registryMirror: https://mirror.internal:5000",
		"docker.io/temporalio/server:1.30.3 -> mirror.internal:5000/dockerhub/temporalio/server:1.30.3",
		"ghcr.io/spiffe/spire-server:1.15.3 -> mirror.internal:5000/ghcr.io/spiffe/spire-server:1.15.3",
		"cgr.dev                  -> mirror.internal:5000/cgr.dev",
		"oci://mirror.internal:5000/charts",
		"mirror.internal:5000/nke/controller:1.4.0  (built by you; on the mirror)",
		"Dry run: site spec is valid",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry-run output lacks %q:\n%s", want, out.String())
		}
	}
}

// TestRunBootstrapSite_StubErrorBeforeAnyCommand: without --dry-run, a site
// spec that names a bundle stops at the named not-implemented error before
// the container runtime is looked up or any command runs.
func TestRunBootstrapSite_StubErrorBeforeAnyCommand(t *testing.T) {
	setBootstrapEnv(t)
	runner := &recordingRunner{next: fakeBootstrapRunner{}}
	lookedUp := false
	err := runBootstrapSite(io.Discard, &bootstrapSiteOptions{
		siteSpec:        siteSpecWithBundle(t, ""),
		bootstrapRunner: runner,
		cweRunner:       fakeCWERunner{},
		lookPath: func(name string) (string, error) {
			lookedUp = true
			return runtimeOnPath(name)
		},
	})
	if !errors.Is(err, errBundleNotImplemented) {
		t.Fatalf("error = %v, want errBundleNotImplemented", err)
	}
	if lookedUp {
		t.Error("the container runtime was looked up before the stub returned")
	}
	if len(runner.calls) != 0 {
		t.Errorf("commands ran before the stub returned: %v", runner.calls)
	}
}

// TestRunBootstrapSite_ErrorsBeforeAnyCommand: every way the bundle file or
// the flag can be wrong is reported by name, and nothing runs.
func TestRunBootstrapSite_ErrorsBeforeAnyCommand(t *testing.T) {
	fixture, err := os.ReadFile(exampleBundle)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		bundle  string // "" leaves the file absent
		apiKey  string
		public  bool
		wantSub string
	}{
		"missing bundle file": {
			wantSub: "bootstrapBundleFile",
		},
		"blank image with a registry": {
			bundle:  strings.Replace(string(fixture), `controller: "registry.customer.internal/nke/controller:1.4.0"`, `controller: " "`, 1),
			wantSub: "images.controller is required with registryMirror",
		},
		"hostless image with a registry": {
			bundle:  strings.Replace(string(fixture), `controller: "registry.customer.internal/nke/controller:1.4.0"`, `controller: "cwe-controller:dev"`, 1),
			wantSub: `images.controller "cwe-controller:dev" names no registry, but registryMirror is declared`,
		},
		"override of an unknown entry": {
			bundle:  string(fixture) + "overrides:\n  spire.images.nope:\n    reason: r\n    ref: x:1\n",
			wantSub: "overrides.spire.images.nope: the manifest has no entry at that path",
		},
		"empty endpoint": {
			bundle:  withMirror(string(fixture), "registryMirror:\n  endpoint: \"\"\n"),
			wantSub: "registryMirror.endpoint is required",
		},
		"credential over plain http": {
			bundle:  withMirror(string(fixture), "registryMirror:\n  endpoint: m:5000\n  plainHTTP: true\n"),
			apiKey:  "secret-key",
			wantSub: "plainHTTP is true and a registry credential was supplied",
		},
		"--public against a declared registry": {
			bundle:  string(fixture),
			public:  true,
			wantSub: "--public contradicts bootstrap bundle",
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := siteSpecWithBundleDoc(t, tc.bundle)
			var out bytes.Buffer
			runner := &recordingRunner{next: fakeBootstrapRunner{}}
			err := runBootstrapSite(&out, &bootstrapSiteOptions{siteSpec: path, bootstrapRunner: runner, registryAPIKey: tc.apiKey, public: tc.public})
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error does not mention %q:\n%v", tc.wantSub, err)
			}
			if errors.Is(err, errBundleNotImplemented) {
				t.Errorf("the stub was reached; validation should have failed first: %v", err)
			}
			if len(runner.calls) != 0 {
				t.Errorf("commands ran: %v", runner.calls)
			}
			if strings.Contains(err.Error(), "secret-key") || strings.Contains(out.String(), "secret-key") {
				t.Error("the credential appeared in the error or the output")
			}
		})
	}
}

// TestRunBootstrapSite_NoBundleUnchanged: a site spec without the new fields
// takes exactly the path it always did, dry run included, with or without
// --public.
func TestRunBootstrapSite_NoBundleUnchanged(t *testing.T) {
	for name, public := range map[string]bool{"without --public": false, "with --public": true} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := runBootstrapSite(&out, &bootstrapSiteOptions{siteSpec: writeSiteSpec(t, validSiteSpec), dryRun: true, public: public}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "Bootstrap bundle:") {
				t.Errorf("a site spec without a bundle printed the bundle table:\n%s", out.String())
			}
		})
	}
}

// TestRunBootstrapSite_PublicBundleNeedsNoRegistry: a bundle file with no
// registryMirror resolves every artifact publicly under --public and runs
// nothing in a dry run.
func TestRunBootstrapSite_PublicBundleNeedsNoRegistry(t *testing.T) {
	for name, doc := range map[string]string{
		"empty file":  "# nothing declared\n",
		"images only": "images:\n  controller: cwe-controller:dev\n",
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			runner := &recordingRunner{next: fakeBootstrapRunner{}}
			err := runBootstrapSite(&out, &bootstrapSiteOptions{
				siteSpec: siteSpecWithBundleDoc(t, doc), dryRun: true, public: true, bootstrapRunner: runner,
			})
			if err != nil {
				t.Fatalf("dry run: %v\n%s", err, out.String())
			}
			if len(runner.calls) != 0 {
				t.Errorf("dry run executed commands: %v", runner.calls)
			}
			for _, want := range []string{
				"registryMirror: none; every artifact is pulled from the public registry it names",
				"docker.io/temporalio/server:1.30.3  (public)",
				"(built by you; side-loaded)",
				"Dry run: site spec is valid",
			} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("dry-run output lacks %q:\n%s", want, out.String())
				}
			}
			if strings.Contains(out.String(), " -> ") {
				t.Errorf("nothing is redirected without a registry:\n%s", out.String())
			}
		})
	}
}

// TestTemplateBootstrapBundle: the command writes the embedded template, byte
// for byte, with its three values to fill in and no manifest.
func TestTemplateBootstrapBundle(t *testing.T) {
	stdout, _, err := runGenerate(t, "template", "bootstrap-bundle")
	if err != nil {
		t.Fatalf("template bootstrap-bundle: %v", err)
	}
	if stdout != string(bundle.TemplateBytes()) {
		t.Errorf("template bootstrap-bundle output differs from the embedded template:\n%s", stdout)
	}
	if !strings.Contains(stdout, "SPDX-License-Identifier: Apache-2.0") || strings.Count(stdout, `: ""`) != 3 {
		t.Errorf("template is missing the header or does not have three values to fill in:\n%s", stdout)
	}
	if strings.Contains(stdout, "\ncomponents:") {
		t.Errorf("the bootstrap bundle carries the manifest; that is nkectl's, printed by get bootstrap-manifest:\n%s", stdout)
	}
}

// fillTemplate fills the template's three values for a registry and the two
// images pushed to it.
func fillTemplate(registry string) string {
	doc := string(bundle.TemplateBytes())
	doc = strings.Replace(doc, `endpoint: ""`, `endpoint: "`+registry+`"`, 1)
	doc = strings.Replace(doc, `controller: ""`, `controller: "`+registry+`/nke/controller:1.4.0"`, 1)
	doc = strings.Replace(doc, `provisionedProvider: ""`, `provisionedProvider: "`+registry+`/nke/capp:1.0.0"`, 1)
	return doc
}

// TestValidateBootstrapBundle: the command runs the checks bootstrap site runs,
// names the fields that are wrong, and prints the resolution when all is well.
func TestValidateBootstrapBundle(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	if err := os.WriteFile(good, []byte(fillTemplate("registry.customer.internal")), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runGenerate(t, "validate", "bootstrap-bundle", "-f", good)
	if err != nil {
		t.Fatalf("validate bootstrap-bundle on a filled template: %v", err)
	}
	for _, want := range []string{
		"registryMirror: https://registry.customer.internal",
		"-> registry.customer.internal/ghcr.io/spiffe/spire-server:1.15.3",
		"registry.customer.internal/nke/controller:1.4.0  (built by you; on the mirror)",
		"bootstrap bundle " + good + " is valid.",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("validate output lacks %q:\n%s", want, stdout)
		}
	}

	unedited := filepath.Join(dir, "unedited.yaml")
	if err := os.WriteFile(unedited, bundle.TemplateBytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = runGenerate(t, "validate", "bootstrap-bundle", "-f", unedited)
	if err == nil {
		t.Fatal("validate accepted the unedited template")
	}
	for _, want := range []string{"registryMirror.endpoint is required", "images.controller is required with registryMirror", "images.provisionedProvider is required with registryMirror"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validate error does not name %q:\n%v", want, err)
		}
	}
	if _, _, err := runGenerate(t, "validate", "bootstrap-bundle"); err == nil {
		t.Error("validate without -f must fail")
	}
}

// TestGetBootstrapManifest_Golden: the manifest is the embedded manifest,
// byte for byte, and nothing in it is a field to fill in. It needs no site.
func TestGetBootstrapManifest_Golden(t *testing.T) {
	stdout, _, err := runGenerate(t, "get", "bootstrap-manifest")
	if err != nil {
		t.Fatalf("get bootstrap-manifest: %v", err)
	}
	if stdout != string(bundle.ManifestBytes()) {
		t.Errorf("get bootstrap-manifest output differs from the embedded manifest:\n%s", stdout)
	}
	for _, want := range []string{"components:", "ghcr.io/spiffe/spire-server:1.15.3", "sha256:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("manifest lacks %q:\n%s", want, stdout)
		}
	}
	// The registry is the bundle file's to name, not the manifest's.
	if strings.Contains(stdout, "registryMirror:") {
		t.Errorf("the manifest declares a registry; that belongs in the bundle file:\n%s", stdout)
	}
	if strings.Contains(stdout, `: ""`) {
		t.Errorf("the manifest has a field to fill in; it is nkectl's manifest, not a scaffold:\n%s", stdout)
	}
}

// TestTemplateBootstrapBundle_ValidatesInBootstrap is the end-to-end claim:
// the template with its three values filled, bootstrap site accepts and routes
// to the declared registry.
func TestTemplateBootstrapBundle_ValidatesInBootstrap(t *testing.T) {
	filled := fillTemplate("registry.customer.internal")
	var out bytes.Buffer
	if err := runBootstrapSite(&out, &bootstrapSiteOptions{siteSpec: siteSpecWithBundleDoc(t, filled), dryRun: true}); err != nil {
		t.Fatalf("bootstrap site --dry-run rejected the generated bundle: %v", err)
	}
	if !strings.Contains(out.String(), "registryMirror: https://registry.customer.internal") ||
		!strings.Contains(out.String(), "-> registry.customer.internal/ghcr.io/spiffe/spire-server:1.15.3") {
		t.Errorf("dry-run table does not route everything to the declared registry:\n%s", out.String())
	}
}
