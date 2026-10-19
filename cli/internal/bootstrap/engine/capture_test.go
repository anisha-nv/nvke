// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCaptureCommandProcess(t *testing.T) {
	if os.Getenv("NVKE_TEST_CAPTURE_PROCESS") != "1" {
		return
	}
	_, _ = fmt.Fprint(os.Stdout, os.Getenv("NVKE_TEST_CAPTURE_STDOUT"))
	_, _ = fmt.Fprint(os.Stderr, os.Getenv("NVKE_TEST_CAPTURE_STDERR"))
	code, err := strconv.Atoi(os.Getenv("NVKE_TEST_CAPTURE_EXIT"))
	require.NoError(t, err)
	os.Exit(code)
}

func captureProcessArgs(t *testing.T, stdout, stderr string, code int) (map[string]string, string, []string) {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	return map[string]string{
		"NVKE_TEST_CAPTURE_PROCESS": "1",
		"NVKE_TEST_CAPTURE_STDOUT":  stdout,
		"NVKE_TEST_CAPTURE_STDERR":  stderr,
		"NVKE_TEST_CAPTURE_EXIT":    strconv.Itoa(code),
	}, executable, []string{"-test.run=^TestCaptureCommandProcess$"}
}

func TestExecCommandRunnerKeepsCombinedRunContract(t *testing.T) {
	for _, code := range []int{0, 23} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			env, executable, args := captureProcessArgs(t, "command output\n", "command diagnostic\n", code)
			out, err := (ExecCommandRunner{}).Run(t.Context(), env, executable, args...)
			require.Contains(t, out, "command output\n")
			require.Contains(t, out, "command diagnostic\n")
			if code == 0 {
				require.NoError(t, err)
			} else {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				require.Equal(t, code, exit.ExitCode())
			}
		})
	}
}

type captureProcessRunner struct {
	env        map[string]string
	executable string
	args       []string
	calls      []fakeCall
	runs       int
}

func (r *captureProcessRunner) Run(context.Context, map[string]string, string, ...string) (string, error) {
	r.runs++
	return "", errors.New("unexpected command after image inspection")
}

func (r *captureProcessRunner) Capture(ctx context.Context, env map[string]string, name string, args ...string) (string, string, error) {
	r.calls = append(r.calls, fakeCall{env: env, name: name, args: args})
	return (ExecCommandRunner{}).Capture(ctx, r.env, r.executable, r.args...)
}

func TestImageInspectionSeparatesProcessDiagnostics(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	pair := digest + " " + digest + "\n"
	warning := "level=warning msg=\"treating lima version `f47461e` as very latest release\"\n"
	for _, test := range []struct {
		name, stdout, stderr string
		code                 int
		wantError            string
	}{
		{"warning with valid stdout", pair, warning, 0, ""},
		{"failed inspection", pair, "runtime inspection denied\n", 23, "runtime inspection denied"},
		{"malformed stdout with digests on stderr", "invalid\n", pair, 0, "want two sha256 digests"},
		{"empty stdout with digests on stderr", "", pair, 0, "want two sha256 digests"},
	} {
		t.Run(test.name, func(t *testing.T) {
			env, executable, args := captureProcessArgs(t, test.stdout, test.stderr, test.code)
			runner := &captureProcessRunner{env: env, executable: executable, args: args}
			var diagnostics bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&diagnostics, nil))
			err := flattenToHostPlatform(t.Context(), runner, "nerdctl", "example.test/controller:local", log)
			if test.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.wantError)
			}
			if test.code != 0 {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				require.Equal(t, test.code, exit.ExitCode())
			}
			var diagnostic struct {
				Stderr string `json:"stderr"`
			}
			require.NoError(t, json.Unmarshal(diagnostics.Bytes(), &diagnostic))
			require.Equal(t, test.stderr, diagnostic.Stderr)
			require.Len(t, runner.calls, 1)
			require.Equal(t, "nerdctl", runner.calls[0].name)
			require.Equal(t, []string{"image", "inspect", "--mode=native", "--format", "{{.Image.Target.Digest}} {{.ManifestDesc.Digest}}", "example.test/controller:local"}, runner.calls[0].args)
			require.Zero(t, runner.runs, "inspection failure must not pull, retag, save or load")
		})
	}
}
