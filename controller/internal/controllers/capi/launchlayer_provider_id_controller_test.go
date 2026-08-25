// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package capi

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/NVIDIA/nke/controller/internal/kube"
)

const launchLayerTestProviderID = "launchlayer://vpc-1/instance/instance-1"

func TestLaunchLayerProviderIDControllerReconcilePatchesTenantNodeProviderID(t *testing.T) {
	controller, tenantClient := newLaunchLayerProviderIDControllerForTest(
		t,
		[]client.Object{
			newCAPIClusterForProviderIDTest(),
			newMachineForProviderIDTest(),
			newLaunchLayerMachineForProviderIDTest(launchLayerTestProviderID, providerIDTestNodeName),
		},
		[]client.Object{
			newTenantNodeForProviderIDTest(""),
		},
	)

	_, err := controller.Reconcile(context.Background(), providerIDReconcileRequest())
	require.NoError(t, err)

	node := getTenantNodeForProviderIDTest(t, tenantClient)
	require.Equal(t, launchLayerTestProviderID, node.Spec.ProviderID)
}

func TestLaunchLayerProviderIDControllerReconcileNoOpsWhenTenantNodeProviderIDMatches(t *testing.T) {
	controller, tenantClient := newLaunchLayerProviderIDControllerForTest(
		t,
		[]client.Object{
			newCAPIClusterForProviderIDTest(),
			newMachineForProviderIDTest(),
			newLaunchLayerMachineForProviderIDTest(launchLayerTestProviderID, providerIDTestNodeName),
		},
		[]client.Object{
			newTenantNodeForProviderIDTest(launchLayerTestProviderID),
		},
	)

	_, err := controller.Reconcile(context.Background(), providerIDReconcileRequest())
	require.NoError(t, err)

	node := getTenantNodeForProviderIDTest(t, tenantClient)
	require.Equal(t, launchLayerTestProviderID, node.Spec.ProviderID)
}

// The NICo controller writes topology labels alongside the providerID. This one
// writes the providerID and nothing else, so whatever else the Node carries is
// left exactly as it was found.
func TestLaunchLayerProviderIDControllerReconcileLeavesNodeLabelsAlone(t *testing.T) {
	labels := map[string]string{"example.com/keep": "value"}
	controller, tenantClient := newLaunchLayerProviderIDControllerForTest(
		t,
		[]client.Object{
			newCAPIClusterForProviderIDTest(),
			newMachineForProviderIDTest(),
			newLaunchLayerMachineForProviderIDTest(launchLayerTestProviderID, providerIDTestNodeName),
		},
		[]client.Object{
			newTenantNodeForProviderIDTest("", labels),
		},
	)

	_, err := controller.Reconcile(context.Background(), providerIDReconcileRequest())
	require.NoError(t, err)

	node := getTenantNodeForProviderIDTest(t, tenantClient)
	require.Equal(t, launchLayerTestProviderID, node.Spec.ProviderID)
	require.Equal(t, labels, node.Labels)
}

func TestLaunchLayerProviderIDControllerReconcileErrorsWhenTenantNodeProviderIDDiffers(t *testing.T) {
	controller, tenantClient := newLaunchLayerProviderIDControllerForTest(
		t,
		[]client.Object{
			newCAPIClusterForProviderIDTest(),
			newMachineForProviderIDTest(),
			newLaunchLayerMachineForProviderIDTest(launchLayerTestProviderID, providerIDTestNodeName),
		},
		[]client.Object{
			newTenantNodeForProviderIDTest("launchlayer://vpc-1/instance/other-instance"),
		},
	)

	_, err := controller.Reconcile(context.Background(), providerIDReconcileRequest())
	require.Error(t, err)

	node := getTenantNodeForProviderIDTest(t, tenantClient)
	require.Equal(t, "launchlayer://vpc-1/instance/other-instance", node.Spec.ProviderID)
}

func TestLaunchLayerProviderIDControllerReconcileWaitsForLaunchLayerMachineProviderID(t *testing.T) {
	tenantProvider := &fakeTenantClusterClientProvider{}
	controller := newLaunchLayerProviderIDControllerForTestWithProvider(
		t,
		[]client.Object{
			newCAPIClusterForProviderIDTest(),
			newMachineForProviderIDTest(),
			newLaunchLayerMachineForProviderIDTest("", providerIDTestNodeName),
		},
		tenantProvider,
	)

	result, err := controller.Reconcile(context.Background(), providerIDReconcileRequest())
	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter)
	require.Equal(t, 0, tenantProvider.calls)
}

func TestLaunchLayerProviderIDControllerReconcileWaitsForOwnerMachine(t *testing.T) {
	tenantProvider := &fakeTenantClusterClientProvider{}
	controller := newLaunchLayerProviderIDControllerForTestWithProvider(
		t,
		[]client.Object{
			newCAPIClusterForProviderIDTest(),
			newLaunchLayerMachineForProviderIDTest(launchLayerTestProviderID, ""),
		},
		tenantProvider,
	)

	result, err := controller.Reconcile(context.Background(), providerIDReconcileRequest())
	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter)
	require.Equal(t, 0, tenantProvider.calls)
}

func TestLaunchLayerProviderIDControllerReconcileWaitsForKubeconfigSecret(t *testing.T) {
	tenantProvider := &fakeTenantClusterClientProvider{
		err: apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "test-kubeconfig"),
	}
	controller := newLaunchLayerProviderIDControllerForTestWithProvider(
		t,
		[]client.Object{
			newCAPIClusterForProviderIDTest(),
			newMachineForProviderIDTest(),
			newLaunchLayerMachineForProviderIDTest(launchLayerTestProviderID, providerIDTestNodeName),
		},
		tenantProvider,
	)

	result, err := controller.Reconcile(context.Background(), providerIDReconcileRequest())
	require.NoError(t, err)
	require.Equal(t, tenantClusterReadinessRetryAfter, result.RequeueAfter)
	require.Equal(t, 1, tenantProvider.calls)
}

