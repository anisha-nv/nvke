// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package server shares RPC controls between native gRPC clients and the generated REST gateway.
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"golang.org/x/net/netutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/tap"

	"github.com/dsx-ai-factory/nvke/management-engine/internal/public/tenant/limits"
	clusterv1 "github.com/dsx-ai-factory/nvke/management-engine/public/tenant/gen/cluster/v1"
)

const (
	maxResponseBytes   = 1 << 20
	drainTimeout       = 30 * time.Second
	connectionIdle     = 30 * time.Second
	gatewayConnections = 2
)

// Addresses separates application listeners from unauthenticated process probes.
type Addresses struct {
	HTTP   string // REST traffic uses the generated gateway and shared RPC controls.
	GRPC   string // Native clients have a separate connection budget from the local gateway.
	Health string // Process probes stay reachable while application traffic drains.
}

// Server owns independent REST, gRPC and health listeners without controller state.
type Server struct {
	addresses Addresses
	http      *http.Server
	grpc      *grpc.Server
	health    *http.Server
	draining  atomic.Bool
	// Gateway routes bind after the RPC listener has its final address.
	gateway *runtime.ServeMux
	// Both transports hold execution slots until their service handlers return.
	admission chan struct{}
	capacity  limits.Capacity
	// The private address is fixed before serving and identifies the gateway route during drain.
	gatewayAddress string
}

// New registers one tenant implementation on both transports. Ready checks storage.
func New(addresses Addresses, capacity limits.Capacity, service clusterv1.ClusterServiceServer, ready func(context.Context) error) (*Server, error) {
	if err := capacity.Validate(); err != nil {
		return nil, fmt.Errorf("invalid API capacity: %w", err)
	}
	if service == nil || ready == nil {
		return nil, fmt.Errorf("cluster service and readiness check are required")
	}
	s := &Server{
		addresses: addresses, admission: make(chan struct{}, capacity.ConcurrentRequests), capacity: capacity,
	}
	// Forwarded metadata is caller-supplied and must be verified before it can establish identity.
	gateway := runtime.NewServeMux(
		runtime.WithDisableHTTPMethodOverride(),
		runtime.WithDisablePathLengthFallback(),
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &jsonMarshaler{runtime.JSONPb{
			UseProtoNames: true, DiscardUnknown: false,
		}}),
	)
	rpc := grpc.NewServer(
		grpc.MaxRecvMsgSize(limits.RequestBytes), grpc.MaxSendMsgSize(maxResponseBytes),
		grpc.MaxConcurrentStreams(capacity.ConcurrentStreams), grpc.MaxHeaderListSize(16<<10), grpc.ConnectionTimeout(5*time.Second),
		grpc.KeepaliveParams(keepalive.ServerParameters{MaxConnectionIdle: connectionIdle}),
		grpc.InTapHandle(rpcDeadline),
		grpc.UnaryInterceptor(s.interceptRPC),
	)
	clusterv1.RegisterClusterServiceServer(rpc, service)
	handler, err := boundary(gateway)
	if err != nil {
		return nil, err
	}
	probes := http.NewServeMux()
	probes.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	probes.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if s.draining.Load() {
			http.Error(w, "server is draining", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), limits.ProbeTimeout)
		defer cancel()
		if err := ready(ctx); err != nil {
			http.Error(w, "datastore unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	s.http = httpServer(limitHTTP(handler, &s.draining, capacity.ConcurrentRequests))
	s.grpc = rpc
	s.health = httpServer(probes)
	s.gateway = gateway
	return s, nil
}

// rpcDeadline bounds message receipt before decoding without bypassing transport cleanup on rejection.
func rpcDeadline(ctx context.Context, _ *tap.Info) (context.Context, error) {
	bounded, cancel := context.WithTimeout(ctx, limits.RequestTimeout)
	context.AfterFunc(bounded, cancel)
	return bounded, nil
}

// interceptRPC rejects new work through normal RPC completion so transport deadlines are released.
func (s *Server) interceptRPC(ctx context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if s.draining.Load() {
		remote, ok := peer.FromContext(ctx)
		if !ok || remote.LocalAddr == nil || remote.LocalAddr.String() != s.gatewayAddress {
			return nil, status.Error(codes.Unavailable, "server is draining")
		}
	}
	select {
	case s.admission <- struct{}{}:
		defer func() { <-s.admission }()
	default:
		return nil, status.Error(codes.ResourceExhausted, "too many concurrent requests")
	}
	return handler(ctx, request)
}

// httpServer bounds connection lifetimes and headers for HTTP and probe listeners.
func httpServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: limits.RequestTimeout,
		WriteTimeout: 10 * time.Second, IdleTimeout: connectionIdle, MaxHeaderBytes: 16 << 10,
	}
}

