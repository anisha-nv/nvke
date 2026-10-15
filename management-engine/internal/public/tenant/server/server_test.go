// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Transport tests exercise the generated gateway and real RPC controls without depending on datastore behavior.
package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
	"golang.org/x/net/netutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/tap"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/dsx-ai-factory/nvke/management-engine/internal/public/tenant/limits"
	clusterv1 "github.com/dsx-ai-factory/nvke/management-engine/public/tenant/gen/cluster/v1"
)

type testService struct {
	clusterv1.UnimplementedClusterServiceServer
	calls   atomic.Int32
	bounded atomic.Bool
	observe func(proto.Message)
	rpc     atomic.Bool
}

// record captures decoded requests so transport tests can detect unexpected or repeated service calls.
func (s *testService) record(request proto.Message) {
	s.calls.Add(1)
	if s.observe != nil {
		s.observe(request)
	}
}

// CreateCluster records transport context and returns a public response to verify RPC deadlines and JSON naming.
func (s *testService) CreateCluster(ctx context.Context, request *clusterv1.CreateClusterRequest) (*clusterv1.Cluster, error) {
	s.record(request)
	deadline, ok := ctx.Deadline()
	s.bounded.Store(ok && time.Until(deadline) <= limits.RequestTimeout)
	_, fromRPC := peer.FromContext(ctx)
	s.rpc.Store(fromRPC)
	return &clusterv1.Cluster{Name: request.GetName(), ResourceVersion: "12"}, nil
}

// ListClusters echoes pagination input so the gateway's query binding can be checked independently of storage.
func (s *testService) ListClusters(_ context.Context, request *clusterv1.ListClustersRequest) (*clusterv1.ListClustersResponse, error) {
	s.record(request)
	return &clusterv1.ListClustersResponse{NextPageToken: request.GetPageToken()}, nil
}

// GetCluster echoes the decoded identity so route binding and admission tests can observe accepted calls.
func (s *testService) GetCluster(_ context.Context, request *clusterv1.GetClusterRequest) (*clusterv1.Cluster, error) {
	s.record(request)
	return &clusterv1.Cluster{Name: request.GetName()}, nil
}

// ReplaceCluster echoes identity and revision so replacement routes can be checked without persisting data.
func (s *testService) ReplaceCluster(_ context.Context, request *clusterv1.ReplaceClusterRequest) (*clusterv1.Cluster, error) {
	s.record(request)
	return &clusterv1.Cluster{Name: request.GetName(), ResourceVersion: request.GetResourceVersion()}, nil
}

