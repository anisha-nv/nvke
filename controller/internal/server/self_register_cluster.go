// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/dsx-ai-factory/nke/controller/api"
	"github.com/dsx-ai-factory/nke/controller/internal/constants"
	"github.com/dsx-ai-factory/nke/controller/internal/db"
)

type selfRegisterConflictError struct {
	existingClusterID uuid.UUID
}

func (e *selfRegisterConflictError) Error() string {
	return fmt.Sprintf("this CWE instance already has cluster %s registered; only one cluster may be self-registered", e.existingClusterID)
}

func (s *clusterService) SelfRegisterCluster(
	ctx context.Context,
	req api.SelfRegisterClusterRequestObject,
) (api.SelfRegisterClusterResponseObject, error) {
	if req.Body == nil {
		return api.SelfRegisterCluster400JSONResponse{Message: "request body is required"}, nil
	}

	dbCluster, err := dbClusterFromSelfRegisterAPI(req.Body.Cluster)
	if err != nil {
		return api.SelfRegisterCluster400JSONResponse{Message: err.Error()}, nil
	}

	var cpc *db.ControlPlaneConfiguration
	if req.Body.Cluster.ControlPlaneConfig != nil && req.Body.Cluster.ControlPlaneConfig.ConfigurationID != uuid.Nil {
		row, err := dbControlPlaneConfigurationFromSelfRegisterAPI(req.Body.Cluster)
		if err != nil {
			return api.SelfRegisterCluster400JSONResponse{Message: err.Error()}, nil
		}
		cpc = &row
	}

	dbNodes := make([]db.Node, 0, len(req.Body.Nodes))
	for _, n := range req.Body.Nodes {
		dbNode, err := dbNodeFromSelfRegisterAPI(n, dbCluster.ID)
		if err != nil {
			return api.SelfRegisterCluster400JSONResponse{Message: err.Error()}, nil
		}
		dbNodes = append(dbNodes, dbNode)
	}

	var registeredCluster db.Cluster
	err = s.db.RunInTx(ctx, func(ctx context.Context, tx db.Tx) error {
		existing, err := tx.ListClusters(ctx, db.ClusterFilters{})
		if err != nil {
			return fmt.Errorf("failed to check for an existing cluster: %w", err)
		}
		if len(existing) > 0 {
			if existing[0].ID != dbCluster.ID {
				return &selfRegisterConflictError{existingClusterID: existing[0].ID}
			}
			// Retry of an already-committed registration: idempotent, not a conflict.
			registeredCluster, err = tx.GetCluster(ctx, dbCluster.ID)
			if err != nil {
				return fmt.Errorf("failed to reload existing cluster: %w", err)
			}
			return nil
		}

		// clusters.configuration_id and control_plane_configurations.cluster_id
		// reference each other, so create cluster -> create config -> update cluster.
		configurationID := dbCluster.ControlPlaneConfig.ConfigurationID
		dbCluster.ControlPlaneConfig.ConfigurationID = uuid.Nil
		if _, err := tx.CreateCluster(ctx, &dbCluster); err != nil {
			return fmt.Errorf("failed to create cluster: %w", err)
		}

		if cpc != nil {
			if _, err := tx.CreateControlPlaneConfiguration(ctx, cpc); err != nil {
				return fmt.Errorf("failed to create control plane configuration: %w", err)
			}
			dbCluster.ControlPlaneConfig.ConfigurationID = configurationID
			if _, err := tx.UpdateCluster(ctx, &dbCluster); err != nil {
				return fmt.Errorf("failed to set cluster configuration id: %w", err)
			}
		}

		for i := range dbNodes {
			if _, err := tx.CreateNode(ctx, &dbNodes[i]); err != nil {
				return fmt.Errorf("failed to create node %s: %w", dbNodes[i].Name, err)
			}
		}

		workflowID := constants.SelfRegisterClusterWorkflowPrefix + dbCluster.ID.String()
		if _, err := tx.CreateWorkflowOutboxEntry(ctx, "SelfRegisterCluster", workflowID, map[string]interface{}{
			"clusterID": dbCluster.ID,
		}); err != nil {
			return fmt.Errorf("failed to create workflow outbox entry: %w", err)
		}

		registeredCluster, err = tx.GetCluster(ctx, dbCluster.ID)
		if err != nil {
			return fmt.Errorf("failed to reload registered cluster: %w", err)
		}
		return nil
	})
	if err != nil {
		var conflictErr *selfRegisterConflictError
		if errors.As(err, &conflictErr) {
			return api.SelfRegisterCluster409JSONResponse{Message: conflictErr.Error()}, nil
		}
		return nil, err
	}

	return api.SelfRegisterCluster201JSONResponse(APIClusterFromDB(&registeredCluster)), nil
}

