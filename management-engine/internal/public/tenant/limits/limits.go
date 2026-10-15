// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package limits defines resource bounds shared by Public API transports and storage.
package limits

import (
	"fmt"
	"time"
)

const (
	// RequestBytes bounds both wire requests and decoded protobuf requests.
	RequestBytes = 64 << 10
	// PageTokenKeyMinBytes is the minimum raw pagination-key length.
	PageTokenKeyMinBytes = 32
	// PageTokenKeyMaxBytes bounds pagination-key configuration files.
	PageTokenKeyMaxBytes = 64
	// RequestTimeout bounds application requests, including receiving their bodies.
	RequestTimeout = 5 * time.Second
	// StorageTimeout bounds a datastore operation even without an ingress deadline.
	StorageTimeout = RequestTimeout
	// ProbeTimeout bounds datastore readiness checks.
	ProbeTimeout = time.Second
)

// Capacity separates connection allowances from request budgets so deployments can tune them independently.
type Capacity struct {
	HTTPConnections    int    // Limits accepted REST connections independently of native clients.
	GRPCConnections    int    // Limits accepted native connections without restricting the gateway's connection.
	ConcurrentStreams  uint32 // Bounds concurrent RPC streams from each connection.
	ConcurrentRequests int    // Bounds HTTP body processing and shared handler execution separately.
}

// DefaultCapacity supplies provisional evaluation limits until deployment load tests establish suitable budgets.
func DefaultCapacity() Capacity {
	return Capacity{HTTPConnections: 32, GRPCConnections: 32, ConcurrentStreams: 16, ConcurrentRequests: 64}
}

// Validate rejects disabled or excessive limits before servers allocate capacity or accept traffic.
func (c Capacity) Validate() error {
	for _, limit := range []struct {
		name  string
		value int64
	}{
		{"max-http-connections", int64(c.HTTPConnections)},
		{"max-grpc-connections", int64(c.GRPCConnections)},
		{"max-concurrent-streams", int64(c.ConcurrentStreams)},
		{"max-concurrent-requests", int64(c.ConcurrentRequests)},
	} {
		if limit.value < 1 || limit.value > 1024 {
			return fmt.Errorf("%s must be between 1 and 1024", limit.name)
		}
	}
	return nil
}
