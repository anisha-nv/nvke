// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/dsx-ai-factory/nke/controller/internal/db"
	"github.com/dsx-ai-factory/nke/controller/internal/nodes"
)

// reconcileControlPlaneScaleDown removes control-plane nodes when the cluster's
// desired count has dropped below the number of healthy nodes. It only records
// the delete intent; reconcileRequestedDeletes promotes it into a teardown, one
// node at a time, so a scale-down drains through the same serialized path as a
// user delete, a remediation, or a node-provisioning-monitor delete.
//
// It is the last intent-recorder in the reconcile: scale-up, remediation and
// rollout all run first and grow before shrink, so the control plane never
// shrinks toward desired while it still owes the cluster a replacement.
func (r *reconciler) reconcileControlPlaneScaleDown(ctx context.Context, cluster db.Cluster, cp *controlPlane) error {
	if !r.cfg.UseControlPlaneControllerForControlPlane() {
		return nil
	}

	logger := r.logger.With(
		"clusterID", cluster.ID,
		"clusterName", cluster.Name,
		"desiredCount", cluster.ControlPlaneConfig.DesiredCount,
	)

	// Shrinking while a rollout is actively replacing nodes would fight it for
	// the same slots. Drift with recycling disabled is not a rollout, so it does
	// not block: a lower desired count still takes effect.
	if cp.upgradeInProgress(cluster) {
		return nil
	}

	// Surplus is measured over healthy, joined, eligible nodes only. Anything
	// mid-provision or mid-delete is excluded, so it is never counted as removable
	// and the surplus falls by one each time a recorded delete lands -- the loop
	// paces itself to one intent per reconcile without a separate in-flight guard.
	eligible := cp.scaleDownEligible()
	surplus := len(eligible) - cluster.ControlPlaneConfig.DesiredCount
	if surplus <= 0 {
		return nil
	}

	// Never shrink below the fault-tolerant floor even when desired is lower:
	// removing another node would leave etcd unable to survive a failure. Hold at
	// the floor and stop here. The delete path's ensureControlPlaneQuorumFloor is
	// the authoritative rule; this pre-check just avoids selecting a victim it
	// would refuse. A desired count below the floor is an unsupported request, so
	// a log is enough to explain the resulting desired-vs-actual gap.
	if len(eligible)-1 < nodes.MinFaultTolerantControlPlaneNodes {
		logger.Warn("Control-plane scale-down held at the fault-tolerant floor",
			"eligible", len(eligible),
			"minFaultTolerant", nodes.MinFaultTolerantControlPlaneNodes,
		)
		return nil
	}

	victim := selectControlPlaneNodeToScaleDown(eligible, cluster.ControlPlaneConfig.ScaleDownRanker())
	nodeLogger := logger.With("nodeID", victim.ID, "nodeName", victim.Name)

	// Same path as every other control-plane delete: record intent and let
	// reconcileRequestedDeletes start the teardown. The quorum floor is NOT
	// skipped -- this is a real shrink, not a replacement -- so the record-time
	// floor is the authoritative guard, accounting for any deletes already queued.
	if err := nodes.DeleteWithTx(ctx, r.db, victim.ID,
		nodes.WithReason("Control-plane scale-down: desired count decreased"),
		nodes.WithAllowCPNode(true),
		// The controller enforces its own preconditions above; skip the live-API
		// guard (also avoids SELECT FOR UPDATE, which SQLite unit tests cannot run).
		nodes.WithSkipLiveDeleteGuard(),
		nodes.WithControlPlaneControllerDelete(true),
	); err != nil {
		// The record-time floor can still refuse the delete if the live count is
		// tighter than the in-memory snapshot. That is the authoritative guard
		// doing its job, not a reconcile failure: log and let the next pass
		// re-evaluate once the state settles.
		var floorErr *nodes.ControlPlaneQuorumFloorError
		if errors.As(err, &floorErr) {
			nodeLogger.Warn("Control-plane scale-down refused by the quorum floor at record time", "err", err)
			return nil
		}
		return fmt.Errorf("recording control-plane scale-down delete for node %s (%s): %w", victim.ID, victim.Name, err)
	}

	nodeLogger.Info("Recorded control-plane scale-down delete; teardown will start one node at a time",
		"eligible", len(eligible),
		"surplus", surplus,
	)
	return nil
}

// upgradeInProgress reports whether the control plane is actively replacing
// nodes: either recycling is marked in flight, or recycling is enabled and there
// is drift left to roll. Drift alone with recycling disabled is deliberately not
// a rollout -- nothing tears those nodes down, so a scale-down must not defer to
// it forever.
func (c *controlPlane) upgradeInProgress(cluster db.Cluster) bool {
	if cluster.Conditions.CPNodesRecycling.Status == db.ConditionStatusTrue {
		return true
	}
	return cluster.EnableCPNodeRecycling && c.countNodesNeedingRollout() > 0
}