// TestGatewayRejectsAmbiguousAndUnboundedRequests verifies invalid HTTP inputs fail before reaching the service.
func TestGatewayRejectsAmbiguousAndUnboundedRequests(t *testing.T) {
	for name, test := range map[string]struct {
		method      string
		path        string
		body        string
		contentType string
		encoding    string
		override    string
		readError   error
		wantHTTP    int
		wantCode    codes.Code
	}{
		"duplicate key":    {body: `{"name":"sample","name":"other"}`},
		"nested duplicate": {body: `{"name":"sample","spec":{"control_plane_class":"a","control_plane_class":"b"}}`},
		"unknown field":    {body: `{"name":"sample","secret":"value"}`},
		"camel alias":      {body: `{"name":"sample","spec":{"controlPlaneClass":"a"}}`},
		"explicit null":    {body: `{"name":"sample","spec":null}`},
		"scalar null":      {body: `{"name":null}`},
		"nested null":      {body: `{"spec":{"cluster_network":{"pods":{"cidr_blocks":[null]}}}}`},
		"trailing JSON":    {body: `{"name":"sample"}{}`},
		"trailing scalar":  {body: `{"name":"sample"}false`},
		"empty body":       {},
		"removed org":      {body: `{"name":"sample","org":"local"}`},
		"project body":     {body: `{"name":"sample","project":"example"}`},
		"name override":    {method: "PUT", path: "/v1/clusters/sample", body: `{"name":"other"}`},
		"empty name":       {method: "PUT", path: "/v1/clusters/sample", body: `{"name":""}`},
		"late override":    {method: "PUT", path: "/v1/clusters/sample", body: `{"spec":{"release":{"channel":{"name":"stable"}}},"name":"other"}`},
		"form":             {body: "name=sample", contentType: "application/x-www-form-urlencoded", wantHTTP: http.StatusUnsupportedMediaType},
		"compression":      {body: `{"name":"sample"}`, encoding: "gzip", wantHTTP: http.StatusUnsupportedMediaType},
		"method override":  {body: `{"name":"sample"}`, override: "GET"},
		"huge body": {
			body: strings.Repeat(" ", limits.RequestBytes+1), wantHTTP: http.StatusRequestEntityTooLarge, wantCode: codes.ResourceExhausted,
		},
		"GET huge body": {
			method: "GET", body: strings.Repeat(" ", limits.RequestBytes+1), wantHTTP: http.StatusRequestEntityTooLarge, wantCode: codes.ResourceExhausted,
		},
		"truncated body": {readError: io.ErrUnexpectedEOF},
		"malformed body": {readError: errors.New("private-body-fragment")},
		"body timeout": {
			readError: &net.OpError{Op: "read", Err: context.DeadlineExceeded}, wantHTTP: http.StatusRequestTimeout, wantCode: codes.DeadlineExceeded,
		},
		"POST query":           {path: "/v1/clusters?org=other", body: `{"name":"sample"}`},
		"org query":            {method: "GET", path: "/v1/clusters?org=local"},
		"project query":        {method: "GET", path: "/v1/clusters?project=example"},
		"query alias":          {method: "GET", path: "/v1/clusters?pageSize=1"},
		"duplicate query":      {method: "GET", path: "/v1/clusters?page_size=1&page_size=2"},
		"bound query":          {method: "GET", path: "/v1/clusters/sample?name=other"},
		"GET body":             {method: "GET", body: `{}`},
		"path encoding":        {path: "/v1/clusters/sam%70le", body: `{"name":"sample"}`},
		"org collection route": {method: "GET", path: "/v1/orgs/local/clusters", wantHTTP: http.StatusNotFound, wantCode: codes.NotFound},
		"org resource route":   {method: "GET", path: "/v1/orgs/local/clusters/sample", wantHTTP: http.StatusNotFound, wantCode: codes.NotFound},
		"project route":        {method: "GET", path: "/v1/projects/example/clusters", wantHTTP: http.StatusNotFound, wantCode: codes.NotFound},
	} {
		t.Run(name, func(t *testing.T) {
			service := &testService{}
			server, err := New(Addresses{}, limits.DefaultCapacity(), service, func(context.Context) error { return nil })
			require.NoError(t, err)
			startGateway(t, server)
			if test.method == "" {
				test.method = http.MethodPost
			}
			if test.path == "" {
				test.path = "/v1/clusters"
			}
			if test.contentType == "" {
				test.contentType = "application/json"
			}
			if test.wantHTTP == 0 {
				test.wantHTTP = http.StatusBadRequest
			}
			if test.wantCode == codes.OK {
				test.wantCode = codes.InvalidArgument
			}
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.readError != nil {
				request.Body = io.NopCloser(iotest.ErrReader(test.readError))
			}
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set("Content-Encoding", test.encoding)
			if test.override != "" {
				request.Header.Set("X-HTTP-Method-Override", test.override)
			}
			response := httptest.NewRecorder()
			server.http.Handler.ServeHTTP(response, request)
			require.Equal(t, test.wantHTTP, response.Code, response.Body.String())
			var result struct {
				Code uint32 `json:"code"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			require.Equal(t, uint32(test.wantCode), result.Code)
			require.NotContains(t, response.Body.String(), "private-body-fragment")
			require.Zero(t, service.calls.Load())
		})
	}
}

// TestIdleGRPCConnectionsReleaseCapacity verifies idle peers cannot retain the connection budget indefinitely.
func TestIdleGRPCConnectionsReleaseCapacity(t *testing.T) {
	// Exercise grpc-go's actual idle timer with an in-memory transport and fake
	// time, avoiding a production timeout override or a 30-second test sleep.
	synctest.Test(t, func(t *testing.T) {
		api, err := New(Addresses{}, limits.DefaultCapacity(), &testService{}, func(context.Context) error { return nil })
		require.NoError(t, err)
		listener := bufconn.Listen(limits.RequestBytes)
		defer api.grpc.Stop()
		go func() { _ = api.grpc.Serve(netutil.LimitListener(listener, 1)) }()
		var clients [2]clusterv1.ClusterServiceClient
		for i := range clients {
			connection, err := grpc.NewClient("passthrough:///memory",
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
			)
			require.NoError(t, err)
			defer func() { _ = connection.Close() }()
			clients[i] = clusterv1.NewClusterServiceClient(connection)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 2*connectionIdle)
		defer cancel()
		_, err = clients[0].GetCluster(ctx, &clusterv1.GetClusterRequest{Name: "sample"})
		require.NoError(t, err)
		started := time.Now()
		_, err = clients[1].GetCluster(ctx, &clusterv1.GetClusterRequest{Name: "sample"}, grpc.WaitForReady(true))
		require.NoError(t, err, "idle connection must release the only slot for the waiting client")
		require.GreaterOrEqual(t, time.Since(started), connectionIdle)
		require.Less(t, time.Since(started), 2*connectionIdle)
	})
}

// TestGRPCDeadlineIncludesIncompleteMessage verifies slow message delivery is bounded before RPC decoding finishes.
func TestGRPCDeadlineIncludesIncompleteMessage(t *testing.T) {
	capacity := limits.DefaultCapacity()
	capacity.ConcurrentRequests = 1
	service := &testService{}
	api, err := New(Addresses{}, capacity, service, func(context.Context) error { return nil })
	require.NoError(t, err)
	listener := bufconn.Listen(1 << 20)
	t.Cleanup(api.grpc.Stop)
	go func() { _ = api.grpc.Serve(listener) }()
	connection, err := listener.Dial()
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	_, err = io.WriteString(connection, http2.ClientPreface)
	require.NoError(t, err)
	framer := http2.NewFramer(connection, connection)
	require.NoError(t, framer.WriteSettings())
	var encoded bytes.Buffer
	encoder := hpack.NewEncoder(&encoded)
	for _, field := range []hpack.HeaderField{
		{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "http"},
		{Name: ":path", Value: clusterv1.ClusterService_GetCluster_FullMethodName},
		{Name: ":authority", Value: "local"}, {Name: "content-type", Value: "application/grpc"},
		{Name: "te", Value: "trailers"},
	} {
		require.NoError(t, encoder.WriteField(field))
	}
	require.NoError(t, framer.WriteHeaders(http2.HeadersFrameParam{
		StreamID: 1, BlockFragment: encoded.Bytes(), EndHeaders: true,
	}))
	// The envelope promises ten message bytes, but the peer sends only one.
	require.NoError(t, framer.WriteData(1, false, []byte{0, 0, 0, 0, 10, 10}))
	client := clusterv1.NewClusterServiceClient(startGateway(t, api))
	ctx, cancel := context.WithTimeout(t.Context(), 2*limits.RequestTimeout)
	defer cancel()
	_, err = client.GetCluster(ctx, &clusterv1.GetClusterRequest{Name: "sample"})
	require.NoError(t, err, "incomplete messages must not consume handler capacity")
	require.Equal(t, int32(1), service.calls.Load())
	started := time.Now()
	require.NoError(t, connection.SetReadDeadline(started.Add(limits.RequestTimeout+2*time.Second)))
	for {
		frame, err := framer.ReadFrame()
		require.NoError(t, err, "server must terminate the incomplete message before the peer deadline")
		if headers, ok := frame.(*http2.HeadersFrame); ok && headers.StreamEnded() {
			break
		}
		if reset, ok := frame.(*http2.RSTStreamFrame); ok && reset.StreamID == 1 {
			break
		}
	}
	require.GreaterOrEqual(t, time.Since(started), limits.RequestTimeout/2, "valid request headers must be accepted")
	_, err = client.GetCluster(ctx, &clusterv1.GetClusterRequest{Name: "sample"})
	require.NoError(t, err)
	require.Equal(t, int32(2), service.calls.Load(), "incomplete messages must never reach the service")
}

// TestNewRejectsInvalidCapacity verifies callers cannot bypass startup validation.
func TestNewRejectsInvalidCapacity(t *testing.T) {
	_, err := New(Addresses{}, limits.Capacity{}, &testService{}, func(context.Context) error { return nil })
	require.ErrorContains(t, err, "invalid API capacity")
}

// TestHTTPAdmissionPrecedesBodyRead verifies slow bodies cannot bypass the configured ingress budget.
func TestHTTPAdmissionPrecedesBodyRead(t *testing.T) {
	capacity := limits.DefaultCapacity()
	capacity.ConcurrentRequests = 1
	service := &testService{}
	api, err := New(Addresses{}, capacity, service, func(context.Context) error { return nil })
	require.NoError(t, err)
	startGateway(t, api)
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close() })
	t.Cleanup(func() { _ = writer.Close() })
	request := httptest.NewRequest(http.MethodPost, "/v1/clusters", reader)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		api.http.Handler.ServeHTTP(response, request)
		close(done)
	}()
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(writer, `{"name":`)
		written <- err
	}()
	select {
	case err := <-written:
		require.NoError(t, err, "the first request must be reading its incomplete body")
	case <-time.After(3 * time.Second):
		t.Fatal("accepted HTTP request did not read its body")
	}
	var reads atomic.Int32
	rejected := httptest.NewRequest(http.MethodPost, "/v1/clusters", nil)
	rejected.Header.Set("Content-Type", "application/json")
	rejected.Body = io.NopCloser(countingReader{reads: &reads})
	rejection := httptest.NewRecorder()
	api.http.Handler.ServeHTTP(rejection, rejected)
	require.Equal(t, http.StatusTooManyRequests, rejection.Code)
	require.Zero(t, reads.Load(), "rejected requests must not read bodies")
	require.Zero(t, service.calls.Load())
	_, err = io.WriteString(writer, `"sample"}`)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	select {
	case <-done:
		require.Equal(t, http.StatusOK, response.Code)
	case <-time.After(3 * time.Second):
		t.Fatal("accepted HTTP body did not complete")
	}
	invalid := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(http.MethodPost, "/v1/clusters", strings.NewReader("{"))
	invalidRequest.Header.Set("Content-Type", "application/json")
	api.http.Handler.ServeHTTP(invalid, invalidRequest)
	require.Equal(t, http.StatusBadRequest, invalid.Code, "completed HTTP bodies must release ingress capacity")
	accepted := httptest.NewRecorder()
	api.http.Handler.ServeHTTP(accepted, httptest.NewRequest(http.MethodGet, "/v1/clusters/sample", nil))
	require.Equal(t, http.StatusOK, accepted.Code, "invalid HTTP bodies must release ingress capacity")
}

type countingReader struct {
	reads *atomic.Int32
}

// Read records unexpected body consumption by a request rejected at ingress.
func (r countingReader) Read([]byte) (int, error) {
	r.reads.Add(1)
	return 0, io.EOF
}

// TestRPCDecodeFailuresNeverCallService verifies invalid messages cannot reach resource operations.
func TestRPCDecodeFailuresNeverCallService(t *testing.T) {
	for name, test := range map[string]struct {
		method string
		code   codes.Code
	}{
		"malformed protobuf": {method: clusterv1.ClusterService_GetCluster_FullMethodName, code: codes.Internal},
		"unknown method":     {method: "/unknown.Service/Method", code: codes.Unimplemented},
	} {
		t.Run(name, func(t *testing.T) {
			capacity := limits.DefaultCapacity()
			capacity.ConcurrentRequests = 1
			service := &testService{}
			api, err := New(Addresses{}, capacity, service, func(context.Context) error { return nil })
			require.NoError(t, err)
			connection := startGateway(t, api)
			ctx, cancel := context.WithTimeout(t.Context(), limits.RequestTimeout)
			defer cancel()
			err = connection.Invoke(ctx, test.method, &clusterv1.GetClusterRequest{}, &clusterv1.Cluster{}, grpc.ForceCodec(malformedCodec{}))
			require.Equal(t, test.code, status.Code(err))
			require.Zero(t, service.calls.Load())
			_, err = clusterv1.NewClusterServiceClient(connection).GetCluster(ctx, &clusterv1.GetClusterRequest{Name: "sample"})
			require.NoError(t, err)
		})
	}
}

type malformedCodec struct{}

// Marshal emits a truncated protobuf tag to exercise the real server decoder's failure path.
func (malformedCodec) Marshal(any) ([]byte, error) { return []byte{0xff}, nil }

// Unmarshal leaves error responses untouched because this codec only sends malformed requests.
func (malformedCodec) Unmarshal([]byte, any) error { return nil }

// Name selects the server's production protobuf decoder.
func (malformedCodec) Name() string { return "proto" }

// TestCanceledRPCStillHoldsHandlerCapacity verifies cancellation cannot admit extra work while a handler remains active.
func TestCanceledRPCStillHoldsHandlerCapacity(t *testing.T) {
	capacity := limits.DefaultCapacity()
	capacity.ConcurrentRequests = 1
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	service := &testService{observe: func(proto.Message) {
		entered <- struct{}{}
		<-release
	}}
	api, err := New(Addresses{}, capacity, service, func(context.Context) error { return nil })
	require.NoError(t, err)
	client := clusterv1.NewClusterServiceClient(startGateway(t, api))
	t.Cleanup(unblock)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.GetCluster(ctx, &clusterv1.GetClusterRequest{Name: "sample"})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach service")
	}
	cancel()
	select {
	case err := <-done:
		require.Equal(t, codes.Canceled, status.Code(err))
	case <-time.After(3 * time.Second):
		t.Fatal("canceled client did not return")
	}
	requestContext, requestCancel := context.WithTimeout(t.Context(), limits.RequestTimeout)
	defer requestCancel()
	_, err = client.GetCluster(requestContext, &clusterv1.GetClusterRequest{Name: "sample"})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Equal(t, int32(1), service.calls.Load(), "canceled handlers must retain their execution slot")
	unblock()
	require.Eventually(t, func() bool { return len(api.admission) == 0 }, time.Second, time.Millisecond)
}

// TestGatewayRoutesAndSerializesRequests verifies public routes decode and return snake_case JSON through real RPC.
func TestGatewayRoutesAndSerializesRequests(t *testing.T) {
	for name, test := range map[string]struct {
		method      string
		path        string
		body        string
		wantRequest proto.Message
		wantBody    string
	}{
		"create": {
			method: "POST", path: "/v1/clusters", body: `{"name":"sample"}`,
			wantRequest: &clusterv1.CreateClusterRequest{Name: "sample"}, wantBody: `"resource_version":"12"`,
		},
		"list": {
			method: "GET", path: "/v1/clusters?page_size=1&page_token=example",
			wantRequest: &clusterv1.ListClustersRequest{PageSize: 1, PageToken: "example"}, wantBody: `"next_page_token":"example"`,
		},
		"get": {
			method: "GET", path: "/v1/clusters/sample",
			wantRequest: &clusterv1.GetClusterRequest{Name: "sample"}, wantBody: `"name":"sample"`,
		},
		"replace matching body name": {
			method: "PUT", path: "/v1/clusters/sample",
			body: `{"spec":{"release":{"channel":{"name":"null"}}},"name":"sample","resource_version":"12"} `,
			wantRequest: &clusterv1.ReplaceClusterRequest{
				Name: "sample", ResourceVersion: "12",
				Spec: &clusterv1.ReplaceClusterSpec{Release: &clusterv1.ReplaceRelease{
					Channel: &clusterv1.ReplaceReleaseChannel{Name: new("null")},
				}},
			},
			wantBody: `"name":"sample"`,
		},
		"replace path name": {
			method: "PUT", path: "/v1/clusters/sample", body: `{"resource_version":"12"}`,
			wantRequest: &clusterv1.ReplaceClusterRequest{Name: "sample", ResourceVersion: "12"}, wantBody: `"name":"sample"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			received := make(chan proto.Message, 1)
			service := &testService{observe: func(request proto.Message) { received <- request }}
			server, err := New(Addresses{}, limits.DefaultCapacity(), service, func(context.Context) error { return nil })
			require.NoError(t, err)
			startGateway(t, server)
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json; charset=utf-8")
			response := httptest.NewRecorder()
			server.http.Handler.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), test.wantBody)
			require.NotContains(t, response.Body.String(), "resourceVersion")
			require.Equal(t, int32(1), service.calls.Load())
			got := <-received
			require.True(t, proto.Equal(test.wantRequest, got), "decoded request: got %v, want %v", got, test.wantRequest)
			if test.method == http.MethodPost {
				require.True(t, service.bounded.Load())
				require.True(t, service.rpc.Load(), "HTTP must use the real RPC transport")
			}
		})
	}
}

