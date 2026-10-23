// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkectl

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dsx-ai-factory/nvke/cli/internal/config"
	"github.com/dsx-ai-factory/nvke/cli/internal/telemetry"
)

const (
	otlpEndpointFlag = "otlp-endpoint"
	otlpInsecureFlag = "otlp-insecure"

	// usesSiteAnnotation marks a command that loads a site, so its telemetry
	// goes to that site's OTLP endpoint.
	usesSiteAnnotation = "nvkectl/uses-site"
)

// markUsesSite annotates cmds as commands that load a site. Whoever passes
// loadClient, loadSite or loadOperator to a command constructor calls it on the
// commands that constructor returns. Commands such as `create ... --dry-run=client`
// are annotated although they may contact nothing, because the annotation is per
// command and cannot see flag values; they export to the current site's endpoint
// when one is set.
func markUsesSite(cmds ...*cobra.Command) {
	for _, cmd := range cmds {
		if cmd.Annotations == nil {
			cmd.Annotations = map[string]string{}
		}
		cmd.Annotations[usesSiteAnnotation] = "true"
	}
}

// otlpSource reads one source of OTLP configuration. ok is false when the
// source names no endpoint. strict is true when an error should fail the
// command, because the operator asked for the export explicitly.
type otlpSource func(cmd *cobra.Command) (cfg telemetry.OTLPConfig, ok, strict bool, err error)

// otlpSources are the places OTLP configuration comes from, in order of
// precedence: the first that names an endpoint wins.
var otlpSources = []otlpSource{otlpFromFlags, otlpFromSite, otlpFromEnv} //nolint:gochecknoglobals // ordered lookup table

// resolveOTLP returns the OTLP configuration for the command about to run, from
// the first source that names an endpoint. strict is true when that endpoint,
// or an error about it, came from a flag, so the caller fails the command
// instead of continuing without export.
func resolveOTLP(cmd *cobra.Command) (cfg telemetry.OTLPConfig, ok, strict bool, err error) {
	for _, source := range otlpSources {
		cfg, ok, strict, err = source(cmd)
		if ok || err != nil {
			return cfg, ok, strict, err
		}
	}
	return telemetry.OTLPConfig{}, false, false, nil
}

// otlpFromFlags reads --otlp-endpoint and --otlp-insecure, which only
// "bootstrap site" registers. The endpoint must be reachable from the temporary
// controller in the bootstrap cluster, so a loopback address is rejected.
func otlpFromFlags(cmd *cobra.Command) (telemetry.OTLPConfig, bool, bool, error) {
	endpointFlag := cmd.Flags().Lookup(otlpEndpointFlag)
	if endpointFlag == nil {
		return telemetry.OTLPConfig{}, false, false, nil
	}
	insecureFlag := cmd.Flags().Lookup(otlpInsecureFlag)
	if !endpointFlag.Changed {
		if insecureFlag != nil && insecureFlag.Changed {
			return telemetry.OTLPConfig{}, false, true, errors.New("--" + otlpInsecureFlag + " applies to --" + otlpEndpointFlag + "; pass --" +
				otlpEndpointFlag + " too, or drop --" + otlpInsecureFlag)
		}
		return telemetry.OTLPConfig{}, false, false, nil
	}
	endpoint := endpointFlag.Value.String()
	if err := telemetry.RejectLoopback(endpoint); err != nil {
		return telemetry.OTLPConfig{}, false, true, fmt.Errorf("--%s: %w", otlpEndpointFlag, err)
	}
	host, insecure, err := telemetry.ParseEndpoint(endpoint)
	if err != nil {
		return telemetry.OTLPConfig{}, false, true, fmt.Errorf("--%s: %w", otlpEndpointFlag, err)
	}
	if flagInsecure, _ := cmd.Flags().GetBool(otlpInsecureFlag); flagInsecure {
		insecure = true
	}
	cfg := telemetry.OTLPConfig{Endpoint: host, Insecure: insecure, Headers: telemetry.HeadersFromEnv()}
	return cfg, true, true, nil
}

// otlpFromSite reads the OTLP endpoint of the site a command uses: the one
// --site names, else the current site. Only commands marked with markUsesSite
// have one. A config that cannot be read or a site that is missing gives no
// endpoint and no error: the command reports that itself, and export is best
// effort. A bad endpoint is not strict, because it was configured earlier and
// not typed on this command line. Headers come from the environment, as the
// config never stores them.
func otlpFromSite(cmd *cobra.Command) (telemetry.OTLPConfig, bool, bool, error) {
	if cmd.Annotations[usesSiteAnnotation] != "true" {
		return telemetry.OTLPConfig{}, false, false, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return telemetry.OTLPConfig{}, false, false, nil
	}
	siteCfg, err := resolveSiteConfig(cfg)
	if err != nil || siteCfg.OTLPEndpoint == "" {
		return telemetry.OTLPConfig{}, false, false, nil
	}
	host, insecure, err := telemetry.ParseEndpoint(siteCfg.OTLPEndpoint)
	if err != nil {
		return telemetry.OTLPConfig{}, false, false, fmt.Errorf("site OTLP endpoint: %w", err)
	}
	return telemetry.OTLPConfig{
		Endpoint: host,
		Insecure: insecure || siteCfg.OTLPInsecure,
		Headers:  telemetry.HeadersFromEnv(),
	}, true, false, nil
}

// otlpFromEnv reads the standard OTEL_EXPORTER_OTLP_* variables. A problem with
// them never fails the command.
func otlpFromEnv(*cobra.Command) (telemetry.OTLPConfig, bool, bool, error) {
	cfg, ok, err := telemetry.OTLPConfigFromEnv()
	return cfg, ok, false, err
}

// enableOTLP turns on OTLP export for run from the resolved configuration. It
// does nothing for a nil run. The returned error is fatal only when fatal is
// true; otherwise the caller reports it and carries on.
func enableOTLP(cmd *cobra.Command, run *telemetry.Run) (fatal bool, err error) {
	if run == nil {
		return false, nil
	}
	cfg, ok, strict, err := resolveOTLP(cmd)
	if err != nil || !ok {
		return strict, err
	}
	return strict, run.EnableOTLP(cfg)
}
