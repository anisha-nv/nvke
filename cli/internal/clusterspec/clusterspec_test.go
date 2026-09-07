// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package clusterspec

import (
	"strings"
	"testing"

	"github.com/NVIDIA/nke/cli/internal/util"
	api "github.com/NVIDIA/nke/controller/api"
)

const (
	testSiteID         = "11111111-1111-4111-8111-111111111111"
	testVpcID          = "22222222-2222-4222-8222-222222222222"
	testPrefixID       = "33333333-3333-4333-8333-333333333333"
	testInstanceTypeID = "44444444-4444-4444-8444-444444444444"
	testKeyGroupID     = "55555555-5555-4555-8555-555555555555"
)

func validInput(provider string) Input {
	return Input{
		Provider:             provider,
		Name:                 "demo",
		KubernetesVersion:    "1.34.2",
		CNIPlugin:            "calico",
		ControlPlaneCount:    1,
		WorkerCount:          1,
		SystemReservedMemory: "1Gi",
		Nico: NicoInput{
			OrgID:                     "example-org",
			SiteID:                    testSiteID,
			VpcID:                     testVpcID,
			VpcPrefixID:               testPrefixID,
			InstanceTypeID:            testInstanceTypeID,
			SSHKeyGroupID:             testKeyGroupID,
			OSProfile:                 "nke-os",
			ControlPlaneConfigProfile: "default-cp",
			WorkerConfigProfile:       "default-worker",
		},
	}
}

func TestBuildAndRenderCAPD(t *testing.T) {
	req, err := Build(validInput(ProviderCAPD))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if req.Infra == nil || req.Nico != nil {
		t.Fatalf("provider payload = infra:%v nico:%v, want CAPD infra only", req.Infra != nil, req.Nico != nil)
	}
	if req.NodePools == nil || len(*req.NodePools) != 2 {
		t.Fatalf("node pools = %#v, want control-plane and worker", req.NodePools)
	}
	rendered, err := Render(req)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	decoded, err := util.YAMLToRequest[api.CreateClusterJSONRequestBody](rendered)
	if err != nil {
		t.Fatalf("decode rendered spec: %v", err)
	}
	if decoded.Infra == nil || decoded.Infra.Cluster["kind"] != "DockerCluster" {
		t.Errorf("rendered CAPD infra = %#v, want DockerCluster", decoded.Infra)
	}
	if got := (*decoded.NodePools)[0].Infra.SpireAttestationMethod; got != api.SpireAttestationMethodCreateJoinToken {
		t.Errorf("CAPD attestation = %q, want joinToken", got)
	}
}

func TestBuildAndRenderNico(t *testing.T) {
	req, err := Build(validInput(ProviderNico))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if req.Nico == nil || req.Infra != nil {
		t.Fatalf("provider payload = nico:%v infra:%v, want NICo only", req.Nico != nil, req.Infra != nil)
	}
	if req.NodePools == nil || len(*req.NodePools) != 2 {
		t.Fatalf("node pools = %#v, want control-plane and worker", req.NodePools)
	}
	for _, pool := range *req.NodePools {
		if pool.Nico == nil || len(pool.Nico.VpcPrefixIDs) != 1 {
			t.Errorf("pool %q nico payload = %#v, want VPC-prefix NICo payload", pool.Name, pool.Nico)
		}
	}
	rendered, err := Render(req)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(rendered), "nico:") {
		t.Errorf("rendered spec is missing nico blocks:\n%s", rendered)
	}
	if err := EqualRequest(rendered, req); err != nil {
		t.Errorf("round trip: %v", err)
	}
}

func TestBuildNicoRejectsMissingProviderInputWithoutRendering(t *testing.T) {
	in := validInput(ProviderNico)
	in.Nico = NicoInput{}
	_, err := Build(in)
	if err == nil {
		t.Fatal("Build accepted missing NICo input")
	}
	for _, want := range []string{"nico.orgID", "nico.siteID", "nico.vpcID", "nico.vpcPrefixID", "nico.osProfile"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

func TestBuildNicoRejectsConflictingNetworks(t *testing.T) {
	in := validInput(ProviderNico)
	in.Nico.SubnetID = testPrefixID
	_, err := Build(in)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("Build error = %v, want conflicting network error", err)
	}
}
