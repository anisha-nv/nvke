// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package bundle

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/dsx-ai-factory/nke/cli/internal/bootstrap/registrymirror"
)

// ── The manifest ────────────────────────────────────────────────────────────

// TestManifest_BuiltImagesDefaultToLocalTags: the images the operator builds
// are listed with hostless tags, so a local build under those names is
// side-loaded and the bundle file needs no images block for them.
func TestManifest_BuiltImagesDefaultToLocalTags(t *testing.T) {
	m := LoadManifest()
	if got := m.Components[ComponentController].Images[ImageController].Ref; got != DefaultControllerImage {
		t.Errorf("controller image defaults to %q, want %q", got, DefaultControllerImage)
	}
	if got := m.Components[ComponentProvisioned].Images[ImageManager].Ref; got != DefaultProvisionedImage {
		t.Errorf("provisioned image defaults to %q, want %q", got, DefaultProvisionedImage)
	}
	for _, ref := range []string{DefaultControllerImage, DefaultProvisionedImage} {
		if _, hasHost := registrymirror.ParseImage(ref); hasHost {
			t.Errorf("%q names a registry; the default has to be side-loaded, not pulled", ref)
		}
	}
	want := map[string]string{
		"controller.images.controller":                    "controller",
		"cluster-api-provider-provisioned.images.manager": "provisionedProvider",
	}
	if got := operatorImagePaths(); !reflect.DeepEqual(got, want) {
		t.Errorf("operatorImagePaths = %v, want %v", got, want)
	}
}

// TestManifest_EveryPulledArtifactHasADigest: what is pulled is pinned. The
// operator's images are theirs to pin, in the reference if at all.
func TestManifest_EveryPulledArtifactHasADigest(t *testing.T) {
	theirs := operatorImagePaths()
	for name, comp := range LoadManifest().Components {
		if comp.Chart != nil {
			if comp.Chart.Ref != "" || comp.Chart.Repo == "" || comp.Chart.SHA256 == "" {
				t.Errorf("%s is pulled but is not an upstream release with a sha256: %+v", chartPath(name), comp.Chart)
			}
		}
		for imageName, img := range comp.Images {
			path := imagePath(name, imageName)
			_, built := theirs[path]
			switch {
			case built && img.Digest != "":
				t.Errorf("%s is the operator's but the manifest pins a digest for it", path)
			case !built && !strings.HasPrefix(img.Digest, "sha256:"):
				t.Errorf("%s is pulled but has no digest", path)
			}
		}
	}
}

// TestManifest_CarriedChartsAreNotListed: the charts nkectl ships inside
// itself are not entries, because nothing fetches them.
func TestManifest_CarriedChartsAreNotListed(t *testing.T) {
	m := LoadManifest()
	for _, name := range []string{ComponentController, ComponentPostgres} {
		if m.Components[name].Chart != nil {
			t.Errorf("%s lists a chart, but nkectl carries that chart in the binary", name)
		}
	}
	if kind, _, _ := m.entry(chartPath(ComponentController)); kind != entryNone {
		t.Error("controller.chart resolves to an entry")
	}
}

func TestManifest_NoFirstPartyRegistry(t *testing.T) {
	for _, host := range []string{"ghcr.io/dsx-ai-factory/", "nvcr.io/"} {
		if strings.Contains(string(ManifestBytes()), host) {
			t.Errorf("the manifest names a first-party registry (%s); operators build those artifacts", host)
		}
	}
}

// TestManifest_EveryComponentHasAComment: the comment on each component key
// says what it is and, for the operator's, where they name theirs.
func TestManifest_EveryComponentHasAComment(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal(ManifestBytes(), &doc); err != nil {
		t.Fatal(err)
	}
	components := doc.Content[0].Content[1]
	theirs := operatorImagePaths()
	for i := 0; i+1 < len(components.Content); i += 2 {
		key := components.Content[i]
		comment := strings.ToLower(key.HeadComment)
		if strings.TrimSpace(comment) == "" {
			t.Errorf("%s has no comment", key.Value)
			continue
		}
		mine := false
		for path := range theirs {
			if strings.HasPrefix(path, key.Value+".") {
				mine = true
			}
		}
		if mine && !strings.Contains(comment, "built by you") {
			t.Errorf("%s holds an image the operator builds but its comment does not say so", key.Value)
		}
	}
}

