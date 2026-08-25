// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package capi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	capiutil "sigs.k8s.io/cluster-api/util"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nke/controller/internal/kube"
)

// LaunchLayerMachineGVK identifies the CAPLL infrastructure machine.
//
// CAPLL is read generically rather than through its Go types, unlike the NICo
// counterpart. cluster-api-provider-launchlayer is an INTERNAL repository, so
// importing it would add a second internal dependency to the vendor tree of a
// repository being prepared to go public, and would break the guarantee that
// every module builds from a clean checkout with no credentials. Only one
// string field is needed, and Cluster API reads other providers' infrastructure
// machines generically for the same reason. Please do not "fix" this to a typed
// import while CAPLL is internal.
func LaunchLayerMachineGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "infrastructure.cluster.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "LaunchLayerMachine",
	}
}

// LaunchLayerProviderIDController fills the small cloud-controller-manager-style
// gap between the management cluster and the tenant cluster. CAPI learns the
// LaunchLayer instance identity from LaunchLayerMachine.spec.providerID, but
// kubelet registers the tenant Node without that providerID. This controller
// copies the identity onto the tenant Node so CAPI can set Machine.status.nodeRef
// and transition to Ready.
//
// It writes the providerID and nothing else.
type LaunchLayerProviderIDController struct {
	k8sClient            client.Client
	tenantClientProvider kube.TenantClientProvider
	logger               *slog.Logger
}

// NewLaunchLayerProviderIDController returns a controller that copies
// LaunchLayerMachine.spec.providerID onto the matching tenant Node.
func NewLaunchLayerProviderIDController(
	k8sClient client.Client,
	tenantClientProvider kube.TenantClientProvider,
	logger *slog.Logger,
) *LaunchLayerProviderIDController {
	return &LaunchLayerProviderIDController{
		k8sClient:            k8sClient,
		tenantClientProvider: tenantClientProvider,
		logger:               logger,
	}
}

// SetupWithManager registers the controller with mgr.
func (c *LaunchLayerProviderIDController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(newLaunchLayerMachine()).
		Named("launchlayer-provider-id").
		Complete(c)
}

// Reconcile copies the provider ID onto the tenant Node backing the owning Machine.
func (c *LaunchLayerProviderIDController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := c.logger.With("namespace", req.Namespace, "launchLayerMachineName", req.Name)

	llMachine := newLaunchLayerMachine()
	if err := c.k8sClient.Get(ctx, req.NamespacedName, llMachine); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get launchlayer machine: %w", err)
	}
	logger = logger.With("resourceVersion", llMachine.GetResourceVersion())

	if llMachine.GetDeletionTimestamp() != nil {
		logger.Info("LaunchLayerMachine is being deleted, skipping provider ID reconciliation")
		return ctrl.Result{}, nil
	}

	providerID, _, err := unstructured.NestedString(llMachine.Object, "spec", "providerID")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to read spec.providerID from launchlayer machine: %w", err)
	}
	if providerID == "" {
		logger.Info("Waiting for providerID to be set on LaunchLayerMachine")
		return ctrl.Result{}, nil
	}

	// The owner Machine anchors the CAPI cluster relationship and defines the
	// tenant Node name used by NKE bootstrap.
	machine, err := capiutil.GetOwnerMachine(ctx, c.k8sClient, metav1.ObjectMeta{
		Namespace:       llMachine.GetNamespace(),
		OwnerReferences: llMachine.GetOwnerReferences(),
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get owner machine: %w", err)
	}
	if machine == nil {
		logger.Info("Waiting for Machine controller to set OwnerRef on LaunchLayerMachine")
		return ctrl.Result{}, nil
	}
	if !machine.DeletionTimestamp.IsZero() {
		logger.Info("Machine is being deleted, skipping provider ID reconciliation", "machineName", machine.Name)
		return ctrl.Result{}, nil
	}
	if machine.Spec.ClusterName == "" {
		return ctrl.Result{}, fmt.Errorf("machine %s/%s has empty spec.clusterName", machine.Namespace, machine.Name)
	}

	cluster := &clusterv1.Cluster{}
	if err := c.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: machine.Namespace,
		Name:      machine.Spec.ClusterName,
	}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("CAPI Cluster no longer exists, skipping provider ID reconciliation", "clusterName", machine.Spec.ClusterName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get CAPI cluster: %w", err)
	}

	tenantClient, err := c.tenantClientProvider.ClientForCluster(ctx, cluster)
	if err != nil {
		if apierrors.IsNotFound(err) || errors.Is(err, kube.ErrTenantClientNotReady) {
			logger.Info("Waiting for CAPI kubeconfig secret", "clusterName", cluster.Name)
			return ctrl.Result{RequeueAfter: tenantClusterReadinessRetryAfter}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to create tenant cluster client: %w", err)
	}

	// CWE creates Machine.Name from db.Node.Name and renders the bootstrap that
	// passes that same value to kubeadm as nodeRegistration.name, so the tenant
	// Node name is known here without consulting the instance.
	node := &corev1.Node{}
	if err := tenantClient.Get(ctx, client.ObjectKey{Name: machine.Name}, node); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Waiting for tenant Node to exist", "nodeName", machine.Name)
			return ctrl.Result{RequeueAfter: tenantClusterReadinessRetryAfter}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get tenant node: %w", err)
	}

	if node.Spec.ProviderID == providerID {
		logger.Debug("Tenant Node already has expected providerID", "nodeName", node.Name)
		return ctrl.Result{}, nil
	}
	// A different providerID means this Node is not the one CAPI will bind to
	// this Machine. Overwriting it would move some other Machine's binding, so
	// stop and let an operator resolve it.
	if node.Spec.ProviderID != "" {
		return ctrl.Result{}, fmt.Errorf(
			"tenant node %s already has providerID %q, expected %q",
			node.Name,
			node.Spec.ProviderID,
			providerID,
		)
	}

	patch := client.MergeFrom(node.DeepCopy())
	node.Spec.ProviderID = providerID
	if err := tenantClient.Patch(ctx, node, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch tenant node providerID: %w", err)
	}

	logger.Info("Patched tenant Node providerID", "nodeName", node.Name, "providerID", node.Spec.ProviderID)
	return ctrl.Result{}, nil
}

func newLaunchLayerMachine() *unstructured.Unstructured {
	llMachine := &unstructured.Unstructured{}
	llMachine.SetGroupVersionKind(LaunchLayerMachineGVK())
	return llMachine
}
