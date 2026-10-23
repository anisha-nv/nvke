// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkectl

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/dsx-ai-factory/nvke/cli/internal/config"
	"github.com/dsx-ai-factory/nvke/cli/internal/telemetry"
)

// otlpCollector is an in-process OTLP/gRPC endpoint that keeps the spans it
// receives and the headers they came with.
type otlpCollector struct {
	coltracepb.UnimplementedTraceServiceServer

	addr string

	mu       sync.Mutex
	spans    []*tracepb.Span
	metadata metadata.MD
}

// nonLoopbackIPv4 returns an IPv4 address of this machine that is not a
// loopback address and that the machine can open a TCP connection to itself on,
// so a test can pass it to a flag that rejects loopback and still reach a
// collector there; with mustConnect false, any such address will do. A host
// firewall or VPN can reset these connections; the test is skipped when no
// address works.
func nonLoopbackIPv4(t *testing.T, mustConnect bool) string {
	t.Helper()
	ifaces, err := net.Interfaces()
	require.NoError(t, err)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		require.NoError(t, err)
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil && !ipNet.IP.IsLoopback() && (!mustConnect || canDialSelf(ipNet.IP.String())) {
				return ipNet.IP.String()
			}
		}
	}
	t.Skip("this machine has no non-loopback IPv4 address it can open a TCP connection to itself on")
	return ""
}

// canDialSelf reports whether a connection to a listener on ip reaches it and
// carries data both ways.
func canDialSelf(ip string) bool {
	lis, err := net.Listen("tcp", ip+":0")
	if err != nil {
		return false
	}
	defer lis.Close()
	go func() {
		if conn, err := lis.Accept(); err == nil {
			_, _ = conn.Write([]byte{1})
			conn.Close()
		}
	}()
	conn, err := net.DialTimeout("tcp", lis.Addr().String(), time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, err = conn.Read(make([]byte, 1))
	return err == nil
}

// newOTLPCollector starts a collector on a non-loopback address of this
// machine and returns it with that address as host:port.
func newOTLPCollector(t *testing.T) *otlpCollector {
	t.Helper()
	lis, err := net.Listen("tcp", nonLoopbackIPv4(t, true)+":0")
	require.NoError(t, err)
	c := &otlpCollector{addr: lis.Addr().String()}
	server := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(server, c)
	collogspb.RegisterLogsServiceServer(server, &otlpLogsSink{})
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
	return c
}

// Export implements coltracepb.TraceServiceServer.
func (c *otlpCollector) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metadata, _ = metadata.FromIncomingContext(ctx)
	for _, resourceSpans := range req.GetResourceSpans() {
		for _, scopeSpans := range resourceSpans.GetScopeSpans() {
			c.spans = append(c.spans, scopeSpans.GetSpans()...)
		}
	}
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

func (c *otlpCollector) received() ([]*tracepb.Span, metadata.MD) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*tracepb.Span(nil), c.spans...), c.metadata.Copy()
}

// otlpLogsSink accepts log exports, which the tests here do not inspect. The
// logs and trace services both name their method Export, so they cannot share a type.
type otlpLogsSink struct {
	collogspb.UnimplementedLogsServiceServer
}

// Export implements collogspb.LogsServiceServer.
func (*otlpLogsSink) Export(context.Context, *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	return &collogspb.ExportLogsServiceResponse{}, nil
}

// An OTLP endpoint that is unreachable or misconfigured never fails a command
// or holds it up. These tests run commands through execute, so none is parallel.
func TestOTLPEndpointNeverFailsTheCommand(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closedPort := lis.Addr().String()
	require.NoError(t, lis.Close())

	tests := map[string]string{
		"unreachable":    "http://" + closedPort,
		"unparsable":     "http://",
		"no endpoint":    "",
		"https endpoint": "https://" + closedPort,
	}
	for name, endpoint := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
			root := NewRootCmd("test", "test", "test", "test")
			root.AddCommand(&cobra.Command{
				Use: "probe",
				RunE: func(*cobra.Command, []string) error {
					slog.Info("probe ran")
					return nil
				},
			})
			root.SetArgs([]string{"probe"})
			var stderr bytes.Buffer
			root.SetErr(&stderr)

			started := time.Now()
			require.NoError(t, execute(context.Background(), root, "test", &stderr))

			assert.Less(t, time.Since(started), 4*time.Second, "the flush is bounded")
			assert.Regexp(t, traceIDLineRE, stderr.String(), "the command's output is unchanged")
		})
	}
}

