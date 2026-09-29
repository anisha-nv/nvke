// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"

	"github.com/dsx-ai-factory/nke/controller/api"
	"github.com/dsx-ai-factory/nke/controller/test/e2e/internal/nke"
	"github.com/dsx-ai-factory/nke/controller/test/e2e/internal/nkectl"
)

const siteHealthSettle = 5 * time.Minute

var _ = Describe("Site tenant API reachability", Serial, Label("capd", "site-health"), func() {
	It("isolates a tenant outage, retains inventory, and recovers with API/CLI equivalence", func(ctx SpecContext) {
		opts := nke.Options{Provision: 10 * time.Minute, Delete: 5 * time.Minute, Poll: 5 * time.Second}
		test := expectSucceed(setupTenantTest(ctx, opts))
		_, clientset := authnCluster()
		Expect(expectSucceed(detectAuthnEnforcement(ctx, clientset))).To(Equal(authnModeEnforcing), "site-health requires enforcing authentication")
		runner := expectSucceed(nkectl.New(ctx, nkectl.Options{Kubeconfig: test.cfg.Kube.Kubeconfig, Context: test.cfg.Kube.Context, Timeouts: opts}))
		DeferCleanup(func() { Expect(runner.Close()).To(Succeed()) })
		assertSiteHealthAuth(ctx, runner.BaseURL())

		clusters := make([]*api.Cluster, 0, 2)
		DeferCleanup(func(ctx SpecContext) {
			for _, cluster := range clusters {
				Expect(test.nke.DeleteCluster(ctx, cluster.ID)).To(Succeed())
			}
			for _, cluster := range clusters {
				Expect(test.nke.WaitForClusterDeleted(ctx, cluster.ID)).To(Succeed())
			}
		}, NodeTimeout(10*time.Minute))
		for _, suffix := range []string{"target", "sentinel"} {
			shape := clusterShape{controlPlanePools: []nke.NodePoolSpec{{DesiredNodeCount: 1}}}
			spec := capdSpec(test.cfg, clusterNamePrefix+"-reach-"+suffix, shape)
			if os.Getenv("PROVISION_CONTROL_PLANE_WITH_CONTROL_PLANE_CONTROLLER") == "true" {
				shape.controlPlaneConfig = &nke.ControlPlaneConfigSpec{DesiredCount: 1}
				spec = capdControlPlaneControllerSpec(test.cfg, clusterNamePrefix+"-reach-"+suffix, shape)
			}
			spec.ControlPlane.ControlPlaneVIP = expectSucceed(test.availableControlPlaneVIP(ctx, preferredControlPlaneVIP(spec.Backend)))
			cluster := expectSucceed(test.nke.CreateCluster(ctx, spec))
			clusters = append(clusters, cluster)
			expectSucceed(test.nke.WaitForClusterReady(ctx, cluster.ID))
		}
		target, sentinel := clusters[0], clusters[1]
		waitSiteHealth(ctx, &test, target.ID, sentinel.ID, api.ConditionStatusTrue)
		assertSiteHealthParity(ctx, &test, runner)

		tenant := expectSucceed(test.tenantClients.GetClient(ctx, target.Name))
		endpoints := expectSucceed(controlPlaneRouteTargets(ctx, tenant))
		Expect(endpoints).To(HaveLen(1), "minimal fixture must cover every API endpoint")
		for _, endpoint := range endpoints {
			// Both the API-discovered node name and the CAPD ownership label must agree.
			label := expectSucceed(exec.CommandContext(ctx, "docker", "inspect", "--format", `{{ index .Config.Labels "io.x-k8s.kind.cluster" }}`, endpoint.name).Output())
			Expect(strings.TrimSpace(string(label))).To(Equal(target.Name))
			Expect(runDockerCommand(ctx, "exec", endpoint.name, "test", "!", "-e", kubeAPIServerManifestBackup)).To(Succeed())
		}
		staged := make([]controlPlaneRouteTarget, 0, len(endpoints))
		restore := func(ctx context.Context) error {
			var failures []string
			for _, endpoint := range staged {
				if err := runDockerCommand(ctx, "exec", endpoint.name, "sh", "-c", `if test -e "$1"; then mv "$1" "$2"; else test -e "$2"; fi`, "restore", kubeAPIServerManifestBackup, kubeAPIServerManifest); err != nil {
					failures = append(failures, endpoint.name)
				}
			}
			if len(failures) > 0 {
				return fmt.Errorf("restoring API manifests on %v", failures)
			}
			return nil
		}
		// Installed before mutation; record before mv so an interrupted command is recoverable.
		DeferCleanup(func(ctx SpecContext) {
			Expect(restore(ctx)).To(Succeed())
			if len(staged) > 0 {
				waitSiteHealth(ctx, &test, target.ID, sentinel.ID, api.ConditionStatusTrue)
			}
		}, NodeTimeout(7*time.Minute))
		for _, endpoint := range endpoints {
			staged = append(staged, endpoint)
			Expect(runDockerCommand(ctx, "exec", endpoint.name, "mv", kubeAPIServerManifest, kubeAPIServerManifestBackup)).To(Succeed())
		}
		waitSiteHealth(ctx, &test, target.ID, sentinel.ID, api.ConditionStatusFalse)
		assertSiteHealthParity(ctx, &test, runner)
		Expect(restore(ctx)).To(Succeed())
		waitSiteHealth(ctx, &test, target.ID, sentinel.ID, api.ConditionStatusTrue)
		assertSiteHealthParity(ctx, &test, runner)
		GinkgoWriter.Println("PASS: site-health outage isolation, inventory, auth, API/CLI parity and recovery")
	}, NodeTimeout(35*time.Minute))
})