// TestRPCCompletionReleasesTransportDeadline prevents completed calls retaining client-selected timeout timers.
func TestRPCCompletionReleasesTransportDeadline(t *testing.T) {
	for name, code := range map[string]codes.Code{
		"accepted": codes.OK, "overloaded": codes.ResourceExhausted, "draining": codes.Unavailable,
	} {
		t.Run(name, func(t *testing.T) {
			api := &Server{admission: make(chan struct{}, 1)}
			if code == codes.ResourceExhausted {
				api.admission <- struct{}{}
			}
			api.draining.Store(code == codes.Unavailable)
			incoming := make(chan context.Context, 1)
			rpc := grpc.NewServer(
				grpc.InTapHandle(func(ctx context.Context, info *tap.Info) (context.Context, error) {
					incoming <- ctx
					return rpcDeadline(ctx, info)
				}),
				grpc.UnaryInterceptor(api.interceptRPC),
			)
			service := &testService{}
			clusterv1.RegisterClusterServiceServer(rpc, service)
			listener := bufconn.Listen(1 << 20)
			t.Cleanup(rpc.Stop)
			go func() { _ = rpc.Serve(listener) }()
			connection, err := grpc.NewClient("passthrough:///deadline-test", grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
			require.NoError(t, err)
			t.Cleanup(func() { _ = connection.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
			defer cancel()
			_, err = clusterv1.NewClusterServiceClient(connection).GetCluster(ctx, &clusterv1.GetClusterRequest{Name: "sample"})
			require.Equal(t, code, status.Code(err))
			// Observe the original transport context, not the application's shorter child deadline.
			transportContext := <-incoming
			select {
			case <-transportContext.Done():
			case <-time.After(time.Second):
				t.Fatal("completed RPC retained its original transport deadline")
			}
			require.ErrorIs(t, transportContext.Err(), context.Canceled)
			if code != codes.OK {
				require.Zero(t, service.calls.Load(), "rejected requests must not reach the service")
			}
		})
	}
}

// TestAdmissionRejectsExcessWithoutCallingService proves both transports share one admission budget.
func TestAdmissionRejectsExcessWithoutCallingService(t *testing.T) {
	service := &testService{}
	server, err := New(Addresses{}, limits.DefaultCapacity(), service, func(context.Context) error { return nil })
	require.NoError(t, err)
	client := clusterv1.NewClusterServiceClient(startGateway(t, server))
	for range limits.DefaultCapacity().ConcurrentRequests - 1 {
		server.admission <- struct{}{}
	}
	for _, full := range []bool{false, true} {
		if full {
			server.admission <- struct{}{}
		}
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/clusters/sample", nil))
		ctx, cancel := context.WithTimeout(t.Context(), limits.RequestTimeout)
		_, err := client.GetCluster(ctx, &clusterv1.GetClusterRequest{Name: "sample"})
		cancel()
		if full {
			require.Equal(t, http.StatusTooManyRequests, response.Code)
			require.Equal(t, codes.ResourceExhausted, status.Code(err))
		} else {
			require.Equal(t, http.StatusOK, response.Code, "HTTP must consume only the last available RPC slot")
			require.NoError(t, err)
		}
		require.Equal(t, int32(2), service.calls.Load(), "rejected requests must not reach the service")
	}
}

// TestGatewaySurvivesNativeConnectionSaturation verifies independent connections retain shared RPC admission and healthy probes.
func TestGatewaySurvivesNativeConnectionSaturation(t *testing.T) {
	capacity := limits.Capacity{HTTPConnections: 4, GRPCConnections: 2, ConcurrentStreams: 4, ConcurrentRequests: 2}
	entered := make(chan struct{}, capacity.ConcurrentRequests)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	service := &testService{observe: func(request proto.Message) {
		if request.(*clusterv1.GetClusterRequest).GetName() == "hold" {
			entered <- struct{}{}
			<-release
		}
	}}
	api, err := New(Addresses{}, capacity, service, func(context.Context) error { return nil })
	require.NoError(t, err)
	listeners := make([]net.Listener, 3)
	for i := range listeners {
		listeners[i], err = net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = listeners[i].Close() })
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- api.Serve(ctx, listeners[0], listeners[1], listeners[2]) }()
	t.Cleanup(func() {
		unblock()
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Error("saturated API did not stop")
		}
	})
	requestContext, requestCancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer requestCancel()
	clients := make([]clusterv1.ClusterServiceClient, capacity.GRPCConnections)
	for i := range clients {
		connection, err := grpc.NewClient(listeners[1].Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithNoProxy())
		require.NoError(t, err)
		t.Cleanup(func() { _ = connection.Close() })
		clients[i] = clusterv1.NewClusterServiceClient(connection)
		_, err = clients[i].GetCluster(requestContext, &clusterv1.GetClusterRequest{Name: "sample"})
		require.NoError(t, err, "each external connection must be accepted before the gateway's first request")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	getStatus := func(address, path string) int {
		t.Helper()
		response, err := client.Get("http://" + address + path)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		return response.StatusCode
	}
	require.Equal(t, http.StatusOK, getStatus(listeners[0].Addr().String(), "/v1/clusters/sample"))
	_, err = clients[0].GetCluster(requestContext, &clusterv1.GetClusterRequest{Name: "sample"})
	require.NoError(t, err, "native RPCs must remain usable while the gateway is connected")
	nativeDone := make(chan error, 1)
	go func() {
		_, err := clients[0].GetCluster(requestContext, &clusterv1.GetClusterRequest{Name: "hold"})
		nativeDone <- err
	}()
	httpDone := make(chan error, 1)
	go func() {
		response, err := client.Get("http://" + listeners[0].Addr().String() + "/v1/clusters/hold")
		if err == nil {
			err = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				err = fmt.Errorf("accepted HTTP request returned %d", response.StatusCode)
			}
		}
		httpDone <- err
	}()
	for range capacity.ConcurrentRequests {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("both transports did not occupy shared RPC capacity")
		}
	}
	_, err = clients[1].GetCluster(requestContext, &clusterv1.GetClusterRequest{Name: "sample"})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Equal(t, http.StatusTooManyRequests, getStatus(listeners[0].Addr().String(), "/v1/clusters/sample"))
	for _, path := range []string{"/healthz", "/readyz"} {
		require.Equal(t, http.StatusOK, getStatus(listeners[2].Addr().String(), path))
	}
	unblock()
	require.NoError(t, <-nativeDone)
	require.NoError(t, <-httpDone)
}