// The tenant client reports a missing kubeconfig as ErrTenantClientNotReady
// rather than a bare NotFound, so the wait path has to recognize both.
func TestLaunchLayerProviderIDControllerReconcileWaitsWhenTenantClientNotReady(t *testing.T) {
	tenantProvider := &fakeTenantClusterClientProvider{err: kube.ErrTenantClientNotReady}
	controller := newLaunchLayerProviderIDControllerForTestWithProvider(
		t,
		[]client.Object{
			newCAPIClusterForProviderIDTest(),
			newMachineForProviderIDTest(),
			newLaunchLayerMachineForProviderIDTest(launchLayerTestProviderID, providerIDTestNodeName),
		},
		tenantProvider,
	)

	result, err := controller.Reconcile(context.Background(), providerIDReconcileRequest())
	require.NoError(t, err)
	require.Equal(t, tenantClusterReadinessRetryAfter, result.RequeueAfter)
}

func TestLaunchLayerProviderIDControllerReconcileSkipsWhenCAPIClusterIsGone(t *testing.T) {
	tenantProvider := &fakeTenantClusterClientProvider{}
	controller := newLaunchLayerProviderIDControllerForTestWithProvider(
		t,
		[]client.Object{
			newMachineForProviderIDTest(),
			newLaunchLayerMachineForProviderIDTest(launchLayerTestProviderID, providerIDTestNodeName),
		},
		tenantProvider,
	)

	result, err := controller.Reconcile(context.Background(), providerIDReconcileRequest())
	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter)
	require.Equal(t, 0, tenantProvider.calls)
}

func TestLaunchLayerProviderIDControllerReconcileWaitsForTenantNode(t *testing.T) {
	controller, _ := newLaunchLayerProviderIDControllerForTest(
		t,
		[]client.Object{
			newCAPIClusterForProviderIDTest(),
			newMachineForProviderIDTest(),
			newLaunchLayerMachineForProviderIDTest(launchLayerTestProviderID, providerIDTestNodeName),
		},
		nil,
	)

	result, err := controller.Reconcile(context.Background(), providerIDReconcileRequest())
	require.NoError(t, err)
	require.Equal(t, tenantClusterReadinessRetryAfter, result.RequeueAfter)
}

func TestLaunchLayerProviderIDControllerReconcileSkipsDeletingLaunchLayerMachine(t *testing.T) {
	deletingLLMachine := newLaunchLayerMachineForProviderIDTest(launchLayerTestProviderID, providerIDTestNodeName)
	now := metav1.Now()
	deletingLLMachine.SetDeletionTimestamp(&now)
	deletingLLMachine.SetFinalizers([]string{"test.finalizer"})

	tenantProvider := &fakeTenantClusterClientProvider{}
	controller := newLaunchLayerProviderIDControllerForTestWithProvider(
		t,
		[]client.Object{
			newCAPIClusterForProviderIDTest(),
			newMachineForProviderIDTest(),
			deletingLLMachine,
		},
		tenantProvider,
	)

	result, err := controller.Reconcile(context.Background(), providerIDReconcileRequest())
	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter)
	require.Equal(t, 0, tenantProvider.calls)
}

func newLaunchLayerProviderIDControllerForTest(
	t *testing.T,
	managementObjects []client.Object,
	tenantObjects []client.Object,
) (*LaunchLayerProviderIDController, client.Client) {
	t.Helper()

	tenantClient := fake.NewClientBuilder().
		WithScheme(newTenantClientProviderIDTestScheme(t)).
		WithObjects(tenantObjects...).
		Build()
	tenantProvider := &fakeTenantClusterClientProvider{
		tenantClient: tenantClient,
	}
	controller := newLaunchLayerProviderIDControllerForTestWithProvider(t, managementObjects, tenantProvider)
	return controller, tenantClient
}

func newLaunchLayerProviderIDControllerForTestWithProvider(
	t *testing.T,
	managementObjects []client.Object,
	tenantProvider kube.TenantClientProvider,
) *LaunchLayerProviderIDController {
	t.Helper()

	managementClient := fake.NewClientBuilder().
		WithScheme(newLaunchLayerProviderIDControllerTestScheme(t)).
		WithObjects(managementObjects...).
		Build()
	return &LaunchLayerProviderIDController{
		k8sClient:            managementClient,
		tenantClientProvider: tenantProvider,
		logger:               slog.New(slog.DiscardHandler),
	}
}

func newLaunchLayerProviderIDControllerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, clusterv1.AddToScheme(scheme))
	// CAPLL has no Go types here, so the GVK is registered against the
	// unstructured type the controller reads it as.
	gvk := LaunchLayerMachineGVK()
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	metav1.AddToGroupVersion(scheme, gvk.GroupVersion())
	return scheme
}

func newLaunchLayerMachineForProviderIDTest(providerID, ownerMachineName string) *unstructured.Unstructured {
	llMachine := newLaunchLayerMachine()
	llMachine.SetName(providerIDTestNodeName)
	llMachine.SetNamespace("cluster-test")
	if providerID != "" {
		llMachine.Object["spec"] = map[string]any{"providerID": providerID}
	}

	if ownerMachineName != "" {
		isController := true
		llMachine.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion: clusterv1.GroupVersion.String(),
			Kind:       machineConst,
			Name:       ownerMachineName,
			Controller: &isController,
		}})
	}

	return llMachine
}