// logsCollector is an in-process OTLP/gRPC logs endpoint that keeps the body of
// every record it receives.
type logsCollector struct {
	collogspb.UnimplementedLogsServiceServer

	addr string

	mu     sync.Mutex
	bodies []string
}

func newLogsCollector(t *testing.T) *logsCollector {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	collector := &logsCollector{addr: lis.Addr().String()}
	server := grpc.NewServer()
	collogspb.RegisterLogsServiceServer(server, collector)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
	return collector
}

// Export implements collogspb.LogsServiceServer.
func (c *logsCollector) Export(_ context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, resourceLogs := range req.GetResourceLogs() {
		for _, scopeLogs := range resourceLogs.GetScopeLogs() {
			for _, record := range scopeLogs.GetLogRecords() {
				c.bodies = append(c.bodies, record.GetBody().GetStringValue())
			}
		}
	}
	return &collogspb.ExportLogsServiceResponse{}, nil
}

func (c *logsCollector) received() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...)
}

// The last record a successful query command logs, "command finished", is
// written after the command returns, so the export must be flushed after it.
// This test runs the command through execute, so it is not parallel.
func TestOTLPExportsTheFinalRecordOfAQueryCommand(t *testing.T) {
	collector := newLogsCollector(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+collector.addr)
	t.Cleanup(func() { verbose = false })

	root := NewRootCmd("test", "test", "test", "test")
	root.SetArgs([]string{"version", "--verbose"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	require.NoError(t, execute(context.Background(), root, "test", io.Discard))

	assert.Contains(t, collector.received(), "command finished")
}

// resolveFor parses args as the flags of "bootstrap site" and resolves the OTLP
// configuration for it, without touching the network.
func resolveFor(t *testing.T, args ...string) (cfg telemetry.OTLPConfig, ok, strict bool, err error) {
	t.Helper()
	cmd := newBootstrapSiteCmd()
	require.NoError(t, cmd.ParseFlags(args))
	return resolveOTLP(cmd)
}

func TestResolveOTLPPrecedence(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_HEADERS", "")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "other.example:4317")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=Bearer x")

	t.Run("the flag wins over the environment and keeps its headers", func(t *testing.T) {
		cfg, ok, strict, err := resolveFor(t, "--otlp-endpoint", "collector.example:4317")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.True(t, strict)
		assert.Equal(t, "collector.example:4317", cfg.Endpoint)
		assert.False(t, cfg.Insecure)
		assert.Equal(t, "Bearer x", cfg.Headers["authorization"])
	})

	t.Run("without the flag the environment applies and is not strict", func(t *testing.T) {
		cfg, ok, strict, err := resolveFor(t)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.False(t, strict)
		assert.Equal(t, "other.example:4317", cfg.Endpoint)
		assert.Equal(t, "Bearer x", cfg.Headers["authorization"])
	})

	t.Run("--otlp-insecure sets Insecure", func(t *testing.T) {
		cfg, ok, _, err := resolveFor(t, "--otlp-endpoint", "collector.example:4317", "--otlp-insecure")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.True(t, cfg.Insecure)
	})

	t.Run("an http URL gives the host and implies insecure", func(t *testing.T) {
		cfg, ok, _, err := resolveFor(t, "--otlp-endpoint", "http://collector.example:4317")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "collector.example:4317", cfg.Endpoint)
		assert.True(t, cfg.Insecure)
	})

	t.Run("a flag error is strict", func(t *testing.T) {
		_, ok, strict, err := resolveFor(t, "--otlp-endpoint", "localhost:4317")
		require.Error(t, err)
		assert.False(t, ok)
		assert.True(t, strict)
	})
}

// siteOTLPEnv clears every OTEL_EXPORTER_OTLP_* variable the shell may export,
// so a test starts with no environment source whatever the ambient environment.
func siteOTLPEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "OTEL_EXPORTER_OTLP_") {
			t.Setenv(name, "") // registers the restore
			require.NoError(t, os.Unsetenv(name))
		}
	}
	t.Cleanup(func() { verbose = false })
}

// siteCollector is an in-process OTLP/gRPC endpoint on a loopback address,
// which a site's endpoint may be, that keeps spans and log records.
type siteCollector struct {
	*otlpCollector
	logs *logsCollector
}

func newSiteCollector(t *testing.T) *siteCollector {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	c := &siteCollector{otlpCollector: &otlpCollector{addr: lis.Addr().String()}, logs: &logsCollector{addr: lis.Addr().String()}}
	server := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(server, c.otlpCollector)
	collogspb.RegisterLogsServiceServer(server, c.logs)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
	return c
}

