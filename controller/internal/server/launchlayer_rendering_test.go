// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dsx-ai-factory/nke/controller/api"
	"github.com/dsx-ai-factory/nke/controller/internal/capi"
	"github.com/dsx-ai-factory/nke/controller/internal/config"
	"github.com/dsx-ai-factory/nke/controller/internal/db"
	"github.com/dsx-ai-factory/nke/controller/internal/temporal/activities"
	"github.com/dsx-ai-factory/nke/controller/internal/testutil"
)

type typedLaunchLayerApplyRecorder struct {
	client.Client
	objects []client.Object
}

func (r *typedLaunchLayerApplyRecorder) Patch(_ context.Context, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
	r.objects = append(r.objects, obj.DeepCopyObject().(client.Object))
	return nil
}

func (r *typedLaunchLayerApplyRecorder) Get(_ context.Context, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	return apierrors.NewNotFound(schema.GroupResource{Group: "cluster.x-k8s.io", Resource: "clusters"}, key.Name)
}

func TestTypedLaunchLayerHTTPPersistenceThroughProductionRendering(t *testing.T) {
	for _, controllerMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("controller=%t", controllerMode), func(t *testing.T) {
			body := launchLayerCreateRequest(controllerMode, false)
			customize := func(p *api.LaunchLayerNodePoolProvider) {
				p.SpireAttestationMethod = "tpm"
				p.BootstrapFiles = &[]api.CloudInitFile{{Path: "/etc/example", Content: "example-content"}}
				p.PreKubeadmCommands = &[]string{"echo example-command"}
			}
			for i := range *body.NodePools {
				customize((*body.NodePools)[i].Launchlayer)
			}
			if controllerMode {
				customize(body.ControlPlaneConfig.Launchlayer)
			}
			tx := &launchLayerRecordingTx{}
			database := &testutil.MockDB{GetTx: func() db.Tx { return tx }}
			database.On("RunInTx", mock.Anything, mock.Anything).Return(nil).Once()
			response := launchLayerHTTP(t, launchLayerHandler(t, database, controllerMode), http.MethodPost, "/api/clusters", body)
			require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
			cluster := tx.clusters[0]
			if controllerMode {
				cluster.ControlPlaneConfig.ControlPlaneConfigSpec = tx.configs[0].Config
			}
			recorder := &typedLaunchLayerApplyRecorder{}
			require.NoError(t, capi.ApplyClusterScaffold(t.Context(), recorder, cluster, false))
			require.Len(t, recorder.objects, 2)
			renderedCluster := recorder.objects[0].(*unstructured.Unstructured)
			require.Equal(t, "LaunchLayerCluster", renderedCluster.GetKind())
			var canonical map[string]any
			require.NoError(t, json.Unmarshal(cluster.Capi.InfraCluster, &canonical))
			require.Equal(t, canonical["spec"], renderedCluster.Object["spec"])
			// Existing pools exercise worker and legacy control-plane nodes; the extra
			// node exercises a pinned controller-based control-plane snapshot.
			count := len(tx.pools)
			if controllerMode {
				count++
			}
			for i := range count {
				for sequence := range 2 {
					node := db.Node{ID: uuid.New(), ClusterID: cluster.ID, Name: fmt.Sprintf("node-%d-%d", i, sequence)}
					queries := &testutil.MockDB{}
					var expected json.RawMessage
					if i < len(tx.pools) {
						pool := tx.pools[i]
						set := db.NodeSet{ID: uuid.New(), NodePoolID: pool.ID, KubernetesVersion: cluster.KubernetesVersion}
						set.SetCapi(*pool.Capi)
						node.NodePoolID = pool.ID
						node.NodeSetID = set.ID
						node.ControlPlane = pool.ControlPlane
						queries.On("GetNodeSet", mock.Anything, set.ID).Return(set, nil)
						queries.On("GetNodePool", mock.Anything, pool.ID).Return(pool, nil)
						expected = set.Capi.InfraMachineTemplate
					} else {
						node.ControlPlane = true
						node.ControlPlaneConfigurationID = tx.configs[0].ID
						queries.On("GetControlPlaneConfiguration", mock.Anything, tx.configs[0].ID).Return(tx.configs[0], nil)
						expected = tx.configs[0].Config.Capi.InfraMachineTemplatePreferences[0]
					}
					queries.On("GetNode", mock.Anything, node.ID).Return(node, nil)
					queries.On("GetCluster", mock.Anything, cluster.ID).Return(cluster, nil)
					applied := &typedLaunchLayerApplyRecorder{}
					activity := activities.NewActivities(config.Config{SpiffeEnabled: false}, queries, applied, nil, "", nil)
					require.NoError(t, activity.CreateCAPIInfraMachineForNode(t.Context(), activities.CreateCAPIInfraMachineForNodeInput{NodeID: node.ID}))
					require.Len(t, applied.objects, 3)
					machine := applied.objects[0].(*unstructured.Unstructured)
					var template map[string]any
					require.NoError(t, json.Unmarshal(expected, &template))
					require.Equal(t, launchLayerSpec(template), machine.Object["spec"])
					require.Equal(t, "LaunchLayerMachine", machine.GetKind())
					secret := applied.objects[1].(*corev1.Secret)
					require.Contains(t, string(secret.Data["value"]), "example-content")
					require.Contains(t, string(secret.Data["value"]), "example-command")
					coreMachine := applied.objects[2].(*clusterv1.Machine)
					require.Equal(t, secret.Name, *coreMachine.Spec.Bootstrap.DataSecretName)
					require.Equal(t, machine.GetName(), coreMachine.Spec.InfrastructureRef.Name)
				}
			}
		})
	}
}
