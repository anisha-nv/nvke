// Throwaway tool: installs Postgres/Temporal/CAPI/CWE onto an existing
// cluster's kubeconfig instead of the ephemeral bootstrap Kind cluster.
// Not part of nkectl; run with `go run ./hack/selfinstall <kubeconfig>`.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/dsx-ai-factory/nke/cli/internal/bootstrap/cwedeploy"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: selfinstall <kubeconfig-path>")
		os.Exit(1)
	}
	kubeconfigPath := os.Args[1]

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg := cwedeploy.DefaultConfig()
	cfg.DisableMTLS = true
	cfg.SPIFFEClusterName = "dev-local"

	engine := cwedeploy.New(cwedeploy.ExecCommandRunner{}, logger)
	if err := engine.Deploy(context.Background(), kubeconfigPath, cfg); err != nil {
		logger.Error("deploy failed", "err", err)
		os.Exit(1)
	}
	logger.Info("deploy succeeded")
}
