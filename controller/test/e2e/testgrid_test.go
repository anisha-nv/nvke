// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dsx-ai-factory/nke/controller/test/e2e/internal/kube"
	"github.com/dsx-ai-factory/nke/controller/test/e2e/internal/testgrid"
)

// testGridMetadataPath is where the run writes what it built and observed, for
// the workflow that publishes it to TestGrid. Empty records nothing, which is
// every run but the one CI lane that publishes.
var testGridMetadataPath string

func init() {
	flag.StringVar(&testGridMetadataPath, "e2e.testgrid-metadata", "",
		"write what the run built and observed to this file as key=value lines, for publishing to TestGrid (empty disables)")
}

// testGridFacts is nil unless -e2e.testgrid-metadata is set, and a nil
// Collector ignores everything, so the hooks below cost a run that does not
// publish nothing but a flag check. Resolved once, on first use, which is
// always after the flags are parsed: every caller is a spec or a suite node.
//
// It starts from what the workflow wrote before the suite ran, so a fact the
// suite never gets to record keeps the workflow's value rather than being
// dropped when the file is rewritten. Only the declared keys, which the
// workflow writes fresh every run: an observed fact must come from this run.
var testGridFacts = sync.OnceValue(func() *testgrid.Collector {
	if testGridMetadataPath == "" {
		return nil
	}
	c := testgrid.NewCollector()
	b, err := os.ReadFile(testGridMetadataPath)
	if err != nil {
		return c
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case testgrid.KeyStage, testgrid.KeyArch, testgrid.KeyGPU, testgrid.KeyKubernetesVersion:
			// A declared value that is present but unreadable becomes a
			// marker the publisher refuses, rather than vanishing: a GPU run
			// whose GPU was dropped would otherwise publish as a CPU run.
			if err := c.Set(key, value); err != nil {
				_ = c.Set(key, invalidDeclared)
			}
		}
	}
	return c
})

// invalidDeclared stands in for a declared value the suite could not read. It
// is a valid value, so it survives the rewrite, and it is not a stage, GPU or
// version the publisher accepts, so the run is not published under the wrong
// name.
const invalidDeclared = "invalid"

// Observing is best effort and bounded: it reads a cluster the run already
// reached, and a slow answer must not hold up the specs it rides along with.
const testGridObserveTimeout = 30 * time.Second

// recordTestGrid sets one fact, logging rather than failing when it is
// rejected. What the run qualified is the finding; a fact TestGrid cannot show
// must not turn a passing run into a failing one.
func recordTestGrid(key, value string) {
	if err := testGridFacts().Set(key, value); err != nil {
		GinkgoLogr.Error(err, "not recording a TestGrid fact", "key", key)
	}
}

// recordRunDeclared records what the run was asked for, from its flags alone,
// before anything is resolved: these are what name the TestGrid dashboard and
// tab, so a run that fails while resolving its site still shows up, red.
//
// The Kubernetes version is the declared one until the site's API server
// answers with its own, and carries the leading v the observed one has, so
// the column reads the same either way.
func recordRunDeclared() {
	if testGridFacts() == nil {
		return
	}
	defer writeTestGridMetadata()
	capabilities, err := runCapabilities()
	recordTestGrid(testgrid.KeyStage, siteStage)
	recordTestGrid(testgrid.KeyArch, capabilities["arch"])
	recordTestGrid(testgrid.KeyGPU, capabilities["gpu"])
	// A run that parsed its capabilities and asked for no GPU has none, even if
	// a file left by an earlier run said otherwise. One that could not parse
	// them says nothing, and keeps what the workflow declared.
	if err == nil && capabilities["gpu"] == "" {
		testGridFacts().Delete(testgrid.KeyGPU)
	}
	kubernetesVersion := cmp.Or(optionalVersion("kube"), forgeDefaultKubernetesVersion)
	recordTestGrid(testgrid.KeyKubernetesVersion, "v"+strings.TrimPrefix(kubernetesVersion, "v"))
}

// recordSiteResolved records which site and cluster name the lane resolved to.
func recordSiteResolved(lane siteLane) {
	if testGridFacts() == nil {
		return
	}
	defer writeTestGridMetadata()
	recordTestGrid(testgrid.KeyClusterName, lane.name)
	recordTestGrid(testgrid.KeySite, lane.site.Key)
}