func waitSiteHealth(ctx context.Context, test *tenantTest, target, sentinel uuid.UUID, want api.ConditionStatus) {
	GinkgoHelper()
	Eventually(func(ctx context.Context) (api.ConditionStatus, error) {
		clusters, err := test.nke.ListClusters(ctx)
		if err != nil {
			return "", err
		}
		var targetCondition *api.Condition
		foundTarget, foundSentinel := false, false
		for _, cluster := range clusters {
			switch cluster.ID {
			case target:
				foundTarget, targetCondition = true, cluster.Conditions.APIServerReachable
			case sentinel:
				foundSentinel = true
				// During initial settling Unknown is allowed; once target is unreachable the sentinel must remain True.
				if want == api.ConditionStatusFalse {
					Expect(cluster.Conditions.APIServerReachable).NotTo(BeNil())
					Expect(cluster.Conditions.APIServerReachable.Status).To(Equal(api.ConditionStatusTrue))
				} else if cluster.Conditions.APIServerReachable == nil || cluster.Conditions.APIServerReachable.Status != api.ConditionStatusTrue {
					return "", nil
				}
			}
		}
		Expect(foundTarget && foundSentinel).To(BeTrue(), "both tenants must remain listed throughout an outage")
		if targetCondition == nil {
			return "", nil
		}
		return targetCondition.Status, nil
	}).WithContext(ctx).WithTimeout(siteHealthSettle).WithPolling(5 * time.Second).Should(Equal(want))
}

type siteHealthProjection struct {
	ID        uuid.UUID
	Name      string
	Condition *api.Condition
}

func siteHealthProject(clusters []api.Cluster) []siteHealthProjection {
	result := make([]siteHealthProjection, 0, len(clusters))
	for _, cluster := range clusters {
		result = append(result, siteHealthProjection{cluster.ID, cluster.Name, cluster.Conditions.APIServerReachable})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID.String() < result[j].ID.String() })
	return result
}

