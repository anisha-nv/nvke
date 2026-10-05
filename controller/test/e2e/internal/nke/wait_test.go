// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nke

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dsx-ai-factory/nke/controller/api"
)

// clusterReply is one answer the fake CWE gives to a cluster read.
type clusterReply struct {
	status   int
	deleting api.ConditionStatus
}

// fakeClusterReads serves replies in order to every cluster read, repeating the
// last, and returns a Client pointed at it.
func fakeClusterReads(t *testing.T, replies ...clusterReply) *Client {
	t.Helper()
	var reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply := replies[min(int(reads.Add(1))-1, len(replies)-1)]
		if reply.status != http.StatusOK {
			http.Error(w, http.StatusText(reply.status), reply.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.Cluster{
			Name:       "e2e-delete",
			Conditions: api.ClusterConditions{Deleting: api.Condition{Status: reply.deleting, Message: "deleting"}},
		})
	}))
	t.Cleanup(server.Close)

	apiClient, err := api.NewClientWithResponses(server.URL + "/api")
	if err != nil {
		t.Fatal(err)
	}
	return &Client{api: apiClient, opts: Options{Delete: 200 * time.Millisecond, Poll: 10 * time.Millisecond}}
}

func TestUnit_WaitForClusterDeleted(t *testing.T) {
	stillDeleting := clusterReply{status: http.StatusOK, deleting: api.ConditionStatusTrue}
	unavailable := clusterReply{status: http.StatusServiceUnavailable}

	cases := []struct {
		name         string
		replies      []clusterReply
		wantErr      string
		wantSentinel bool
	}{{
		name:    "gone",
		replies: []clusterReply{stillDeleting, {status: http.StatusNotFound}},
	}, {
		name:         "still deleting when the budget runs out",
		replies:      []clusterReply{stillDeleting},
		wantErr:      "cluster delete timed out",
		wantSentinel: true,
	}, {
		name:    "never read back",
		replies: []clusterReply{unavailable},
		wantErr: "503",
	}, {
		name:    "read back once, then unreadable until the budget runs out",
		replies: []clusterReply{stillDeleting, unavailable},
		wantErr: "503",
	}, {
		name:    "reported failed",
		replies: []clusterReply{{status: http.StatusOK, deleting: api.ConditionStatusFalse}},
		wantErr: "deletion failed",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := fakeClusterReads(t, tc.replies...).WaitForClusterDeleted(t.Context(), uuid.New())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("WaitForClusterDeleted() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("WaitForClusterDeleted() error = %v, want one containing %q", err, tc.wantErr)
			}
			if got := errors.Is(err, ErrClusterDeleteTimeout); got != tc.wantSentinel {
				t.Errorf("errors.Is(err, ErrClusterDeleteTimeout) = %t, want %t (err: %v)", got, tc.wantSentinel, err)
			}
		})
	}
}

func TestUnit_WaitForClusterDeletedWithinUsesItsBudget(t *testing.T) {
	c := fakeClusterReads(t, clusterReply{status: http.StatusOK, deleting: api.ConditionStatusTrue})
	c.opts.Delete = time.Hour

	start := time.Now()
	err := c.WaitForClusterDeletedWithin(t.Context(), uuid.New(), 100*time.Millisecond)
	if !errors.Is(err, ErrClusterDeleteTimeout) {
		t.Fatalf("WaitForClusterDeletedWithin() error = %v, want ErrClusterDeleteTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("WaitForClusterDeletedWithin() took %v, want it bounded by its budget rather than Options.Delete", elapsed)
	}
}
