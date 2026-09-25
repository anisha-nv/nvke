// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package db

// ControlPlaneScaleDownRanker is implemented by control-plane providers that
// express an ordered placement preference, so scale-down can shed the
// least-preferred node first. A provider participates purely by implementing
// this interface; one that expresses no preference (e.g. a single-template
// CAPI/infra path) does not implement it, and scale-down falls back to age.
//
// This is the extension point for new providers: implement ScaleDownRank and
// add the provider to activeProvider — the control-plane package never learns
// the provider's name or key type, only the ordering.
type ControlPlaneScaleDownRanker interface {
	// ScaleDownRank returns the node's shed priority within this config: a
	// higher value is shed sooner. A node placed on the primary (most-preferred)
	// type ranks 0; each less-preferred type ranks higher; a node placed off the
	// preference list (or not yet placed) ranks highest, so an unexpected type is
	// shed before any preferred one. Rank 0 is therefore the primary type against
	// which a rebalance probe compares.
	ScaleDownRank(node Node) int
}

// ScaleDownRanker returns the active provider's scale-down ranker, or nil when
// the provider expresses no placement preference. A nil ranker is a valid
// input to the selector, which then orders purely by age and ID.
func (c ControlPlaneConfigSpec) ScaleDownRanker() ControlPlaneScaleDownRanker {
	if ranker, ok := c.activeProvider().(ControlPlaneScaleDownRanker); ok {
		return ranker
	}
	return nil
}

// ScaleDownRank ranks a node by its instance type's position in the provider's
// ordered preference: the index of the node's type (0 = most preferred, shed
// last), or len(preference) for a type that is off the list or not yet placed
// (shed first). It satisfies ControlPlaneScaleDownRanker.
func (p NicoControlPlaneProvider) ScaleDownRank(node Node) int {
	id := node.NicoInstanceTypeID()
	for i, pref := range p.InstanceTypePreference {
		if pref == id {
			return i
		}
	}
	return len(p.InstanceTypePreference)
}
