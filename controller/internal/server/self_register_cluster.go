// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"go.temporal.io/sdk/client"

	"github.com/dsx-ai-factory/nke/controller/internal/constants"
	"github.com/dsx-ai-factory/nke/controller/internal/db"
)

// selfRegisterClusterRequest carries an already-provisioned cluster's DB rows,
// imported wholesale (preserving every ID) from another CWE instance's
// /api/clusters{,/nodePools,/nodes} responses. This is the data-import half of
// self-registration; the SelfRegisterCluster Temporal workflow it enqueues is
// the live-K8s-object half.
type selfRegisterClusterRequest struct {
	Cluster                   db.Cluster                    `json:"cluster"`
	ControlPlaneConfiguration *db.ControlPlaneConfiguration `json:"controlPlaneConfiguration,omitempty"`
	Nodes                     []db.Node                     `json:"nodes"`
}

// selfRegisterClusterHandler is a prototype endpoint, registered directly on
// the raw mux (not generated from openapi.yaml): POST /api/self-register-cluster.
// It writes the cluster/control-plane-configuration/node rows with their
// IDs preserved from the request, then enqueues SelfRegisterCluster, which
// recreates the live CAPI objects (Cluster, ProvisionedCluster, Machine,
// ProvisionedMachine) without touching the real nodes.
func selfRegisterClusterHandler(database db.DB, temporalClient client.Client, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req selfRegisterClusterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			handleError(w, logger, fmt.Sprintf("invalid request body: %s", err), http.StatusBadRequest)
			return
		}
		if req.Cluster.ID == uuid.Nil {
			handleError(w, logger, "cluster.id is required", http.StatusBadRequest)
			return
		}

		ctx := r.Context()
		err := database.RunInTx(ctx, func(ctx context.Context, tx db.Tx) error {
			// clusters.configuration_id and control_plane_configurations.cluster_id
			// reference each other, so the cluster row is created first with its
			// configuration_id left at the request's original value, the config row
			// is created second (now that cluster_id resolves), and the cluster is
			// updated afterward if that first value didn't already match -- same
			// three-step order the normal CreateCluster handler uses.
			configurationID := req.Cluster.ControlPlaneConfig.ConfigurationID
			req.Cluster.ControlPlaneConfig.ConfigurationID = uuid.Nil
			if _, err := tx.CreateCluster(ctx, &req.Cluster); err != nil {
				return fmt.Errorf("failed to create cluster: %w", err)
			}

			if req.ControlPlaneConfiguration != nil {
				if _, err := tx.CreateControlPlaneConfiguration(ctx, req.ControlPlaneConfiguration); err != nil {
					return fmt.Errorf("failed to create control plane configuration: %w", err)
				}
				req.Cluster.ControlPlaneConfig.ConfigurationID = configurationID
				if _, err := tx.UpdateCluster(ctx, &req.Cluster); err != nil {
					return fmt.Errorf("failed to set cluster configuration id: %w", err)
				}
			}

			for i := range req.Nodes {
				node := req.Nodes[i]
				node.ClusterID = req.Cluster.ID
				if _, err := tx.CreateNode(ctx, &node); err != nil {
					return fmt.Errorf("failed to create node %s: %w", node.Name, err)
				}
			}

			workflowID := constants.SelfRegisterClusterWorkflowPrefix + req.Cluster.ID.String()
			if _, err := tx.CreateWorkflowOutboxEntry(ctx, "SelfRegisterCluster", workflowID, map[string]interface{}{
				"clusterID": req.Cluster.ID,
			}); err != nil {
				return fmt.Errorf("failed to create workflow outbox entry: %w", err)
			}

			return nil
		})
		if err != nil {
			logger.Error("self-register cluster failed", "error", err)
			handleError(w, logger, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"clusterID": req.Cluster.ID.String()})
	}
}