// ── The bundle file ──────────────────────────────────────────────────────────

// TestTemplate: what `template bootstrap-bundle` writes parses, needs
// exactly three values filled in, and validates once they are.
func TestTemplate(t *testing.T) {
	doc := string(TemplateBytes())
	if !strings.Contains(doc, "SPDX-License-Identifier: Apache-2.0") {
		t.Error("the template has no license header")
	}
	if got := strings.Count(doc, `: ""`); got != 3 {
		t.Errorf("the template has %d values to fill in, want 3 (endpoint and the two images)", got)
	}
	err := mustLoad(t, doc).Validate(LoadManifest())
	if err == nil {
		t.Fatal("the unedited template must not validate")
	}
	for _, want := range []string{
		"registryMirror.endpoint is required",
		"images.controller is required with registryMirror: push the image to your registry and name it here",
		"images.provisionedProvider is required with registryMirror: push the image to your registry and name it here",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q:\n%s", want, err)
		}
	}
	if got := strings.Count(err.Error(), "\n  - "); got != 3 {
		t.Errorf("expected exactly three problems, got %d:\n%s", got, err)
	}
	b := mustLoad(t, filled())
	if err := b.Validate(LoadManifest()); err != nil {
		t.Errorf("the template with its three values filled must validate: %v", err)
	}
	if b.Public() || len(b.Overrides) != 0 {
		t.Errorf("filled template = %+v", b)
	}
	// The template carries no manifest: that is nkectl's, printed by
	// `get bootstrap-manifest`.
	if strings.Contains(doc, "\ncomponents:") {
		t.Error("the template carries a components section; the manifest is nkectl's")
	}
	// The commented override block is an example the operator can uncomment.
	for _, want := range []string{"# overrides:", "#   spire.images.server:", "#     reason:"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the template is missing the commented example %q", want)
		}
	}
}

// TestLoad_EmptyAndPublicForms: nothing declared is a valid bundle that pulls
// from public registries, and so is one that names only an image.
func TestLoad_EmptyAndPublicForms(t *testing.T) {
	for name, doc := range map[string]string{
		"empty file":     "",
		"comment only":   "# nothing\n",
		"empty mapping":  "{}\n",
		"images only":    "images:\n  controller: registry.example/nke/controller:1.4.0\n",
		"overrides only": overrideSPIREServer("", "CVE"),
	} {
		t.Run(name, func(t *testing.T) {
			b := mustLoad(t, doc)
			if !b.Public() {
				t.Error("a bundle with no registryMirror must report itself public")
			}
			if err := b.Validate(LoadManifest()); err != nil {
				t.Errorf("must validate: %v", err)
			}
		})
	}
}

// TestLoad_RejectsUnknownFieldsAndOldShapes: a typo, and a file in the shape
// where the operator carried the whole manifest, are refused rather than
// half-read.
func TestLoad_RejectsUnknownFieldsAndOldShapes(t *testing.T) {
	for name, doc := range map[string]string{
		"typo":               "registryMirrorr:\n  endpoint: m:5000\n",
		"old header":         "apiVersion: nke.nvidia.com/v1alpha1\nkind: BootstrapBundle\n",
		"old generatedFrom":  "generatedFrom: provisional\n",
		"manifest carried":   "components:\n  temporal:\n    chart:\n      version: \"0.74.0\"\n",
		"two documents":      filled() + "---\nimages: {}\n",
		"override extra key": "overrides:\n  spire.images.server:\n    reason: x\n    ref: y\n    tag: z\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, doc))
			if err == nil {
				t.Fatal("Load accepted a bundle it should reject")
			}
			if !strings.Contains(err.Error(), "bootstrap bundle ") {
				t.Errorf("error does not name the file: %v", err)
			}
		})
	}
}