func dbClusterFromSelfRegisterAPI(c api.Cluster) (db.Cluster, error) {
	if c.ID == uuid.Nil {
		return db.Cluster{}, errors.New("cluster.id is required")
	}

	out := db.Cluster{
		ID:                c.ID,
		Name:              c.Name,
		CniPlugin:         db.CniPlugin(c.CniPlugin),
		CniVersion:        c.CniVersion,
		EtcdEncrypted:     c.EtcdEncrypted,
		KubernetesVersion: c.ControlPlaneKubernetesVersion,
		PodCIDR:           c.Network.PodCIDR,
		ServiceCIDR:       c.Network.ServiceCIDR,
		CreatedAt:         c.CreatedAt,
		UpdatedAt:         c.CreatedAt,
	}
	if c.ControlPlaneVIP != nil {
		out.ControlPlaneVIP = *c.ControlPlaneVIP
	}
	if c.EnableCPNodeRecycling != nil {
		out.EnableCPNodeRecycling = *c.EnableCPNodeRecycling
	}
	if c.TeleportEnabled != nil {
		out.TeleportEnabled = *c.TeleportEnabled
	}
	if err := reencode(c.Conditions, &out.Conditions); err != nil {
		return db.Cluster{}, fmt.Errorf("cluster.conditions: %w", err)
	}

	// Self-registration is for static (pre-provisioned, CAPI-backed) nodes
	// only; the Nico use case is not supported.
	if c.Infra == nil || c.Nico != nil {
		return db.Cluster{}, errors.New("self-registration only supports CAPI-backed clusters (cluster.infra is required, cluster.nico is rejected)")
	}
	out.ProviderType = db.ProviderTypeCAPI
	infraCluster, err := json.Marshal(c.Infra.Cluster)
	if err != nil {
		return db.Cluster{}, fmt.Errorf("cluster.infra.cluster: %w", err)
	}
	out.Capi = &db.CAPIClusterProvider{InfraCluster: infraCluster}

	if c.ControlPlaneConfig != nil {
		out.ControlPlaneConfig.ConfigurationID = c.ControlPlaneConfig.ConfigurationID
		out.ControlPlaneConfig.DesiredCount = c.ControlPlaneConfig.DesiredCount
		if c.ControlPlaneConfig.MaxSurge != nil {
			out.ControlPlaneConfig.MaxSurge = *c.ControlPlaneConfig.MaxSurge
		}
	}

	return out, nil
}

func dbControlPlaneConfigurationFromSelfRegisterAPI(c api.Cluster) (db.ControlPlaneConfiguration, error) {
	cfg := c.ControlPlaneConfig
	spec := db.ControlPlaneConfigSpec{
		KubernetesVersion:    c.ControlPlaneKubernetesVersion,
		SystemReservedMemory: cfg.SystemReservedMemory,
		NodeLabels:           cfg.NodeLabels,
	}
	if cfg.TeleportEnabled != nil {
		spec.TeleportEnabled = *cfg.TeleportEnabled
	}
	if cfg.ProvisioningMonitorDisabled != nil {
		spec.ProvisioningMonitorDisabled = *cfg.ProvisioningMonitorDisabled
	}

	if cfg.Infra != nil {
		capi := &db.CAPIControlPlaneProvider{}
		if cfg.Infra.MachineTemplate != nil {
			mt, err := json.Marshal(cfg.Infra.MachineTemplate)
			if err != nil {
				return db.ControlPlaneConfiguration{}, fmt.Errorf("cluster.controlPlaneConfig.infra.machineTemplate: %w", err)
			}
			capi.InfraMachineTemplatePreferences = []json.RawMessage{mt}
		}
		if err := reencode(cfg.Infra.BootstrapFiles, &capi.BootstrapFiles); err != nil {
			return db.ControlPlaneConfiguration{}, fmt.Errorf("cluster.controlPlaneConfig.infra.bootstrapFiles: %w", err)
		}
		capi.PreKubeadmCommands = cfg.Infra.PreKubeadmCommands
		spec.Capi = capi
	}

	return db.ControlPlaneConfiguration{
		ID:           cfg.ConfigurationID,
		ClusterID:    c.ID,
		Config:       spec,
		ChangeReason: "self-register",
		CreatedAt:    c.CreatedAt,
	}, nil
}

func dbNodeFromSelfRegisterAPI(n api.Node, clusterID uuid.UUID) (db.Node, error) {
	if n.ID == uuid.Nil {
		return db.Node{}, errors.New("node.id is required")
	}
	// node_pool_id/node_set_id are FKs this request has no rows for.
	if n.NodePoolID != uuid.Nil || n.NodeSetID != uuid.Nil {
		return db.Node{}, fmt.Errorf("node %s: self-registration does not support node-pool-managed nodes (nodePoolID/nodeSetID must be unset)", n.Name)
	}
	if n.Nico != nil {
		return db.Node{}, fmt.Errorf("node %s: self-registration does not support Nico-provider nodes", n.Name)
	}

	out := db.Node{
		ID:                     n.ID,
		NodePoolID:             n.NodePoolID,
		NodeSetID:              n.NodeSetID,
		ClusterID:              clusterID,
		ControlPlane:           n.ControlPlane,
		Name:                   n.Name,
		IP:                     n.IP,
		CreatedAt:              time.Now(),
		UpdatedAt:              time.Now(),
		SpireAttestationMethod: db.SpireAttestationMethodTPM,
	}
	if err := reencode(n.Conditions, &out.Conditions); err != nil {
		return db.Node{}, fmt.Errorf("node %s conditions: %w", n.Name, err)
	}
	if n.ControlPlaneRollout != nil {
		out.ControlPlaneConfigurationID = n.ControlPlaneRollout.PinnedConfigurationID
	}

	return out, nil
}

func reencode(src, dst interface{}) error {
	raw, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}