// startGateway connects the production gateway to a real RPC listener for transport tests.
func startGateway(t *testing.T, server *Server) *grpc.ClientConn {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(server.grpc.Stop)
	go func() { _ = server.grpc.Serve(listener) }()
	connection, err := server.connectGateway(listener.Addr())
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

// TestListenersReadinessAndGracefulStop verifies separate probes reflect storage health and listeners stop on cancellation.
func TestListenersReadinessAndGracefulStop(t *testing.T) {
	service := &testService{}
	var available atomic.Bool
	server, err := New(Addresses{}, limits.DefaultCapacity(), service, func(context.Context) error {
		if !available.Load() {
			return errors.New("private storage diagnostic")
		}
		return nil
	})
	require.NoError(t, err)
	listeners := make([]net.Listener, 3)
	for i := range listeners {
		listeners[i], err = net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = listeners[i].Close() })
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listeners[0], listeners[1], listeners[2]) }()
	client := &http.Client{Timeout: 3 * time.Second}
	for _, path := range []string{"/healthz", "/readyz"} {
		response, err := client.Get("http://" + listeners[2].Addr().String() + path)
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		if path == "/readyz" {
			require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
		} else {
			require.Equal(t, http.StatusOK, response.StatusCode)
		}
		require.NotContains(t, string(body), "private storage diagnostic")
	}
	available.Store(true)
	response, err := client.Get("http://" + listeners[2].Addr().String() + "/readyz")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())
	connection, err := grpc.NewClient(listeners[1].Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	rpcContext, rpcCancel := context.WithTimeout(ctx, 2*limits.RequestTimeout)
	defer rpcCancel()
	result, err := clusterv1.NewClusterServiceClient(connection).CreateCluster(rpcContext, &clusterv1.CreateClusterRequest{Name: "sample"})
	require.NoError(t, err)
	require.Equal(t, "sample", result.GetName())
	require.True(t, service.bounded.Load())
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("API did not drain on cancellation")
	}
}