// limitHTTP bounds parsing and rejects new requests during shutdown. RPC admission counts accepted calls once.
func limitHTTP(next http.Handler, draining *atomic.Bool, capacity int) http.Handler {
	ingress := make(chan struct{}, capacity)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if draining.Load() {
			writeError(w, http.StatusServiceUnavailable, codes.Unavailable, "server is draining")
			return
		}
		select {
		case ingress <- struct{}{}:
			defer func() { <-ingress }()
		default:
			writeError(w, http.StatusTooManyRequests, codes.ResourceExhausted, "too many concurrent requests")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), limits.RequestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Run binds all listeners before serving, then drains requests on cancellation.
func (s *Server) Run(ctx context.Context) error {
	addresses := []string{s.addresses.HTTP, s.addresses.GRPC, s.addresses.Health}
	listeners := make([]net.Listener, 0, len(addresses))
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for _, address := range addresses {
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
		if err != nil {
			return fmt.Errorf("bind API listener: %w", err)
		}
		listeners = append(listeners, listener)
	}
	return s.Serve(ctx, listeners[0], listeners[1], listeners[2])
}

// Serve accepts prebound listeners, also allowing tests to use ephemeral ports.
func (s *Server) Serve(ctx context.Context, rest, rpc, health net.Listener) error {
	defer func() {
		_ = rest.Close()
		_ = rpc.Close()
		_ = health.Close()
	}()
	internal, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("bind gateway RPC listener: %w", err)
	}
	defer func() { _ = internal.Close() }()
	s.gatewayAddress = internal.Addr().String()
	connection, err := s.connectGateway(internal.Addr())
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	rest = netutil.LimitListener(rest, s.capacity.HTTPConnections)
	rpc = netutil.LimitListener(rpc, s.capacity.GRPCConnections)
	health = netutil.LimitListener(health, 8)
	// Reconnects may briefly overlap without consuming native-client capacity.
	internal = netutil.LimitListener(internal, gatewayConnections)
	failures := make(chan error, 4)
	go func() { failures <- s.http.Serve(rest) }()
	go func() { failures <- s.grpc.Serve(rpc) }()
	go func() { failures <- s.grpc.Serve(internal) }()
	go func() { failures <- s.health.Serve(health) }()
	var result error
	select {
	case <-ctx.Done():
	case result = <-failures:
	}
	s.shutdown()
	if result != nil && !errors.Is(result, http.ErrServerClosed) && !errors.Is(result, grpc.ErrServerStopped) {
		return fmt.Errorf("serve cluster API: %w", result)
	}
	return nil
}

// connectGateway sends HTTP calls through the same RPC controls as native gRPC clients.
func (s *Server) connectGateway(address net.Addr) (*grpc.ClientConn, error) {
	// This connection stays in the process's network namespace and bypasses ambient proxies.
	connection, err := grpc.NewClient("passthrough:///"+address.String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithNoProxy(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxResponseBytes), grpc.MaxCallSendMsgSize(limits.RequestBytes)),
	)
	if err != nil {
		return nil, fmt.Errorf("connect REST gateway: %w", err)
	}
	if err := clusterv1.RegisterClusterServiceHandler(context.Background(), s.gateway, connection); err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("register REST gateway: %w", err)
	}
	return connection, nil
}

// shutdown drains accepted calls before closing the gateway's RPC connection.
func (s *Server) shutdown() {
	s.draining.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	// Accepted HTTP bodies can still start gateway RPCs, so finish HTTP before stopping gRPC.
	if err := s.http.Shutdown(ctx); err != nil {
		_ = s.http.Close()
	}
	stopped := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-ctx.Done():
		s.grpc.Stop()
	}
	if err := s.health.Shutdown(ctx); err != nil {
		_ = s.health.Close()
	}
}