// TestValidate_ImagesAreTheOperators: the two images are the operator's to
// name, with no override needed. With a registry declared they must come from
// it, so a hostless image -- named or defaulted -- is refused there. Without
// one, hostless is the normal development form. Explicitly blank is a mistake.
func TestValidate_ImagesAreTheOperators(t *testing.T) {
	const mirror = "registryMirror:\n  endpoint: mirror.internal:5000\n"
	for name, tc := range map[string]struct {
		doc     string
		wantSub string
	}{
		"no registry, left out":   {doc: ""},
		"no registry, local tags": {doc: "images:\n  controller: cwe-controller:dev\n  provisionedProvider: cluster-api-provider-provisioned:dev\n"},
		"no registry, published":  {doc: "images:\n  controller: registry.example/nke/controller:1.4.0@sha256:aa\n  provisionedProvider: registry.example/nke/capp:1.0.0\n"},
		"registry, on it":         {doc: mirror + "images:\n  controller: mirror.internal:5000/nke/controller:1.4.0\n  provisionedProvider: mirror.internal:5000/nke/capp:1.0.0\n"},
		"registry, elsewhere":     {doc: mirror + "images:\n  controller: registry.example/nke/controller:1.4.0\n  provisionedProvider: registry.example/nke/capp:1.0.0\n"},
		"registry, left out": {
			doc:     mirror,
			wantSub: "images.controller is required with registryMirror: push the image to mirror.internal:5000 and name it here",
		},
		"registry, empty": {
			doc:     mirror + "images:\n  controller: \"\"\n  provisionedProvider: mirror.internal:5000/nke/capp:1.0.0\n",
			wantSub: "images.controller is required with registryMirror",
		},
		"registry, hostless": {
			doc:     mirror + "images:\n  controller: mirror.internal:5000/nke/controller:1.4.0\n  provisionedProvider: cluster-api-provider-provisioned:dev\n",
			wantSub: `images.provisionedProvider "cluster-api-provider-provisioned:dev" names no registry`,
		},
		"blank": {doc: "images:\n  controller: \"  \"\n", wantSub: "images.controller is blank"},
	} {
		t.Run(name, func(t *testing.T) {
			err := mustLoad(t, tc.doc).Validate(LoadManifest())
			if tc.wantSub == "" {
				if err != nil {
					t.Errorf("the operator's images are theirs to name: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("Validate error = %v, want it to mention %q", err, tc.wantSub)
			}
		})
	}
}

// overrideSPIREServer appends an override of the spire-server image to a
// newer tag with the digest of the image it now names.
func overrideSPIREServer(doc, reason string) string {
	return doc + "overrides:\n  spire.images.server:\n    reason: \"" + reason + "\"\n" +
		"    ref: ghcr.io/spiffe/spire-server:1.15.4\n    digest: sha256:" + strings.Repeat("1", 64) + "\n"
}

// TestValidate_OverrideRules: every way an override can be wrong is named by
// field, and a correct one is accepted.
func TestValidate_OverrideRules(t *testing.T) {
	const ok = ""
	for name, tc := range map[string]struct {
		block   string
		wantSub string
	}{
		"image, correct": {block: overrideSPIREServer("", "CVE-2026-1234; 1.15.4 not shipped yet"), wantSub: ok},
		"image, new ref without digest": {
			block:   "overrides:\n  spire.images.server:\n    reason: r\n    ref: ghcr.io/spiffe/spire-server:1.15.4\n",
			wantSub: ok,
		},
		"chart by repo, correct": {
			block:   "overrides:\n  temporal.chart:\n    reason: r\n    repo: https://go.temporal.io/helm-charts\n    name: temporal\n    version: \"0.75.0\"\n",
			wantSub: ok,
		},
		"chart by ref, correct": {block: "overrides:\n  spire-crds.chart:\n    reason: r\n    ref: ../charts/spire-crds\n", wantSub: ok},
		"no reason": {
			block:   "overrides:\n  spire.images.server:\n    reason: \"  \"\n    ref: ghcr.io/spiffe/spire-server:1.15.4\n",
			wantSub: "overrides.spire.images.server.reason must say why",
		},
		"unknown path": {
			block:   "overrides:\n  spire.images.nope:\n    reason: r\n    ref: x:1\n",
			wantSub: "overrides.spire.images.nope: the manifest has no entry at that path",
		},
		"carried chart": {
			block:   "overrides:\n  controller.chart:\n    reason: r\n    ref: ../charts/controller\n",
			wantSub: "overrides.controller.chart: the manifest has no entry at that path",
		},
		"operator's own image": {
			block:   "overrides:\n  controller.images.controller:\n    reason: r\n    ref: x:1\n",
			wantSub: "that image is yours already; name it in images.controller instead",
		},
		"image without ref": {
			block:   "overrides:\n  spire.images.server:\n    reason: r\n    digest: sha256:" + strings.Repeat("1", 64) + "\n",
			wantSub: "overrides.spire.images.server.ref is required",
		},
		"image changes nothing": {
			block:   "overrides:\n  spire.images.server:\n    reason: r\n    ref: ghcr.io/spiffe/spire-server:1.15.3\n",
			wantSub: "overrides.spire.images.server changes nothing",
		},
		"image keeps the old digest": {
			block:   "overrides:\n  spire.images.server:\n    reason: r\n    ref: ghcr.io/spiffe/spire-server:1.15.4\n    digest: sha256:4082f30d3e0ddc4000a171392c4ea174345ee44d161ee917c70b97b2ecfba141\n",
			wantSub: "overrides.spire.images.server.digest still names the image it replaced",
		},
		"image with chart fields": {
			block:   "overrides:\n  spire.images.server:\n    reason: r\n    ref: x:1\n    version: \"1\"\n",
			wantSub: "overrides.spire.images.server is an image: ref and digest are its replacement fields",
		},
		"chart with nothing": {
			block:   "overrides:\n  temporal.chart:\n    reason: r\n",
			wantSub: "a chart is replaced by a ref (an OCI reference or a path), or by repo, name and version together",
		},
		"chart with ref and repo": {
			block:   "overrides:\n  temporal.chart:\n    reason: r\n    ref: oci://x/temporal\n    repo: https://x\n    name: temporal\n    version: \"1\"\n",
			wantSub: "overrides.temporal.chart: give a ref, or repo, name and version, not both",
		},
		"chart changes nothing": {
			block:   "overrides:\n  temporal.chart:\n    reason: r\n    repo: https://go.temporal.io/helm-charts\n    name: temporal\n    version: \"0.74.0\"\n",
			wantSub: "overrides.temporal.chart changes nothing",
		},
		"chart keeps the old sha256": {
			block:   "overrides:\n  temporal.chart:\n    reason: r\n    ref: oci://x/temporal\n    sha256: 14a8efe1cc92c91befac151db91f20f9a13962788e9d710c86655add6d966d37\n",
			wantSub: "overrides.temporal.chart.sha256 still names the chart it replaced",
		},
		"chart with digest": {
			block:   "overrides:\n  temporal.chart:\n    reason: r\n    ref: oci://x/temporal\n    digest: sha256:aa\n",
			wantSub: "overrides.temporal.chart is a chart: digest is an image field",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := mustLoad(t, filled()+tc.block).Validate(LoadManifest())
			if tc.wantSub == ok {
				if err != nil {
					t.Errorf("a correct override must be accepted: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("Validate error = %v, want it to mention %q", err, tc.wantSub)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "bootstrap bundle ") {
				t.Errorf("error does not name the file: %v", err)
			}
		})
	}
}

// TestValidate_AllProblemsTogether: a mirror problem, a blank image and a bad
// override surface in one aggregated error.
func TestValidate_AllProblemsTogether(t *testing.T) {
	doc := "registryMirror:\n  endpoint: \"\"\n  chartsNamespace: \"Charts\"\nimages:\n  controller: \" \"\n" +
		"overrides:\n  spire.images.server:\n    reason: \"\"\n    ref: ghcr.io/spiffe/spire-server:1.15.4\n"
	err := mustLoad(t, doc).Validate(LoadManifest())
	if err == nil {
		t.Fatal("expected problems")
	}
	for _, want := range []string{
		"registryMirror.endpoint is required",
		"registryMirror.chartsNamespace",
		"images.controller is required with registryMirror",
		"images.provisionedProvider is required with registryMirror",
		"overrides.spire.images.server.reason must say why",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

// TestValidate_MirrorCACertFileRelativeToBundleDir: caCertFile resolves against
// the bundle file's directory and must hold certificates.
func TestValidate_MirrorCACertFileRelativeToBundleDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bundle.yaml")
	doc := strings.Replace(fill(string(TemplateBytes()), "m:5000", "m:5000/nke/controller:1", "m:5000/nke/capp:1"),
		`  # caCertFile: "registry-ca.crt"`, `  caCertFile: "ca.crt"`, 1)
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.CACertPath(); got != filepath.Join(dir, "ca.crt") {
		t.Errorf("CACertPath = %q", got)
	}
	if err := b.Validate(LoadManifest()); err != nil {
		t.Errorf("Validate reads no files and must pass: %v", err)
	}
	err = b.ValidateReferences()
	if err == nil || !strings.Contains(err.Error(), "PRIVATE KEY") || !strings.Contains(err.Error(), "registryMirror.caCertFile") {
		t.Errorf("a key file named as caCertFile must be rejected by field: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "bootstrap bundle ") {
		t.Errorf("error does not name the file: %v", err)
	}
}

// TestExampleIsTheTemplateFilled keeps cli/examples/bootstrap-bundle.yaml
// equal to `template bootstrap-bundle` with its three values filled in, so the
// example shows the template, and it validates as a complete production form.
func TestExampleIsTheTemplateFilled(t *testing.T) {
	path := filepath.Join("..", "..", "..", "examples", "bootstrap-bundle.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	want := fill(string(TemplateBytes()), "registry.customer.internal",
		"registry.customer.internal/nke/controller:1.4.0", "registry.customer.internal/nke/cluster-api-provider-provisioned:1.4.0")
	if string(raw) != want {
		t.Errorf("cli/examples/bootstrap-bundle.yaml is not the template with its three values filled; regenerate it:\n"+
			"  nkectl template bootstrap-bundle | <fill the three values> > cli/examples/bootstrap-bundle.yaml\n%s", diffHint(string(raw), want))
	}
	example, err := Load(path)
	if err != nil {
		t.Fatalf("load example: %v", err)
	}
	if err := example.Validate(LoadManifest()); err != nil {
		t.Errorf("the example is not valid:\n%v", err)
	}
	if err := example.ValidateReferences(); err != nil {
		t.Errorf("the example names a file it should not: %v", err)
	}
	if example.Public() || len(example.Overrides) != 0 {
		t.Errorf("the example should declare a registry and no override: %+v", example)
	}
}

// diffHint returns the first differing line of two documents.
func diffHint(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) && i < len(w); i++ {
		if g[i] != w[i] {
			return fmt.Sprintf("first difference at line %d:\n  got:  %s\n  want: %s", i+1, g[i], w[i])
		}
	}
	return fmt.Sprintf("lengths differ: got %d lines, want %d", len(g), len(w))
}

// ── Resolution ───────────────────────────────────────────────────────────────

// TestResolve_EverythingFromTheMirror: with only an endpoint declared every
// upstream artifact resolves onto the mirror by convention, the operator's
// images resolve to what was written, and nothing resolves to a public
// registry.
func TestResolve_EverythingFromTheMirror(t *testing.T) {
	c, err := Resolve(mustLoad(t, filled()), LoadManifest())
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Temporal.Args; !reflect.DeepEqual(got, []string{"oci://mirror.internal:5000/charts/temporal", "--version", "0.74.0"}) {
		t.Errorf("temporal args = %v", got)
	}
	if c.Temporal.SHA256 == "" || c.Temporal.Version != "0.74.0" {
		t.Errorf("temporal identity not carried: %+v", c.Temporal)
	}
	if c.ControllerImage.Repository != "mirror.internal:5000/nke/controller" || c.ControllerImage.Tag != "1.4.0" {
		t.Errorf("controller image = %+v", c.ControllerImage)
	}
	if c.ProvisionedImage.Ref != "mirror.internal:5000/nke/capp:1.0.0" {
		t.Errorf("provisioned image = %+v", c.ProvisionedImage)
	}
	if want := []string{"cgr.dev", "docker.io", "ghcr.io", "registry.k8s.io"}; !reflect.DeepEqual(c.Registries, want) {
		t.Errorf("Registries = %v, want %v", c.Registries, want)
	}
	for _, a := range c.Artifacts {
		if a.Note == "via mirror" && !strings.HasPrefix(strings.TrimPrefix(a.Location, "oci://"), "mirror.internal:5000/") {
			t.Errorf("%s resolves to %q, not to the mirror", a.Path, a.Location)
		}
	}
	table := c.DryRunTable(false)
	for _, want := range []string{
		"registryMirror: https://mirror.internal:5000",
		"docker.io                -> mirror.internal:5000/docker.io",
		"ghcr.io/spiffe/spire-server:1.15.3 -> mirror.internal:5000/ghcr.io/spiffe/spire-server:1.15.3",
		"charts                   -> oci://mirror.internal:5000/charts",
		"spire.images.server ",
		"mirror.internal:5000/nke/controller:1.4.0  (built by you; on the mirror)",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("dry-run table is missing %q:\n%s", want, table)
		}
	}
	if strings.Contains(table, "public)") || strings.Contains(table, "side-loaded") {
		t.Errorf("nothing may resolve publicly with a mirror declared:\n%s", table)
	}
}

// TestResolve_PublicIsUpstream: with no registryMirror block every artifact
// resolves to the registry it names, no redirect is computed, and no
// credential is implied.
func TestResolve_PublicIsUpstream(t *testing.T) {
	c, err := Resolve(mustLoad(t, ""), LoadManifest())
	if err != nil {
		t.Fatal(err)
	}
	if c.Mirror != nil || len(c.Registries) != 0 {
		t.Errorf("public mode must compute no redirect: mirror=%+v registries=%v", c.Mirror, c.Registries)
	}
	if got := c.Temporal.Args; !reflect.DeepEqual(got, []string{"temporal", "--repo", "https://go.temporal.io/helm-charts", "--version", "0.74.0"}) {
		t.Errorf("temporal args = %v", got)
	}
	if c.ControllerImage.Ref != DefaultControllerImage {
		t.Errorf("controller image = %q", c.ControllerImage.Ref)
	}
	table := c.DryRunTable(false)
	for _, want := range []string{
		"registryMirror: none; every artifact is pulled from the public registry it names",
		"ghcr.io/spiffe/spire-server:1.15.3  (public)",
		DefaultControllerImage + "  (built by you; side-loaded)",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("dry-run table is missing %q:\n%s", want, table)
		}
	}
	if strings.Contains(table, " -> ") {
		t.Errorf("nothing is redirected without a mirror:\n%s", table)
	}
}

// TestResolve_OneRuleForEveryImage: with a mirror, every image with a host
// comes through it, the operator's included. An image that already names the
// mirror is used as written. Without a mirror, a hostless image is side-loaded
// and an image with a host is pulled as written.
func TestResolve_OneRuleForEveryImage(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	controller := imagePath(ComponentController, ImageController)
	for name, tc := range map[string]struct {
		doc          string
		wantLocation string
		wantNote     string
		wantRegistry bool // registry.example added to c.Registries
		wantDigest   string
	}{
		"pushed elsewhere, with a mirror": {
			doc: "registryMirror:\n  endpoint: mirror.internal:5000\nimages:\n  controller: registry.example/nke/controller:1.4.0@" + digest +
				"\n  provisionedProvider: mirror.internal:5000/nke/capp:1.0.0\n",
			wantLocation: "mirror.internal:5000/registry.example/nke/controller:1.4.0@" + digest,
			wantNote:     "built by you; via mirror",
			wantRegistry: true,
			wantDigest:   digest,
		},
		"pushed to the mirror itself": {
			doc:          filled(),
			wantLocation: "mirror.internal:5000/nke/controller:1.4.0",
			wantNote:     "built by you; on the mirror",
		},
		"hostless, no mirror": {
			doc:          "",
			wantLocation: DefaultControllerImage,
			wantNote:     "built by you; side-loaded",
		},
		"pushed elsewhere, no mirror": {
			doc:          "images:\n  controller: registry.example/nke/controller:1.4.0\n",
			wantLocation: "",
			wantNote:     "built by you; public",
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := Resolve(mustLoad(t, tc.doc), LoadManifest())
			if err != nil {
				t.Fatal(err)
			}
			var row Artifact
			for _, a := range c.Artifacts {
				if a.Path == controller {
					row = a
				}
			}
			if row.Location != tc.wantLocation || row.Note != tc.wantNote {
				t.Errorf("controller row = %+v, want location %q note %q", row, tc.wantLocation, tc.wantNote)
			}
			if tc.wantDigest != "" && c.ControllerImage.Digest != tc.wantDigest {
				t.Errorf("digest from the reference not carried: %+v", c.ControllerImage)
			}
			inList := false
			for _, r := range c.Registries {
				if r == "registry.example" {
					inList = true
				}
				if r == "mirror.internal:5000" {
					t.Error("the mirror host was added to its own redirect list")
				}
			}
			if inList != tc.wantRegistry {
				t.Errorf("Registries = %v, want registry.example listed: %v", c.Registries, tc.wantRegistry)
			}
		})
	}
}

// TestResolve_ImageOnTheMirrorIsNotDoubleMapped: an override that names an
// image already on the mirror is used as written, not mapped under itself.
func TestResolve_ImageOnTheMirrorIsNotDoubleMapped(t *testing.T) {
	doc := filled() + "overrides:\n  spire.images.server:\n    reason: r\n    ref: mirror.internal:5000/spiffe/spire-server:1.15.4\n"
	c, err := Resolve(mustLoad(t, doc), LoadManifest())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range c.Artifacts {
		if a.Path == "spire.images.server" {
			if a.Location != "mirror.internal:5000/spiffe/spire-server:1.15.4" || a.Note != "on the mirror" {
				t.Errorf("row = %+v", a)
			}
		}
	}
	for _, r := range c.Registries {
		if r == "mirror.internal:5000" {
			t.Error("the mirror host was added to its own redirect list")
		}
	}
}

// TestResolve_Overrides: an override replaces the entry, is resolved like any
// other artifact of its kind, and is reported where the operator will see it.
func TestResolve_Overrides(t *testing.T) {
	const reason = "CVE-2026-1234; 1.15.4 not shipped yet"
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.yaml")
	doc := overrideSPIREServer(filled(), reason) +
		"  spire-crds.chart:\n    reason: \"internal chart build\"\n    ref: ../charts/spire-crds\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Validate(LoadManifest()); err != nil {
		t.Fatalf("declared overrides must be allowed: %v", err)
	}
	c, err := Resolve(b, LoadManifest())
	if err != nil {
		t.Fatal(err)
	}
	want := []Overridden{{Path: "spire-crds.chart", Reason: "internal chart build"}, {Path: "spire.images.server", Reason: reason}}
	if !reflect.DeepEqual(c.Overrides, want) {
		t.Errorf("Overrides = %+v, want %+v", c.Overrides, want)
	}
	// The relative chart path is relative to the bundle file.
	if wantPath := filepath.Join(dir, "../charts/spire-crds"); c.SPIRECRDs.Args[0] != filepath.Clean(wantPath) && c.SPIRECRDs.Args[0] != wantPath {
		t.Errorf("relative chart ref resolved to %q, want under %q", c.SPIRECRDs.Args[0], dir)
	}
	if !c.SPIRECRDs.Built || c.SPIRECRDs.SHA256 != "" {
		t.Errorf("an operator-supplied chart carries no upstream digest: %+v", c.SPIRECRDs)
	}
	table := c.DryRunTable(false)
	for _, sub := range []string{
		"overridden (2;",
		"spire.images.server: " + reason,
		"ghcr.io/spiffe/spire-server:1.15.4 [overridden] -> mirror.internal:5000/ghcr.io/spiffe/spire-server:1.15.4",
		"spire-crds.chart",
		"[overridden]",
	} {
		if !strings.Contains(table, sub) {
			t.Errorf("dry-run table is missing %q:\n%s", sub, table)
		}
	}
	if strings.Contains(table, "spire-agent:1.15.3 [overridden]") {
		t.Errorf("an entry with no override was marked:\n%s", table)
	}
	// The manifest itself was not mutated.
	if LoadManifest().Components[ComponentSPIRE].Images["server"].Ref != "ghcr.io/spiffe/spire-server:1.15.3" {
		t.Error("Resolve mutated the manifest")
	}
}