func (c *siteCollector) spanNames() []string {
	spans, _ := c.received()
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.GetName())
	}
	return names
}

// saveSiteConfig writes cfg to a config directory of its own and points
// XDG_CONFIG_HOME at it.
func saveSiteConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	require.NoError(t, cfg.Save())
}

// runSiteCommand runs nvkectl with args through execute and returns stdout,
// everything written to stderr, including the logs the command installs on the
// process's stderr, and the error.
func runSiteCommand(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd("v1.2.3", "test", "test", "test")
	root.SetArgs(args)
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	r, w, pipeErr := os.Pipe()
	require.NoError(t, pipeErr)
	realStderr, realLogger := os.Stderr, slog.Default()
	t.Cleanup(func() { os.Stderr = realStderr; slog.SetDefault(realLogger) })
	os.Stderr = w
	var logs bytes.Buffer
	copied := make(chan struct{})
	go func() { _, _ = io.Copy(&logs, r); close(copied) }()
	err = execute(context.Background(), root, "v1.2.3", &errOut)
	os.Stderr = realStderr
	slog.SetDefault(realLogger)
	require.NoError(t, w.Close())
	<-copied
	require.NoError(t, r.Close())
	return out.String(), errOut.String() + logs.String(), err
}

func TestSiteOTLPExportsACommandsTelemetryToItsSite(t *testing.T) {
	siteOTLPEnv(t)
	collector := newSiteCollector(t)
	controller := newFakeController(t)
	saveSiteConfig(t, &config.Config{CurrentSite: "t", Sites: map[string]config.SiteConfig{
		"t": {URL: controller.URL, OTLPEndpoint: collector.addr, OTLPInsecure: true},
	}})

	stdout, _, err := runSiteCommand(t, "get", "clusters", "--verbose")
	require.NoError(t, err)
	assert.Contains(t, stdout, "demo")

	spans, _ := collector.received()
	var rootSpan, clientSpan *tracepb.Span
	for _, span := range spans {
		switch {
		case span.GetName() == "nvkectl get clusters":
			rootSpan = span
		case span.GetKind() == tracepb.Span_SPAN_KIND_CLIENT:
			clientSpan = span
		}
	}
	require.NotNil(t, rootSpan, "the collector holds the command's root span: %v", collector.spanNames())
	require.NotNil(t, clientSpan, "the collector holds the HTTP client span: %v", collector.spanNames())
	traceID := hex.EncodeToString(rootSpan.GetTraceId())
	assert.Equal(t, traceID, hex.EncodeToString(clientSpan.GetTraceId()))
	assert.Equal(t, []string{traceID}, traceparents(t, controller), "the controller received the exported trace")
	controller.mu.Lock()
	parts := strings.Split(controller.headers[0].Get("Traceparent"), "-")
	controller.mu.Unlock()
	require.Len(t, parts, 4)
	assert.Equal(t, hex.EncodeToString(clientSpan.GetSpanId()), parts[2], "the controller's parent is the HTTP client span")
	assert.Contains(t, collector.logs.received(), "command finished", "the logs are exported too")
}

func TestSiteOTLPSiteFlagUsesThatSitesEndpoint(t *testing.T) {
	siteOTLPEnv(t)
	current, other := newSiteCollector(t), newSiteCollector(t)
	controller := newFakeController(t)
	saveSiteConfig(t, &config.Config{CurrentSite: "t", Sites: map[string]config.SiteConfig{
		"t":     {URL: controller.URL, OTLPEndpoint: current.addr, OTLPInsecure: true},
		"other": {URL: controller.URL, OTLPEndpoint: other.addr, OTLPInsecure: true},
	}})

	_, _, err := runSiteCommand(t, "get", "clusters", "--site", "other")
	require.NoError(t, err)

	assert.Contains(t, other.spanNames(), "nvkectl get clusters")
	assert.Empty(t, current.spanNames(), "the current site's collector receives nothing")
}

func TestSiteOTLPSiteEndpointWinsOverEnvironment(t *testing.T) {
	siteOTLPEnv(t)
	siteCollector, envCollector := newSiteCollector(t), newSiteCollector(t)
	controller := newFakeController(t)
	saveSiteConfig(t, &config.Config{CurrentSite: "t", Sites: map[string]config.SiteConfig{
		"t": {URL: controller.URL, OTLPEndpoint: siteCollector.addr, OTLPInsecure: true},
	}})
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+envCollector.addr)
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=Bearer example-token")

	_, _, err := runSiteCommand(t, "get", "clusters")
	require.NoError(t, err)

	spans, md := siteCollector.received()
	assert.NotEmpty(t, spans)
	assert.Equal(t, []string{"Bearer example-token"}, md.Get("authorization"), "the environment supplies the headers")
	assert.Empty(t, envCollector.spanNames(), "the environment's endpoint receives nothing")
}