func assertSiteHealthParity(ctx context.Context, test *tenantTest, runner *nkectl.Runner) {
	GinkgoHelper()
	Eventually(func(ctx context.Context) error {
		before, err := test.nke.ListClusters(ctx)
		if err != nil {
			return err
		}
		expected := siteHealthProject(before)
		for _, format := range []string{"json", "yaml"} {
			output, runErr := runner.Run(ctx, "get", "clusters", "-o", format)
			if runErr != nil {
				return runErr
			}
			data := []byte(output)
			if format == "yaml" {
				data, err = yaml.YAMLToJSON(data)
				if err != nil {
					return err
				}
			}
			var clusters []api.Cluster
			if decodeErr := json.Unmarshal(data, &clusters); decodeErr != nil {
				return decodeErr
			}
			if !reflect.DeepEqual(expected, siteHealthProject(clusters)) {
				return fmt.Errorf("%s projection differs", format)
			}
		}
		output, err := runner.Run(ctx, "get", "clusters")
		if err != nil {
			return err
		}
		lines := strings.Split(strings.TrimSpace(output), "\n")
		if len(lines) != len(before)+1 || !strings.HasSuffix(strings.TrimSpace(lines[0]), "API REACHABLE") {
			return fmt.Errorf("table header or row count differs")
		}
		rows := make(map[string][]string)
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) != 7 {
				return fmt.Errorf("unexpected table row")
			}
			rows[fields[4]] = fields
		}
		for _, cluster := range before {
			fields, ok := rows[cluster.ID.String()]
			if !ok || fields[0] != cluster.Name {
				return fmt.Errorf("table identity differs")
			}
			// This spec only creates provisioned fixtures and never mutates their lifecycle.
			if cluster.Conditions.Provisioned.Status == api.ConditionStatusTrue && fields[3] != "Provisioned" {
				return fmt.Errorf("lifecycle status changed")
			}
			want := "Unknown"
			if c := cluster.Conditions.APIServerReachable; c != nil && (c.Status == api.ConditionStatusTrue || c.Status == api.ConditionStatusFalse) {
				want = string(c.Status)
			}
			if fields[6] != want {
				return fmt.Errorf("table reachability differs")
			}
		}
		after, err := test.nke.ListClusters(ctx)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(expected, siteHealthProject(after)) {
			return fmt.Errorf("API changed during comparison")
		}
		return nil
	}).WithContext(ctx).WithTimeout(siteHealthSettle).WithPolling(5 * time.Second).MustPassRepeatedly(2).Should(Succeed())
}

func assertSiteHealthAuth(ctx context.Context, baseURL string) {
	GinkgoHelper()
	binary := os.Getenv(nkectl.BinaryEnv)
	if binary == "" {
		binary = "nkectl"
	}
	parsed := expectSucceed(url.Parse(baseURL))
	for _, token := range []string{"", "example-invalid-token"} {
		func() {
			var status atomic.Int32
			proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
				r.SetURL(parsed)
				r.Out.Header.Del("Authorization")
				if token != "" {
					r.Out.Header.Set("Authorization", "Bearer "+token)
				}
			}}
			proxy.ModifyResponse = func(r *http.Response) error { status.Store(int32(r.StatusCode)); return nil }
			server := httptest.NewServer(proxy)
			defer server.Close()
			req := expectSucceed(http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/clusters", http.NoBody))
			response, requestErr := (&http.Client{Timeout: time.Minute}).Do(req)
			Expect(requestErr).NotTo(HaveOccurred())
			defer response.Body.Close()
			Expect(response.Body.Close()).To(Succeed())
			Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			dir := expectSucceed(os.MkdirTemp("", "site-health-auth-"))
			defer os.RemoveAll(dir)
			Expect(os.MkdirAll(filepath.Join(dir, "nkectl"), 0o700)).To(Succeed())
			// No kubeconfig: the CLI cannot mint a replacement token for either denial control.
			cfg := fmt.Sprintf("sites:\n  denied:\n    url: %s\n    kubeconfig: \"\"\ncurrent-site: denied\n", server.URL)
			Expect(os.WriteFile(filepath.Join(dir, "nkectl", "config.yaml"), []byte(cfg), 0o600)).To(Succeed())
			command := exec.CommandContext(ctx, binary, "--site", "denied", "get", "clusters")
			command.Env = append(os.Environ(), "XDG_CONFIG_HOME="+dir)
			status.Store(0)
			_, err := command.CombinedOutput()
			Expect(err).To(HaveOccurred(), "CLI must reject missing/invalid credentials")
			Expect(status.Load()).To(Equal(int32(http.StatusUnauthorized)), "denial must come from CWE")
		}()
	}
}
