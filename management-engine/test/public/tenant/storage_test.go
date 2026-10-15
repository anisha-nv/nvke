// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Real datastore tests verify persistence, concurrency, projection and the API identity's permissions.
package tenant_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	internalv1 "github.com/dsx-ai-factory/nvke/management-engine/apis/v1"
	"github.com/dsx-ai-factory/nvke/management-engine/controllers"
	tenantcluster "github.com/dsx-ai-factory/nvke/management-engine/internal/public/tenant/apis/cluster"
	"github.com/dsx-ai-factory/nvke/management-engine/internal/public/tenant/limits"
	"github.com/dsx-ai-factory/nvke/management-engine/internal/public/tenant/server"
	clusterv1 "github.com/dsx-ai-factory/nvke/management-engine/public/tenant/gen/cluster/v1"
)

// TestPublicClusterPersistence exercises public operations against Kubernetes admission and RBAC.
func TestPublicClusterPersistence(t *testing.T) {
	useExisting := false
	environment := &envtest.Environment{
		UseExistingCluster:    &useExisting,
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	config, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	api := newService(t, config)
	client, err := dynamic.NewForConfig(config)
	require.NoError(t, err)
	resource := client.Resource(resourceGVR()).Namespace("default")
	ctx := t.Context()

	t.Run("public transports return controller status from storage", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		listeners := make([]net.Listener, 3)
		for i := range listeners {
			listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
			require.NoError(t, err)
			listeners[i] = listener
			t.Cleanup(func() { _ = listener.Close() })
		}
		runtimeServer, err := server.New(server.Addresses{}, limits.DefaultCapacity(), api, api.Ready)
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() { done <- runtimeServer.Serve(ctx, listeners[0], listeners[1], listeners[2]) }()
		t.Cleanup(func() {
			cancel()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Error("public API did not stop after the test")
			}
		})
		connection, err := grpc.NewClient("passthrough:///"+listeners[1].Addr().String(),
			grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithNoProxy())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, connection.Close()) })
		rpc := clusterv1.NewClusterServiceClient(connection)
		httpClient := &http.Client{Timeout: 5 * time.Second}
		t.Cleanup(httpClient.CloseIdleConnections)
		endpoint := "http://" + listeners[0].Addr().String() + "/v1/clusters"
		body, err := protojson.Marshal(storageCreate(t, "transport-roundtrip"))
		require.NoError(t, err)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		response, err := httpClient.Do(request)
		require.NoError(t, err)
		body, err = io.ReadAll(response.Body)
		require.NoError(t, response.Body.Close())
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode, "%s", body)
		var created clusterv1.Cluster
		require.NoError(t, protojson.Unmarshal(body, &created))
		public, err := rpc.GetCluster(ctx, &clusterv1.GetClusterRequest{Name: created.Name})
		require.NoError(t, err)
		require.True(t, proto.Equal(&created, public))
		require.Equal(t, clusterv1.ClusterStatus_STATE_PENDING, public.Status.State)

		scheme := runtime.NewScheme()
		require.NoError(t, internalv1.AddToScheme(scheme))
		kube, err := kubeclient.New(config, kubeclient.Options{Scheme: scheme})
		require.NoError(t, err)
		// Direct reconciliation verifies the status boundary without depending on watches or CWE.
		reconciler := &controllers.NVKEClusterReconciler{Client: kube}
		_, err = reconciler.Reconcile(ctx, ctrl.Request{Namespace: "default", Name: created.Name})
		require.NoError(t, err)
		stored, err := resource.Get(ctx, created.Name, metav1.GetOptions{})
		require.NoError(t, err)
		observed, found, err := unstructured.NestedInt64(stored.Object, "status", "observedGeneration")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, created.Generation, observed)
		public, err = rpc.GetCluster(ctx, &clusterv1.GetClusterRequest{Name: created.Name})
		require.NoError(t, err)
		require.Equal(t, stored.GetResourceVersion(), public.ResourceVersion)
		require.NotEqual(t, created.ResourceVersion, public.ResourceVersion)
		require.Equal(t, clusterv1.ClusterStatus_STATE_UNKNOWN, public.Status.State)
		require.Equal(t, clusterv1.ClusterStatus_REASON_STATUS_UNAVAILABLE, public.Status.Reason)
		require.Equal(t, created.Generation, public.Status.ObservedGeneration)
		request, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/"+created.Name, nil)
		require.NoError(t, err)
		response, err = httpClient.Do(request)
		require.NoError(t, err)
		body, err = io.ReadAll(response.Body)
		require.NoError(t, response.Body.Close())
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode, "%s", body)
		var projected clusterv1.Cluster
		require.NoError(t, protojson.Unmarshal(body, &projected))
		require.True(t, proto.Equal(public, &projected))
		assert.NotContains(t, string(body), "ProvisioningNotConfigured")
	})

	t.Run("atomic concurrent creation", func(t *testing.T) {
		requests := []*clusterv1.CreateClusterRequest{storageCreate(t, "create-race"), storageCreate(t, "create-race")}
		var wait sync.WaitGroup
		errors := make(chan error, 2)
		for _, request := range requests {
			wait.Go(func() { _, err := api.CreateCluster(ctx, request); errors <- err })
		}
		wait.Wait()
		close(errors)
		results := map[codes.Code]int{}
		for err := range errors {
			results[status.Code(err)]++
		}
		assert.Equal(t, map[codes.Code]int{codes.OK: 1, codes.AlreadyExists: 1}, results)
	})

	t.Run("changed merge preserves private state and returns persisted version", func(t *testing.T) {
		request := storageCreate(t, "roundtrip")
		request.Spec.Release.Channel.Version = new("v1")
		created, err := api.CreateCluster(ctx, request)
		require.NoError(t, err)
		require.NotEmpty(t, created.Uid)
		require.Equal(t, int64(1), created.Generation)
		_, err = resource.Patch(ctx, created.Name, types.MergePatchType,
			[]byte(`{"metadata":{"annotations":{"example.com/private":"PRIVATE"}},
			"spec":{"acceleratedRuntime":{"name":"none"}}}`), metav1.PatchOptions{FieldValidation: "Strict"})
		require.NoError(t, err)
		stored, err := resource.Get(ctx, created.Name, metav1.GetOptions{})
		require.NoError(t, err)
		stored.Object["status"] = map[string]any{"conditions": []any{map[string]any{
			"type": "Ready", "status": "True", "observedGeneration": stored.GetGeneration(),
			"reason": "Ready", "message": "PRIVATE", "lastTransitionTime": "2026-10-10T00:00:00Z",
		}}}
		stored, err = resource.UpdateStatus(ctx, stored, metav1.UpdateOptions{})
		require.NoError(t, err)
		current, err := api.GetCluster(ctx, &clusterv1.GetClusterRequest{Name: created.Name})
		require.NoError(t, err)
		require.Equal(t, clusterv1.ClusterStatus_STATE_READY, current.Status.State)
		require.Equal(t, "v1", current.Spec.Release.Channel.GetVersion())
		replaced, err := api.ReplaceCluster(ctx, replacement(created.Name, current.ResourceVersion))
		require.NoError(t, err)
		assert.Equal(t, clusterv1.ClusterStatus_STATE_PENDING, replaced.Status.State,
			"old ready observation cannot describe changed desired state")
		assert.Equal(t, current.Generation+1, replaced.Generation)
		assert.NotEqual(t, current.ResourceVersion, replaced.ResourceVersion)
		persisted, err := api.GetCluster(ctx, &clusterv1.GetClusterRequest{Name: created.Name})
		require.NoError(t, err)
		assert.True(t, proto.Equal(replaced, persisted))
		raw, err := resource.Get(ctx, created.Name, metav1.GetOptions{})
		require.NoError(t, err)
		_, found, err := unstructured.NestedString(raw.Object, "spec", "release", "channel", "version")
		require.NoError(t, err)
		assert.False(t, found, "replacement must clear the omitted writable version")
		assert.Equal(t, "PRIVATE", raw.GetAnnotations()["example.com/private"])
		runtimeName, _, err := unstructured.NestedString(raw.Object, "spec", "acceleratedRuntime", "name")
		require.NoError(t, err)
		assert.Equal(t, "none", runtimeName)
		network, _, err := unstructured.NestedMap(raw.Object, "spec", "clusterNetwork")
		require.NoError(t, err)
		originalNetwork, _, err := unstructured.NestedMap(stored.Object, "spec", "clusterNetwork")
		require.NoError(t, err)
		assert.Equal(t, originalNetwork, network, "create-only networking must survive replacement")
		encoded, err := protojson.Marshal(replaced)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), "PRIVATE")
		assert.NotContains(t, string(encoded), "accelerated")
		assert.NotContains(t, string(encoded), "managedFields")
		_, err = api.ReplaceCluster(ctx, replacement(created.Name, current.ResourceVersion))
		assert.Equal(t, codes.Aborted, status.Code(err))
	})

	t.Run("complete pagination", func(t *testing.T) {
		for i := range 4 {
			_, err := api.CreateCluster(ctx, storageCreate(t, fmt.Sprintf("page-%d", i)))
			require.NoError(t, err)
		}
		first, err := api.ListClusters(ctx, &clusterv1.ListClustersRequest{PageSize: 2})
		require.NoError(t, err)
		require.Len(t, first.Clusters, 2)
		require.NotEmpty(t, first.NextPageToken)
		names := map[string]bool{}
		page := first
		for {
			for _, cluster := range page.Clusters {
				require.False(t, names[cluster.Name], "duplicate cluster on different pages")
				names[cluster.Name] = true
			}
			if page.NextPageToken == "" {
				break
			}
			page, err = api.ListClusters(ctx, &clusterv1.ListClustersRequest{PageToken: page.NextPageToken})
			require.NoError(t, err)
		}
		all, err := resource.List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		expected := make(map[string]bool, len(all.Items))
		for _, cluster := range all.Items {
			expected[cluster.GetName()] = true
		}
		assert.Equal(t, expected, names)
	})

	t.Run("configured namespaces isolate same-name clusters", func(t *testing.T) {
		_, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Create(ctx,
			&unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "other"},
			}}, metav1.CreateOptions{})
		require.NoError(t, err)
		other, err := tenantcluster.New(client, tenantcluster.Config{
			Namespace: "other", DatastoreID: "envtest", PageTokenKey: []byte(strings.Repeat("test", 8)),
		})
		require.NoError(t, err)
		original, err := api.CreateCluster(ctx, storageCreate(t, "namespace-isolation"))
		require.NoError(t, err)
		request := &clusterv1.GetClusterRequest{Name: original.Name}
		_, err = other.GetCluster(ctx, request)
		require.Equal(t, codes.NotFound, status.Code(err))
		created, err := other.CreateCluster(ctx, storageCreate(t, request.Name))
		require.NoError(t, err)
		assert.NotEqual(t, original.Uid, created.Uid)
		isolated, err := other.GetCluster(ctx, request)
		require.NoError(t, err)
		assert.True(t, proto.Equal(created, isolated))
		_, err = other.ReplaceCluster(ctx, replacement(request.Name, original.ResourceVersion))
		require.Equal(t, codes.Aborted, status.Code(err))
		unchanged, err := api.GetCluster(ctx, request)
		require.NoError(t, err)
		assert.True(t, proto.Equal(original, unchanged))
	})

	t.Run("deployed API role enforces datastore least privilege", func(t *testing.T) {
		installAPIIdentity(t, ctx, client)
		identity := rest.CopyConfig(config)
		identity.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:default:public-api"}
		scoped, err := dynamic.NewForConfig(identity)
		require.NoError(t, err)
		access := scoped.Resource(resourceGVR()).Namespace("default")
		require.Eventually(t, func() bool {
			_, err := access.List(ctx, metav1.ListOptions{Limit: 1})
			return err == nil
		}, 5*time.Second, 20*time.Millisecond, "API RoleBinding should become effective")
		public := newService(t, identity)
		created, err := public.CreateCluster(ctx, storageCreate(t, "least-privilege"))
		require.NoError(t, err)
		_, err = public.GetCluster(ctx, &clusterv1.GetClusterRequest{Name: created.Name})
		require.NoError(t, err)
		_, err = public.ListClusters(ctx, &clusterv1.ListClustersRequest{})
		require.NoError(t, err)
		_, err = public.ReplaceCluster(ctx, replacement(created.Name, created.ResourceVersion))
		require.NoError(t, err)
		stored, err := access.Get(ctx, created.Name, metav1.GetOptions{})
		require.NoError(t, err)
		for _, forbidden := range []struct {
			name string
			call func() error
		}{
			{name: "update", call: func() error { _, err := access.Update(ctx, stored, metav1.UpdateOptions{}); return err }},
			{name: "delete", call: func() error { return access.Delete(ctx, created.Name, metav1.DeleteOptions{}) }},
			{name: "status update", call: func() error {
				_, err := access.UpdateStatus(ctx, stored, metav1.UpdateOptions{})
				return err
			}},
			{name: "status patch", call: func() error {
				_, err := access.Patch(ctx, created.Name, types.MergePatchType,
					[]byte(`{"status":{}}`), metav1.PatchOptions{}, "status")
				return err
			}},
			{name: "other namespace", call: func() error {
				_, err := scoped.Resource(resourceGVR()).Namespace("other").Get(ctx, created.Name, metav1.GetOptions{})
				return err
			}},
			{name: "cluster-wide list", call: func() error {
				_, err := scoped.Resource(resourceGVR()).List(ctx, metav1.ListOptions{})
				return err
			}},
		} {
			t.Run(forbidden.name, func(t *testing.T) { assert.True(t, apierrors.IsForbidden(forbidden.call())) })
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err := api.GetCluster(canceled, &clusterv1.GetClusterRequest{Name: "roundtrip"})
		assert.Equal(t, codes.Canceled, status.Code(err))
	})
}