func TestSiteOTLPWithoutSiteEndpointFallsBackToEnvironment(t *testing.T) {
	siteOTLPEnv(t)
	envCollector := newSiteCollector(t)
	controller := newFakeController(t)
	saveSiteConfig(t, &config.Config{CurrentSite: "t", Sites: map[string]config.SiteConfig{"t": {URL: controller.URL}}})
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+envCollector.addr)

	_, _, err := runSiteCommand(t, "get", "clusters")
	require.NoError(t, err)

	assert.Contains(t, envCollector.spanNames(), "nvkectl get clusters")
}

// A command that loads no site never exports to one.
func TestSiteOTLPCommandsWithoutASiteExportNothing(t *testing.T) {
	for name, args := range map[string][]string{
		"version":                {"version"},
		"render":                 {"render", "deploy-path"},
		"get bootstrap-manifest": {"get", "bootstrap-manifest"},
		"config view":            {"config", "view"},
	} {
		t.Run(name, func(t *testing.T) {
			siteOTLPEnv(t)
			collector := newSiteCollector(t)
			saveSiteConfig(t, &config.Config{CurrentSite: "t", Sites: map[string]config.SiteConfig{
				"t": {URL: "http://127.0.0.1:1", OTLPEndpoint: collector.addr, OTLPInsecure: true},
			}})

			// render fails for want of its flags; the export step still must not use the site.
			_, _, _ = runSiteCommand(t, append(args, "--verbose")...)

			assert.Empty(t, collector.spanNames())
			assert.Empty(t, collector.logs.received())
		})
	}
}

func TestSiteOTLPUnreachableEndpointNeverFailsTheCommand(t *testing.T) {
	siteOTLPEnv(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closed := lis.Addr().String()
	require.NoError(t, lis.Close())
	controller := newFakeController(t)

	saveSiteConfig(t, &config.Config{CurrentSite: "t", Sites: map[string]config.SiteConfig{"t": {URL: controller.URL}}})
	baseOut, baseErr, err := runSiteCommand(t, "get", "clusters")
	require.NoError(t, err)
	assert.Zero(t, strings.Count(baseErr, exportWarning))

	saveSiteConfig(t, &config.Config{CurrentSite: "t", Sites: map[string]config.SiteConfig{
		"t": {URL: controller.URL, OTLPEndpoint: closed, OTLPInsecure: true},
	}})
	started := time.Now()
	stdout, stderr, err := runSiteCommand(t, "get", "clusters")

	require.NoError(t, err)
	assert.Less(t, time.Since(started), 6*time.Second, "the flush is bounded")
	assert.Equal(t, baseOut, stdout, "stdout is unchanged")
	assert.Equal(t, 1, strings.Count(stderr, exportWarning), "one warning for the unreachable endpoint:\n%s", stderr)
}

// A site whose stored endpoint cannot be used warns and leaves the command alone.
func TestSiteOTLPBadSiteEndpointWarnsAndContinues(t *testing.T) {
	siteOTLPEnv(t)
	controller := newFakeController(t)
	saveSiteConfig(t, &config.Config{CurrentSite: "t", Sites: map[string]config.SiteConfig{
		"t": {URL: controller.URL, OTLPEndpoint: "http://"},
	}})

	stdout, stderr, err := runSiteCommand(t, "get", "clusters")

	require.NoError(t, err)
	assert.Contains(t, stdout, "demo")
	assert.Contains(t, stderr, "OTLP export is off")
}

// The export step reads the config best effort: the command reports a missing
// or corrupt config itself, and the step neither fails nor changes that.
func TestSiteOTLPMissingOrCorruptConfigDoesNotFailTheExportStep(t *testing.T) {
	for name, write := range map[string]func(t *testing.T){
		"missing": func(*testing.T) {},
		"corrupt": func(t *testing.T) {
			dir, err := config.Dir()
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("sites: [not: a map"), 0o600))
		},
	} {
		t.Run(name, func(t *testing.T) {
			siteOTLPEnv(t)
			envCollector := newSiteCollector(t)
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+envCollector.addr)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			write(t)

			_, stderr, err := runSiteCommand(t, "get", "clusters")

			require.Error(t, err, "the command reports its own config error")
			assert.Regexp(t, "site|config", err.Error(), "the error is the command's, not the export step's")
			assert.NotContains(t, stderr, "OTLP export is off")
			assert.Contains(t, envCollector.spanNames(), "nvkectl get clusters", "the environment still exports")
		})
	}
}