// recordSiteObserved reads the built site's API server version and its node,
// through the kubeconfig the bootstrap returned.
func recordSiteObserved(ctx context.Context, kubeconfig string) {
	if testGridFacts() == nil || kubeconfig == "" {
		return
	}
	defer writeTestGridMetadata()
	ctx, cancel := context.WithTimeout(ctx, testGridObserveTimeout)
	defer cancel()
	restConfig, err := kube.RESTConfig(kubeconfig, "")
	if err != nil {
		GinkgoLogr.Error(err, "not observing the site for TestGrid")
		return
	}
	restConfig.Timeout = testGridObserveTimeout
	if version, verr := siteServerVersion(restConfig); verr != nil {
		GinkgoLogr.Error(verr, "not observing the site's version for TestGrid")
	} else {
		recordTestGrid(testgrid.KeyKubernetesVersion, version)
	}
	c, err := kube.RuntimeClient(restConfig)
	if err != nil {
		GinkgoLogr.Error(err, "not observing the site's node for TestGrid")
		return
	}
	nodes, err := testGridNodes(ctx, c)
	if err != nil {
		GinkgoLogr.Error(err, "not observing the site's node for TestGrid")
		return
	}
	summary := testgrid.NodeFacts(nodes)
	recordTestGrid(testgrid.KeyNodeArch, summary.Arch)
	recordTestGrid(testgrid.KeyInstanceType, summary.InstanceType)
}

func siteServerVersion(restConfig *rest.Config) (string, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return "", fmt.Errorf("building a discovery client: %w", err)
	}
	version, err := dc.ServerVersion()
	if err != nil {
		return "", fmt.Errorf("reading the server version: %w", err)
	}
	return version.GitVersion, nil
}

// recordTenantObserved reads the GPU product and kubelet version the tenant's
// nodes report. Called while the tenant still exists, which in the shared
// subtree means just before it is deleted.
func recordTenantObserved(ctx context.Context, test *tenantTest) {
	if testGridFacts() == nil || test.cluster == nil || test.tenantClients == nil {
		return
	}
	defer writeTestGridMetadata()
	ctx, cancel := context.WithTimeout(ctx, testGridObserveTimeout)
	defer cancel()
	c, err := test.tenantClients.GetAdminClient(ctx, test.cluster.Name)
	if err != nil {
		GinkgoLogr.Error(err, "not observing the tenant for TestGrid", "cluster", test.cluster.Name)
		return
	}
	nodes, err := testGridNodes(ctx, c)
	if err != nil {
		GinkgoLogr.Error(err, "not observing the tenant for TestGrid", "cluster", test.cluster.Name)
		return
	}
	summary := testgrid.NodeFacts(nodes)
	recordTestGrid(testgrid.KeyGPUProduct, summary.GPUProduct)
	recordTestGrid(testgrid.KeyTenantKubernetesVersion, summary.KubeletVersion)
}

func testGridNodes(ctx context.Context, c client.Client) ([]corev1.Node, error) {
	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes); err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	return nodes.Items, nil
}

// writeTestGridMetadata writes what the run has recorded so far.
//
// Each record function writes as it finishes, rather than only at the end, so
// a run that never reaches AfterSuite -- go test's own timeout, or the runner
// killed -- still leaves what it learned, and those are the runs most worth
// seeing. The write is atomic, so a later one simply replaces an earlier one.
// AfterSuite writes once more.
//
// Nothing is written until the suite has recorded something: the workflow
// writes what the run was asked for before the suite starts, and a suite that
// stops before recording anything must leave that file as it is rather than
// replace it with an empty one.
func writeTestGridMetadata() {
	facts := testGridFacts()
	if len(facts.Encode()) == 0 {
		return
	}
	if err := facts.WriteFile(testGridMetadataPath); err != nil {
		GinkgoLogr.Error(err, "writing the TestGrid metadata")
	}
}

// The Forge workflow records the declared Kubernetes version itself, before
// the suite runs, so a run that fails in setup can still be published. Its
// default has to be the suite's.
func TestForgeWorkflowDefaultsToTheSuitesKubernetesVersion(t *testing.T) {
	const workflow = "../../../.github/workflows/forge-site-controller-e2e.yml"
	b, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"${KUBERNETES_VERSION:-` + forgeDefaultKubernetesVersion + `}"`; !strings.Contains(string(b), want) {
		t.Fatalf("%s does not default the Kubernetes version with %s", workflow, want)
	}
}