type blockingService struct {
	testService
	entered  chan struct{}
	release  chan struct{}
	deadline time.Time
}

// CreateCluster holds an accepted request so shutdown tests can distinguish draining from immediate cancellation.
func (s *blockingService) CreateCluster(ctx context.Context, request *clusterv1.CreateClusterRequest) (*clusterv1.Cluster, error) {
	s.deadline, _ = ctx.Deadline()
	close(s.entered)
	select {
	case <-s.release:
		return s.testService.CreateCluster(ctx, request)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type observedBody struct {
	io.ReadCloser
	reading  chan time.Time
	once     sync.Once
	deadline time.Time
}

// Read signals that HTTP admission completed before the request body reaches the gateway.
func (b *observedBody) Read(data []byte) (int, error) {
	b.once.Do(func() { b.reading <- b.deadline })
	return b.ReadCloser.Read(data)
}

// TestDrainRejectsReadinessAndNewRequestsWhileCompletingAcceptedRequest verifies accepted work survives graceful shutdown.
func TestDrainRejectsReadinessAndNewRequestsWhileCompletingAcceptedRequest(t *testing.T) {
	for _, pauseBody := range []bool{false, true} {
		name := "handler active"
		if pauseBody {
			name = "body incomplete before RPC"
		}
		t.Run(name, func(t *testing.T) {
			service := &blockingService{entered: make(chan struct{}), release: make(chan struct{}, 1)}
			t.Cleanup(func() { close(service.release) })
			capacity := limits.DefaultCapacity()
			capacity.ConcurrentRequests = 1
			server, err := New(Addresses{}, capacity, service, func(context.Context) error { return nil })
			require.NoError(t, err)
			reading := make(chan time.Time, 1)
			handler := server.http.Handler
			server.http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
				defer cancel()
				deadline, _ := ctx.Deadline()
				r.Body = &observedBody{ReadCloser: r.Body, reading: reading, deadline: deadline}
				handler.ServeHTTP(w, r.WithContext(ctx))
			})
			listeners := make([]net.Listener, 3)
			for i := range listeners {
				listeners[i], err = net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				t.Cleanup(func() { _ = listeners[i].Close() })
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			done := make(chan error, 1)
			go func() { done <- server.Serve(ctx, listeners[0], listeners[1], listeners[2]) }()
			client := &http.Client{Timeout: 3 * time.Second}
			t.Cleanup(client.CloseIdleConnections)
			connection, err := grpc.NewClient("passthrough:///"+listeners[1].Addr().String(),
				grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithNoProxy())
			require.NoError(t, err)
			t.Cleanup(func() { _ = connection.Close() })
			rpc := clusterv1.NewClusterServiceClient(connection)
			rpcContext, rpcCancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer rpcCancel()
			_, err = rpc.GetCluster(rpcContext, &clusterv1.GetClusterRequest{Name: "native"})
			require.NoError(t, err, "establish an external connection while the gateway remains cold")
			request, err := net.Dial("tcp", listeners[0].Addr().String())
			require.NoError(t, err)
			t.Cleanup(func() { _ = request.Close() })
			require.NoError(t, request.SetDeadline(time.Now().Add(3*time.Second)))
			body := `{"name":"sample"}`
			_, err = fmt.Fprintf(request, "POST /v1/clusters HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", len(body))
			require.NoError(t, err)
			var httpDeadline time.Time
			select {
			case httpDeadline = <-reading:
			case <-time.After(3 * time.Second):
				t.Fatal("request did not pass HTTP admission")
			}
			if !pauseBody {
				_, err = io.WriteString(request, body)
				require.NoError(t, err)
				select {
				case <-service.entered:
				case <-time.After(3 * time.Second):
					t.Fatal("request did not reach service")
				}
			}
			cancel()
			require.Eventually(t, server.draining.Load, time.Second, time.Millisecond)
			response, err := client.Get("http://" + listeners[2].Addr().String() + "/readyz")
			require.NoError(t, err)
			require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
			require.NoError(t, response.Body.Close())
			newRequest := httptest.NewRecorder()
			server.http.Handler.ServeHTTP(newRequest, httptest.NewRequest(http.MethodGet, "/v1/clusters/sample", nil))
			require.Equal(t, http.StatusServiceUnavailable, newRequest.Code)
			_, err = rpc.GetCluster(rpcContext, &clusterv1.GetClusterRequest{Name: "native"})
			require.Equal(t, codes.Unavailable, status.Code(err))
			require.Equal(t, int32(1), service.calls.Load(), "new requests must not reach the service")
			if pauseBody {
				_, err = io.WriteString(request, body)
				require.NoError(t, err)
				select {
				case <-service.entered:
				case <-time.After(time.Second):
					t.Fatal("accepted HTTP body did not reach RPC during drain")
				}
			}
			require.Equal(t, 1, len(server.admission), "forwarded HTTP holds one shared execution slot")
			require.False(t, service.deadline.IsZero())
			// gRPC propagates remaining time, so allow a small transport delay without resetting the deadline.
			require.WithinDuration(t, httpDeadline, service.deadline, 50*time.Millisecond, "RPC must retain the earlier HTTP deadline")
			service.release <- struct{}{}
			response, err = http.ReadResponse(bufio.NewReader(request), nil)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.StatusCode)
			_, err = io.Copy(io.Discard, response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, int32(2), service.calls.Load())
			require.True(t, service.rpc.Load())
			require.True(t, service.bounded.Load())
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("drain did not finish after active request")
			}
			require.Eventually(t, func() bool { return len(server.admission) == 0 }, time.Second, time.Millisecond)
		})
	}
}