// TestResolve_NamespaceOverrides: an operator whose registry is organised by
// project rather than by upstream host declares the mapping.
func TestResolve_NamespaceOverrides(t *testing.T) {
	doc := "registryMirror:\n  endpoint: mirror.internal:5000\n  ociNamespaces:\n    - registry: docker.io\n    - registry: ghcr.io\n      namespace: ghcr\n  chartsNamespace: helm\n"
	c, err := Resolve(mustLoad(t, doc), LoadManifest())
	if err != nil {
		t.Fatal(err)
	}
	if got := c.SPIRE.Args; !reflect.DeepEqual(got, []string{"oci://mirror.internal:5000/helm/spire", "--version", "0.30.1"}) {
		t.Errorf("spire args = %v", got)
	}
	table := c.DryRunTable(true)
	for _, want := range []string{
		"auth: credential supplied",
		"docker.io                -> mirror.internal:5000\n",
		"ghcr.io                  -> mirror.internal:5000/ghcr\n",
		"cgr.dev                  -> mirror.internal:5000/cgr.dev\n",
		"docker.io/temporalio/server:1.30.3 -> mirror.internal:5000/temporalio/server:1.30.3",
		"ghcr.io/spiffe/spire-server:1.15.3 -> mirror.internal:5000/ghcr/spiffe/spire-server:1.15.3",
		"oci://mirror.internal:5000/helm",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("dry-run table is missing %q:\n%s", want, table)
		}
	}
}