// storageCreate builds valid public input with a unique name for independent persistence scenarios.
func storageCreate(t *testing.T, name string) *clusterv1.CreateClusterRequest {
	t.Helper()
	request := new(clusterv1.CreateClusterRequest)
	data := fmt.Sprintf(`{"name":%q,"spec":{
		"control_plane_class":"standard","release":{"channel":{"name":"stable"}},
		"cluster_network":{"pods":{"cidr_blocks":["10.244.0.0/16"]},"services":{"cidr_blocks":["10.96.0.0/12"]}}
	}}`, name)
	require.NoError(t, protojson.Unmarshal([]byte(data), request))
	return request
}

// resourceGVR locates the internal resource so tests can compare public responses with persisted state.
func resourceGVR() schema.GroupVersionResource {
	return internalv1.SchemeGroupVersion.WithResource("nvkeclusters")
}

// installAPIIdentity loads the shipped permissions so tests detect accidental privilege expansion.
func installAPIIdentity(t *testing.T, ctx context.Context, client dynamic.Interface) {
	t.Helper()
	manifest, err := os.Open(filepath.Join("..", "..", "..", "config", "api", "state", "identity.yaml"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manifest.Close()) })
	decoder := yaml.NewYAMLOrJSONDecoder(manifest, 4096)
	for {
		var object unstructured.Unstructured
		err := decoder.Decode(&object)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		object.SetNamespace("default")
		if object.GetKind() == "RoleBinding" {
			subjects, _, err := unstructured.NestedSlice(object.Object, "subjects")
			require.NoError(t, err)
			for _, subject := range subjects {
				subject.(map[string]any)["namespace"] = "default"
			}
			require.NoError(t, unstructured.SetNestedSlice(object.Object, subjects, "subjects"))
		}
		gvr, _ := meta.UnsafeGuessKindToResource(object.GroupVersionKind())
		_, err = client.Resource(gvr).Namespace("default").Create(ctx, &object, metav1.CreateOptions{})
		require.NoError(t, err)
	}
}
