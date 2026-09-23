// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package clusters

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/dsx-ai-factory/nke/cli/internal/util"
	"github.com/dsx-ai-factory/nke/controller/api"
)

func TestLaunchLayerExamplePostsEveryTypedFieldAndReadsItBack(t *testing.T) {
	path := "../../../examples/launchlayer-cluster.yaml"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := util.YAMLToRequest[api.CreateClusterJSONRequestBody](data)
	if err != nil {
		t.Fatal(err)
	}
	fake, clientFn := newFakeCWE(t)
	if _, err := run(t, NewCreateCmd(clientFn), "-f", path); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	posted := append([]api.CreateClusterJSONRequestBody(nil), fake.posted...)
	fake.mu.Unlock()
	if len(posted) != 1 || !reflect.DeepEqual(want, posted[0]) {
		t.Fatalf("typed YAML was not preserved over HTTP: want %#v, got %#v", want, posted)
	}
	out, err := run(t, NewGetCmd(clientFn), "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var clusters []api.Cluster
	if err := json.Unmarshal([]byte(out), &clusters); err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 1 || clusters[0].Launchlayer == nil || !reflect.DeepEqual(want.Launchlayer, clusters[0].Launchlayer) {
		t.Fatalf("typed response was not preserved: %s", out)
	}
	if clusters[0].Infra != nil || clusters[0].Nico != nil {
		t.Fatal("typed response changed API mode")
	}
}