// TestResolve_MirrorCAPathIsResolved: the closure's mirror carries the CA
// path validation read, not the string as written.
func TestResolve_MirrorCAPathIsResolved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.yaml")
	if err := os.WriteFile(path, []byte("registryMirror:\n  endpoint: m:5000\n  caCertFile: ca.crt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Resolve(b, LoadManifest())
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "ca.crt"); c.Mirror.CACertFile != want {
		t.Errorf("closure CACertFile = %q, want %q", c.Mirror.CACertFile, want)
	}
	if b.RegistryMirror.CACertFile != "ca.crt" {
		t.Error("Resolve mutated the bundle's own mirror block")
	}
	if !strings.Contains(c.DryRunTable(false), "tls: ca "+filepath.Join(dir, "ca.crt")) {
		t.Errorf("dry-run table does not show the resolved CA path:\n%s", c.DryRunTable(false))
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// filled is the template with its three values filled in: a registry at
// mirror.internal:5000 and the two images pushed to it.
func filled() string {
	return fill(string(TemplateBytes()), "mirror.internal:5000", "mirror.internal:5000/nke/controller:1.4.0", "mirror.internal:5000/nke/capp:1.0.0")
}

// fill sets the template's three values.
func fill(doc, registry, controller, provisioned string) string {
	doc = strings.Replace(doc, `endpoint: ""`, `endpoint: "`+registry+`"`, 1)
	doc = strings.Replace(doc, `controller: ""`, `controller: "`+controller+`"`, 1)
	doc = strings.Replace(doc, `provisionedProvider: ""`, `provisionedProvider: "`+provisioned+`"`, 1)
	return doc
}

func mustLoad(t *testing.T, doc string) *Bundle {
	t.Helper()
	b, err := Load(write(t, doc))
	if err != nil {
		t.Fatalf("load: %v\n%s", err, doc)
	}
	return b
}

func write(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bundle.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