// scaleDownEligible returns the control-plane-controller nodes that are safe to
// remove for a desired-count shrink: fully healthy, joined, and not already on
// their way out or in for replacement. Basing the surplus on this set makes it
// self-limiting -- a node whose delete has been recorded drops out on the next
// reconcile, so the count converges one node at a time.
func (c *controlPlane) scaleDownEligible() []db.Node {
	var eligible []db.Node
	for _, node := range c.nodes {
		if !node.IsManagedByControlPlaneController() {
			continue
		}
		// A node on its way out (recorded or tearing down) or in for replacement
		// is not removable surplus.
		if nodes.IsDeleteInFlight(node) || isMarkedForRecycling(node) {
			continue
		}
		// Only a fully healthy, joined member is a clean removal. Anything still
		// coming up is left for a later reconcile once it settles.
		if node.Conditions.Joined.Status != db.ConditionStatusTrue {
			continue
		}
		if isPending(node) || !isProvisioned(node) || !isKubernetesReady(node) || !isNodeAgentReady(node) {
			continue
		}
		eligible = append(eligible, node)
	}
	return eligible
}

// primaryControlPlaneRank is the ScaleDownRank of a node placed on the primary
// (most-preferred) type. The ControlPlaneScaleDownRanker contract fixes it at 0,
// so any rank above it means the node sits on a less-preferred (or off-list)
// type.
const (
	primaryControlPlaneRank = 0
)

// controlPlaneHasNodeBelowPrimaryRank reports whether any eligible control-plane
// node sits below the primary type -- i.e. carries a ScaleDownRank above the
// primary rank. It is the steady-state rebalance probe: a true result means the
// control plane is running at least one less-preferred node that a preferred
// replacement could improve, without changing the node count.
//
// It only detects that a lower-ranked node exists. Whether a more-preferred type
// is actually schedulable is a capacity question owned by the node scheduler, so
// acting on this signal must surge-then-shed and verify the replacement is
// strictly better-ranked before removing the old node, or it would churn. A nil
// ranker (a provider expressing no preference, e.g. the CAPI/infra path) never
// reports a candidate.
//
// Rebalance follow-up (Refs NKX-15398): the reconciler will use this probe as
// the steady-state (desired == count) trigger for a surge-then-shed rebalance,
// reusing the existing scale-up and scale-down paths rather than adding new
// deletion logic:
//  1. when a below-primary node exists AND the scheduler confirms a higher-ranked
//     placement is available, reconcileControlPlaneScaleUp surges one replacement,
//     bounded by maxSurge and the quorum floor;
//  2. once the replacement joins, reconcileControlPlaneScaleDown sees the surplus
//     and selectControlPlaneNodeToScaleDown removes the least-preferred (highest
//     rank) node -- the one being replaced, never the fresh primary node.
//
// The strict-improvement gate in step 1 is what prevents churn ("race to the
// bottom") when the primary type has no spare capacity.
func controlPlaneHasNodeBelowPrimaryRank(eligible []db.Node, ranker db.ControlPlaneScaleDownRanker) bool {
	if ranker == nil {
		return false
	}
	for _, node := range eligible {
		if ranker.ScaleDownRank(node) > primaryControlPlaneRank {
			return true
		}
	}
	return false
}

// selectControlPlaneNodeToScaleDown picks which surplus node to remove. It
// prefers to shed the least-preferred node first (higher ScaleDownRank), then
// the oldest node, then the lowest ID for a stable choice across reconciles.
// The ranker is provider-agnostic: any provider expressing a placement
// preference supplies the ordering, and a nil ranker (a provider that expresses
// none, e.g. the CAPI/infra path) collapses the selection to oldest-then-ID.
//
// Failure-domain-aware selection (spreading the survivors across racks/zones) is
// intentionally out of scope here; it would need topology the control-plane
// config does not yet carry, and the quorum floor already bounds how far a
// scale-down can go.
func selectControlPlaneNodeToScaleDown(eligible []db.Node, ranker db.ControlPlaneScaleDownRanker) db.Node {
	rankOf := func(node db.Node) int {
		if ranker == nil {
			return 0
		}
		return ranker.ScaleDownRank(node)
	}

	sorted := make([]db.Node, len(eligible))
	copy(sorted, eligible)
	sort.Slice(sorted, func(i, j int) bool {
		if ri, rj := rankOf(sorted[i]), rankOf(sorted[j]); ri != rj {
			return ri > rj // least preferred (higher rank) first
		}
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt) // oldest first
		}
		return sorted[i].ID.String() < sorted[j].ID.String()
	})
	return sorted[0]
}
