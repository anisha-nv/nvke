// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dsx-ai-factory/nke/controller/test/e2e/internal/kube"
)

const (
	// tenantRuntimeTarget is the deploy path installed into the tenant. It
	// lives in this repository and installs public charts, so a run needs no
	// credential beyond the tenant's own.
	tenantRuntimeTarget = "tenant-runtime"

	// gpuResourceName is what the device plugin advertises once a driver is
	// loaded and a GPU is visible to the kubelet. Allocatable rather than
	// capacity is the NKE contract: capacity says the hardware is there,
	// allocatable says a pod can have it.
	gpuResourceName corev1.ResourceName = "nvidia.com/gpu"

	// gpuProductLabel is applied by GPU feature discovery. It proves the
	// discovery chain ran end to end rather than just the device plugin, and
	// it names the hardware, which is what a qualification report wants.
	gpuProductLabel = "nvidia.com/gpu.product"

	// cudaSampleImage runs the CUDA vectorAdd sample and prints "Test PASSED".
	//
	// Pinned to the multi-architecture index digest, so the tag can move
	// without this run changing and so either architecture resolves from the
	// one reference. Pulled anonymously from the same registry the driver and
	// toolkit images come from, which means it adds no egress a tenant
	// running this runtime did not already need.
	cudaSampleImage = "nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda12.5.0" +
		"@sha256:7c7540bdf1f942d4fb6db97069fd6c289471b54ac29e3c7fcdf914cf77af7d41"

	// Budgets. They are split across three specs rather than pooled so a
	// failure says which stage was slow.
	//
	// runtimeInstallBudget covers helm, whose own --wait is PHASE_TIMEOUT
	// (15m), plus fetching the chart.
	runtimeInstallBudget = 25 * time.Minute
	// gpuReadyBudget is the large one, and it is large because the driver is
	// built on the node: the container fetches kernel headers and compiles
	// the modules before anything can advertise a GPU. helm returning says
	// nothing about this -- it waits on the operator Deployment and creates a
	// ClusterPolicy, and a custom resource is not a workload, so the operator
	// has not yet made the driver DaemonSet when helm reports success.
	gpuReadyBudget = 40 * time.Minute
	// cudaWorkloadBudget is a pull and a few seconds of arithmetic.
	cudaWorkloadBudget = 10 * time.Minute
)

// validateGPURuntimePostCreate installs the GPU runtime into the tenant and
// proves the result is a cluster a GPU workload can actually run on.
//
// Three specs, deliberately. "The GPU lane failed" is not an actionable
// sentence; "the chart installed, the node never advertised a GPU" is, and it
// points at the driver build rather than at the chart or the scheduler.
func validateGPURuntimePostCreate(test *tenantTest) {
	It("installs the tenant GPU runtime", func(ctx context.Context) {
		installTenantRuntime(ctx, test)
	}, Label("readiness", "gpu"), NodeTimeout(runtimeInstallBudget))

	It("advertises an allocatable GPU on a worker node", func(ctx context.Context) {
		adminClient := expectSucceed(test.tenantClients.GetAdminClient(ctx, test.cluster.Name))

		By("Waiting for the device plugin to advertise an allocatable GPU")
		var gpuNode corev1.Node
		Eventually(func(ctx context.Context) error {
			node, err := firstGPUNode(ctx, adminClient)
			if err != nil {
				return err
			}
			gpuNode = node
			return nil
		}).WithContext(ctx).
			WithTimeout(gpuReadyBudget).
			WithPolling(30*time.Second).
			Should(Succeed(), "a GPU node should become schedulable for GPU work")

		By("Checking GPU feature discovery labeled the node")
		product := gpuNode.Labels[gpuProductLabel]
		Expect(product).NotTo(BeEmpty(),
			"node %s advertises a GPU but carries no %s label, so feature discovery did not run",
			gpuNode.Name, gpuProductLabel)
		GinkgoLogr.Info("GPU node ready",
			"node", gpuNode.Name,
			"product", product,
			"allocatable", gpuNode.Status.Allocatable[gpuResourceName],
			"driver", gpuNode.Labels["nvidia.com/cuda.driver-version.full"])
	}, Label("readiness", "gpu"), NodeTimeout(gpuReadyBudget+5*time.Minute))

	It("runs a CUDA workload on a GPU node", func(ctx context.Context) {
		adminClient := expectSucceed(test.tenantClients.GetAdminClient(ctx, test.cluster.Name))

		// An ordinary namespaced pod that asks for the resource by name, not
		// anything the operator schedules for itself. The operator's own
		// validator also runs vectorAdd, but it runs privileged and with the
		// driver install directory mounted, so it can pass on a cluster where
		// a normal workload still cannot get a GPU.
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-cuda-vectoradd-",
				Namespace:    "default",
			},
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{{
					Name:  "vectoradd",
					Image: cudaSampleImage,
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{gpuResourceName: resource.MustParse("1")},
					},
				}},
				Tolerations: []corev1.Toleration{{
					Key:      string(gpuResourceName),
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoSchedule,
				}},
			},
		}

		_, logs, err := kube.RunPod(ctx, adminClient, pod, "vectoradd", cudaWorkloadBudget)
		Expect(err).NotTo(HaveOccurred(), "the CUDA sample should run to completion on a GPU node")
		Expect(logs).To(ContainSubstring("Test PASSED"),
			"the CUDA sample ran but did not report success:\n%s", logs)
	}, Label("readiness", "gpu"), NodeTimeout(cudaWorkloadBudget+5*time.Minute))
}