// siteResolveFor resolves the OTLP configuration of "get clusters", a command
// that uses a site, with the config in a temp dir and the given flags parsed.
func siteResolveFor(t *testing.T, cfg *config.Config, args ...string) (telemetry.OTLPConfig, bool, bool, error) {
	t.Helper()
	saveSiteConfig(t, cfg)
	root := NewRootCmd("test", "test", "test", "test")
	cmd, _, err := root.Find([]string{"get", "clusters"})
	require.NoError(t, err)
	require.NoError(t, root.ParseFlags(args))
	require.NoError(t, cmd.ParseFlags(args))
	t.Cleanup(func() { site = "" })
	return resolveOTLP(cmd)
}

func TestSiteOTLPSourceOrder(t *testing.T) {
	siteOTLPEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "env.example:4317")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=Bearer x")
	cfg := &config.Config{CurrentSite: "a", Sites: map[string]config.SiteConfig{
		"a":     {URL: "http://a", OTLPEndpoint: "a.example:4317", OTLPInsecure: true},
		"b":     {URL: "http://b", OTLPEndpoint: "https://b.example:4317"},
		"plain": {URL: "http://plain"},
		"bad":   {URL: "http://bad", OTLPEndpoint: "http://"},
	}}

	t.Run("the current site beats the environment, which gives only headers", func(t *testing.T) {
		got, ok, strict, err := siteResolveFor(t, cfg)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.False(t, strict)
		assert.Equal(t, "a.example:4317", got.Endpoint)
		assert.True(t, got.Insecure, "the site's insecure setting applies")
		assert.Equal(t, "Bearer x", got.Headers["authorization"])
	})

	t.Run("--site picks that site", func(t *testing.T) {
		got, ok, _, err := siteResolveFor(t, cfg, "--site", "b")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "b.example:4317", got.Endpoint)
		assert.False(t, got.Insecure)
	})

	t.Run("a site without an endpoint falls back to the environment", func(t *testing.T) {
		got, ok, strict, err := siteResolveFor(t, cfg, "--site", "plain")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.False(t, strict)
		assert.Equal(t, "env.example:4317", got.Endpoint)
	})

	t.Run("an unknown site falls back to the environment", func(t *testing.T) {
		got, ok, _, err := siteResolveFor(t, cfg, "--site", "nope")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "env.example:4317", got.Endpoint)
	})

	t.Run("a bad site endpoint is an error that is not strict", func(t *testing.T) {
		_, ok, strict, err := siteResolveFor(t, cfg, "--site", "bad")
		require.Error(t, err)
		assert.False(t, ok)
		assert.False(t, strict)
	})

	t.Run("a command that uses no site ignores the config", func(t *testing.T) {
		saveSiteConfig(t, cfg)
		root := NewRootCmd("test", "test", "test", "test")
		cmd, _, err := root.Find([]string{"version"})
		require.NoError(t, err)
		got, ok, _, err := resolveOTLP(cmd)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "env.example:4317", got.Endpoint)
	})
}

// A command that loads a site must carry the annotation, or it never exports to
// the site's endpoint. Whether a command loads a site cannot be checked from the
// tree, so the set is listed here: a new command that uses a site and is
// missing from this list fails review, not silently.
func TestSiteOTLPAnnotatedCommands(t *testing.T) {
	var got []string
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Annotations[usesSiteAnnotation] == "true" {
			got = append(got, cmd.CommandPath())
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(NewRootCmd("test", "test", "test", "test"))
	slices.Sort(got)

	assert.Equal(t, []string{
		"nvkectl create cluster",
		"nvkectl create clusterrelease",
		"nvkectl create machineimage",
		"nvkectl create nodepool",
		"nvkectl create nodepoolrelease",
		"nvkectl delete cluster",
		"nvkectl delete clusterrelease",
		"nvkectl delete machineimage",
		"nvkectl delete nodepool",
		"nvkectl delete nodepoolrelease",
		"nvkectl deprecate clusterrelease",
		"nvkectl deprecate nodepoolrelease",
		"nvkectl get clusterreleases",
		"nvkectl get clusters",
		"nvkectl get kubeconfig",
		"nvkectl get machineimages",
		"nvkectl get nodepoolreleases",
		"nvkectl get nodepools",
		"nvkectl get providers",
		"nvkectl patch cluster",
		"nvkectl patch nodepool",
	}, got)
}
