// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/NVIDIA/nke/controller/api"
	"github.com/NVIDIA/nke/controller/internal/capi"
	"github.com/NVIDIA/nke/controller/internal/capi/validation"
	"github.com/NVIDIA/nke/controller/internal/config"
	"github.com/NVIDIA/nke/controller/internal/constants"
	"github.com/NVIDIA/nke/controller/internal/db"
)

// Captures production apply objects without Kubernetes admission or any network.
// Every other client method is deliberately unavailable.
type launchLayerApplyRecorder struct {
	client.Client
	objects []client.Object
}

func (r *launchLayerApplyRecorder) Patch(_ context.Context, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
	r.objects = append(r.objects, obj.DeepCopyObject().(client.Object))
	return nil
}

func TestLaunchLayerStoredIntentRendersClusterMachinesAndBootstrap(t *testing.T) {
	data, err := os.ReadFile("../../../../docs/examples/generic-launchlayer-cluster.json")
	require.NoError(t, err)
	var request api.CreateClusterRequest
	require.NoError(t, json.Unmarshal(data, &request))
	require.NoError(t, validation.LaunchLayerCluster(request.Infra.Cluster, "infra.cluster"))
	// A caller's resource identity is nonauthoritative. Cluster metadata survives;
	// template-level metadata is not implicitly copied into each Machine.
	request.Infra.Cluster["metadata"] = map[string]any{
		"name": "caller-name", "namespace": "caller-namespace",
		"labels": map[string]any{"example.org/cluster": "retained"},
	}
	raw, err := json.Marshal(request.Infra.Cluster)
	require.NoError(t, err)
	cluster := db.Cluster{ID: uuid.New(), Name: request.Name, KubernetesVersion: request.ControlPlaneKubernetesVersion}
	cluster.SetCAPIProvider(db.CAPIClusterProvider{InfraCluster: raw})
	recorder := &launchLayerApplyRecorder{}
	require.NoError(t, capi.ApplyClusterScaffold(t.Context(), recorder, cluster, false))
	require.Len(t, recorder.objects, 2)
	infraCluster := recorder.objects[0].(*unstructured.Unstructured)
	require.Equal(t, request.Infra.Cluster["spec"], infraCluster.Object["spec"])
	require.Equal(t, cluster.Name, infraCluster.GetName())
	require.Equal(t, constants.NamespaceNKESystem, infraCluster.GetNamespace())
	require.Equal(t, "retained", infraCluster.GetLabels()["example.org/cluster"])
	capiCluster := recorder.objects[1].(*clusterv1.Cluster)
	require.Equal(t, "LaunchLayerCluster", capiCluster.Spec.InfrastructureRef.Kind)
	require.Equal(t, cluster.Name, capiCluster.Spec.InfrastructureRef.Name)

	for i, pool := range *request.NodePools {
		template := pool.Infra.MachineTemplate
		template["metadata"] = map[string]any{
			"name": "caller-template", "labels": map[string]any{"example.org/template": "not-propagated"},
		}
		require.NoError(t, validation.LaunchLayerMachineTemplate(template, "infra.machineTemplate"))
		manifest, err := json.Marshal(template)
		require.NoError(t, err)
		var nodeSet db.NodeSet
		nodeSet.SetCapi(db.CAPINodePoolProvider{
			InfraMachineTemplate: manifest, SpireAttestationMethod: db.SpireAttestationMethodTPM,
			BootstrapFiles:     []db.CloudInitFile{{Path: "/etc/example.conf", Content: "example-content"}},
			PreKubeadmCommands: []string{"echo example-command"},
		})
		// First and subsequent nodes use the exact production GenerateTemplate path.
		for sequence := range 2 {
			recorder := &launchLayerApplyRecorder{}
			a := &Activities{k8sClient: recorder, cfg: config.Config{SpiffeEnabled: false}}
			createCtx := &capiMachineCreateContext{
				cluster: cluster, namespace: constants.NamespaceNKESystem, nodeSet: nodeSet,
				nodePool: db.NodePool{ControlPlane: pool.ControlPlane},
				node:     db.Node{ID: uuid.New(), Name: fmt.Sprintf("example-%d-%d", i, sequence)},
			}
			require.NoError(t, a.createTemplateBackedCAPIMachineForNode(t.Context(), createCtx))
			require.Len(t, recorder.objects, 3)
			machine := recorder.objects[0].(*unstructured.Unstructured)
			require.Equal(t, validation.LaunchLayerAPIVersion, machine.GetAPIVersion())
			require.Equal(t, "LaunchLayerMachine", machine.GetKind())
			require.Equal(t, template["spec"].(map[string]any)["template"].(map[string]any)["spec"], machine.Object["spec"])
			require.Equal(t, createCtx.node.Name, machine.GetName())
			require.Equal(t, constants.NamespaceNKESystem, machine.GetNamespace())
			require.NotContains(t, machine.GetLabels(), "example.org/template")
			secret := recorder.objects[1].(*corev1.Secret)
			require.Contains(t, string(secret.Data["value"]), "/etc/example.conf")
			require.Contains(t, string(secret.Data["value"]), "example-command")
			coreMachine := recorder.objects[2].(*clusterv1.Machine)
			require.Equal(t, "LaunchLayerMachine", coreMachine.Spec.InfrastructureRef.Kind)
			require.Equal(t, machine.GetName(), coreMachine.Spec.InfrastructureRef.Name)
			require.Equal(t, secret.Name, *coreMachine.Spec.Bootstrap.DataSecretName)
		}
	}
}