// installTenantRuntime runs the deploy path against the tenant.
//
// The same script and the same deploy-path format the site install uses, so
// there is no second installer to keep correct. It runs here on the runner
// rather than on a node because the tenant is what it targets, and the runner
// is where a relayed kubeconfig can reach one.
func installTenantRuntime(ctx context.Context, test *tenantTest) {
	GinkgoHelper()

	repo := repoRoot(ctx)
	kubeconfig := writeTenantKubeconfig(ctx, test)

	By("Installing the " + tenantRuntimeTarget + " deploy path into the tenant")
	command := exec.CommandContext(ctx, "bash",
		filepath.Join(repo, "build", "use-nke-sbom.sh"), "install",
		"--sbom", filepath.Join(repo, "controller", "deploy"),
		"--target", tenantRuntimeTarget,
		"--kubeconfig", kubeconfig,
	)
	command.Stdout = GinkgoWriter
	command.Stderr = GinkgoWriter
	Expect(command.Run()).To(Succeed(), "should install the %s deploy path", tenantRuntimeTarget)
}

// writeTenantKubeconfig puts a kubeconfig for the tenant on disk and returns
// its path.
//
// A file because helm and kubectl take one; there is no way to hand either an
// in-process dialer, which is why this needs the relayed form rather than the
// published one. Mode 0600 in a private directory because it carries a
// cluster-admin credential for the duration, and removed as soon as the spec
// that asked for it ends.
func writeTenantKubeconfig(ctx context.Context, test *tenantTest) string {
	GinkgoHelper()

	kubeconfig := expectSucceed(test.tenantClients.RelayedAdminKubeconfig(ctx, test.cluster.Name))
	dir, err := os.MkdirTemp("", "nke-e2e-tenant-kubeconfig-")
	Expect(err).NotTo(HaveOccurred(), "should create a directory for the tenant kubeconfig")
	DeferCleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "tenant.kubeconfig")
	Expect(os.WriteFile(path, kubeconfig, 0o600)).To(Succeed(), "should write the tenant kubeconfig")
	return path
}

// firstGPUNode returns a node advertising at least one allocatable GPU.
func firstGPUNode(ctx context.Context, c client.Client) (corev1.Node, error) {
	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes); err != nil {
		return corev1.Node{}, fmt.Errorf("listing tenant nodes: %w", err)
	}
	for _, node := range nodes.Items {
		if quantity, ok := node.Status.Allocatable[gpuResourceName]; ok && !quantity.IsZero() {
			return node, nil
		}
	}
	return corev1.Node{}, fmt.Errorf("no node advertises allocatable %s among %d nodes", gpuResourceName, len(nodes.Items))
}

// repoRoot is the clone the deploy path and its install script are read from.
func repoRoot(ctx context.Context) string {
	GinkgoHelper()

	top, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	Expect(err).NotTo(HaveOccurred(), "should find the clone holding the deploy path")
	return strings.TrimSpace(string(top))
}
