// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nico

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/dsx-ai-factory/nvke/controller/internal/clients/shared"
	"github.com/dsx-ai-factory/nvke/controller/internal/config"
)

func TestUnit_ValidateAndSetMachineProfileSelection(t *testing.T) {
	type parameters struct {
		existing      *clusterv1.Machine
		requestedName string
		enabled       bool
		wantError     string
	}
	tests := map[string]parameters{
		"new disabled Machine keeps legacy hooks for selected profile": {
			requestedName: "worker-profile",
		},
		"new disabled Machine without profile stays legacy": {},
		"new enabled Machine delegates empty profile": {
			enabled: true,
		},
		"new enabled Machine delegates selected profile": {
			requestedName: "worker-profile",
			enabled:       true,
		},
		"existing ungated Machine remains legacy after enablement": {
			existing:      profileTestMachine(nil, nil),
			requestedName: "worker-profile",
			enabled:       true,
		},
		"existing annotation-only Machine remains legacy after enablement": {
			existing:      profileTestMachine(map[string]string{configProfileAnnotation: "old-profile"}, nil),
			requestedName: "worker-profile",
			enabled:       true,
		},
		"existing delegated Machine survives disablement": {
			existing:      delegatedProfileTestMachine("worker-profile"),
			requestedName: "worker-profile",
		},
		"existing delegated Machine preserves empty profile": {
			existing: delegatedProfileTestMachine(""),
			enabled:  true,
		},
		"existing delegated Machine rejects profile change": {
			existing:      delegatedProfileTestMachine("worker-profile"),
			requestedName: "other-profile",
			wantError:     "conflicts with requested profile",
		},
		"existing delegated Machine rejects missing requested profile": {
			existing:  delegatedProfileTestMachine("worker-profile"),
			enabled:   true,
			wantError: "conflicts with requested profile",
		},
		"readiness gate without annotation fails closed": {
			existing:  gatedProfileTestMachineWithoutAnnotation(),
			wantError: "without a configuration profile annotation",
		},
	}

	for name, params := range tests {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, clusterv1.AddToScheme(scheme))
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if params.existing != nil {
				builder = builder.WithObjects(params.existing)
			}
			machine := profileTestMachine(nil, nil)
			err := validateAndSetMachineProfileSelection(context.Background(), builder.Build(), machine,
				config.Config{MachineConfigurationControllerEnabled: params.enabled}, params.requestedName)
			if params.wantError != "" {
				require.ErrorContains(t, err, params.wantError)
				return
			}
			require.NoError(t, err)
			if params.existing != nil {
				require.Equal(t, params.existing.Annotations[configProfileAnnotation], machine.Annotations[configProfileAnnotation])
				_, existingHasProfile := params.existing.Annotations[configProfileAnnotation]
				_, desiredHasProfile := machine.Annotations[configProfileAnnotation]
				require.Equal(t, existingHasProfile, desiredHasProfile)
				require.Empty(t, machine.Spec.ReadinessGates)
			} else if params.enabled {
				require.Equal(t, params.requestedName, machine.Annotations[configProfileAnnotation])
				require.Contains(t, machine.Annotations, configProfileAnnotation)
				require.NotContains(t, machine.Labels, configProfileAnnotation)
				require.Equal(t, []clusterv1.MachineReadinessGate{{ConditionType: configReadyGate}}, machine.Spec.ReadinessGates)
			} else {
				require.Empty(t, machine.Annotations[configProfileAnnotation])
				require.Empty(t, machine.Spec.ReadinessGates)
			}
		})
	}
}

func TestUnit_ApplyMachinePreservesProfileSelection(t *testing.T) {
	type parameters struct {
		existing *clusterv1.Machine
	}
	delegated := delegatedProfileTestMachine("worker-profile")
	delegated.Spec.ReadinessGates = append(delegated.Spec.ReadinessGates, clusterv1.MachineReadinessGate{ConditionType: "example.com/OtherReady"})
	tests := map[string]parameters{
		"delegated Machine keeps profile and other readiness gates": {existing: delegated},
		"delegated Machine keeps empty profile and gate":            {existing: delegatedProfileTestMachine("")},
		"annotation-only legacy Machine stays ungated": {
			existing: profileTestMachine(map[string]string{configProfileAnnotation: "old-profile"}, nil),
		},
	}
	for name, params := range tests {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, clusterv1.AddToScheme(scheme))
			k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(params.existing).Build()
			desired := profileTestMachine(nil, nil)
			require.NoError(t, validateAndSetMachineProfileSelection(context.Background(), k8sClient, desired, config.Config{}, params.existing.Annotations[configProfileAnnotation]))
			require.Empty(t, desired.Spec.ReadinessGates)
			require.Equal(t, params.existing.Annotations[configProfileAnnotation], desired.Annotations[configProfileAnnotation])
			require.NoError(t, shared.ApplyMachine(context.Background(), k8sClient, "cloud-workflow-engine", desired))

			actual := &clusterv1.Machine{}
			require.NoError(t, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(params.existing), actual))
			require.Equal(t, params.existing.Spec.ReadinessGates, actual.Spec.ReadinessGates)
			require.Equal(t, params.existing.Annotations[configProfileAnnotation], actual.Annotations[configProfileAnnotation])
			require.Contains(t, actual.Annotations, configProfileAnnotation)
			require.NotContains(t, actual.Labels, configProfileAnnotation)
		})
	}
}

func delegatedProfileTestMachine(name string) *clusterv1.Machine {
	machine := profileTestMachine(nil, nil)
	machine.Annotations[configProfileAnnotation] = name
	machine.Spec.ReadinessGates = []clusterv1.MachineReadinessGate{{ConditionType: configReadyGate}}
	return machine
}

func gatedProfileTestMachineWithoutAnnotation() *clusterv1.Machine {
	machine := profileTestMachine(nil, nil)
	machine.Spec.ReadinessGates = []clusterv1.MachineReadinessGate{{ConditionType: configReadyGate}}
	return machine
}

func profileTestMachine(annotations, labels map[string]string) *clusterv1.Machine {
	if annotations == nil {
		annotations = map[string]string{}
	}
	if labels == nil {
		labels = map[string]string{}
	}
	return &clusterv1.Machine{
		TypeMeta: metav1.TypeMeta{APIVersion: clusterv1.GroupVersion.String(), Kind: "Machine"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        "worker-0",
			Namespace:   "nke-system",
			Annotations: annotations,
			Labels:      labels,
		},
	}
}
