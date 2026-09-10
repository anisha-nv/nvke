// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package activities

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"net"
	"strconv"
	"strings"
	"text/template"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/NVIDIA/nke/controller/internal/constants"
	"github.com/NVIDIA/nke/controller/internal/db"
)

//go:embed capi_spire_agent.conf.tmpl
var capiSpireAgentConfigTemplateText string

const (
	capiNativeSpireServerAddressEnvironmentKey = "SPIRE_SERVER_ADDRESS"
	capiNativeSpireServerPortEnvironmentKey    = "SPIRE_SERVER_PORT"
	capiNativeSpireTrustDomainEnvironmentKey   = "SPIRE_TRUST_DOMAIN"
)

type capiCloudInitConfig struct {
	WriteFiles []capiCloudInitFile `yaml:"write_files,omitempty"`
	RunCmd     []string            `yaml:"runcmd,omitempty"`
}

type capiCloudInitFile struct {
	Path        string `yaml:"path"`
	Owner       string `yaml:"owner,omitempty"`
	Permissions string `yaml:"permissions,omitempty"`
	Content     string `yaml:"content"`
}

func (f capiCloudInitFile) MarshalYAML() (interface{}, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	appendMappingNode(node, "path", 0, f.Path, "")
	if f.Owner != "" {
		appendMappingNode(node, "owner", 0, f.Owner, "")
	}
	if f.Permissions != "" {
		appendMappingNode(node, "permissions", yaml.DoubleQuotedStyle, f.Permissions, "!!str")
	}
	appendMappingNode(node, "content", yaml.LiteralStyle, f.Content, "")
	return node, nil
}

func appendMappingNode(node *yaml.Node, key string, style yaml.Style, value, tag string) {
	valueNode := &yaml.Node{Kind: yaml.ScalarNode, Style: style, Value: value}
	if tag != "" {
		valueNode.Tag = tag
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		valueNode,
	)
}

func (a *Activities) renderCAPIBootstrapUserData(
	ctx context.Context,
	nodeSet db.NodeSet,
	spireJoinToken string,
) (string, error) {
	capiNodeSetProvider, err := nodeSet.CAPIProvider()
	if err != nil {
		return "", fmt.Errorf("failed to read node set capi provider: %w", err)
	}

	if capiNodeSetProvider.SpireAttestationMethod == db.SpireAttestationMethodJoinToken && spireJoinToken == "" {
		return "", fmt.Errorf("spire join token is required when infra.spireAttestationMethod is joinToken")
	}

	// A site stood up without SPIFFE has no SPIRE and no workload socket to
	// read a bundle from, so a node gets neither the bundle nor an agent
	// config. Asking anyway blocks provisioning: the dial fails, the activity
	// retries forever, and the cluster never leaves Provisioning.
	var trustDomain, trustBundle string
	if a.cfg.SpiffeEnabled {
		trustDomain, trustBundle, err = a.getTrustDomainBundle(ctx, a.cfg)
		if err != nil {
			return "", fmt.Errorf("failed to get trust domain and bundle: %w", err)
		}
	}

	files := make([]capiCloudInitFile, 0, len(capiNodeSetProvider.BootstrapFiles)+2)
	for _, file := range capiNodeSetProvider.BootstrapFiles {
		files = append(files, capiCloudInitFile{
			Path:        file.Path,
			Owner:       file.Owner,
			Permissions: file.Permissions,
			Content:     file.Content,
		})
	}
	if a.cfg.SpiffeEnabled {
		nativeSpireEnvironment, err := buildCAPINativeSpireEnvironment(
			capiNodeSetProvider.SpireAttestationMethod,
			a.cfg.SpireServerAddress,
			a.cfg.SpireServerPort,
			trustDomain,
			spireJoinToken,
		)
		if err != nil {
			return "", fmt.Errorf("failed to render native SPIRE environment: %w", err)
		}

		files = append(files,
			capiCloudInitFile{
				Path:        constants.CAPISpireTrustBundlePath,
				Owner:       "root:root",
				Permissions: "0644",
				Content:     trustBundle,
			},
			capiCloudInitFile{
				Path:        constants.CAPISpireAgentConfigPath,
				Owner:       "root:root",
				Permissions: "0644",
				Content: buildCAPISpireAgentConfig(
					capiNodeSetProvider.SpireAttestationMethod,
					a.cfg.SpireServerAddress,
					a.cfg.SpireServerPort,
					trustDomain,
					spireJoinToken,
				),
			},
			capiCloudInitFile{
				Path:        constants.CAPINativeSpireEnvironmentPath,
				Owner:       "root:root",
				Permissions: "0600",
				Content:     nativeSpireEnvironment,
			},
			capiCloudInitFile{
				Path:        constants.CAPINativeSpireTrustBundlePath,
				Owner:       "root:root",
				Permissions: "0644",
				Content:     trustBundle,
			},
		)
	}

	userData := capiCloudInitConfig{
		WriteFiles: files,
		RunCmd:     append([]string(nil), capiNodeSetProvider.PreKubeadmCommands...),
	}

	yamlBytes, err := yaml.Marshal(userData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal CAPI bootstrap cloud-init: %w", err)
	}

	return "#cloud-config\n" + string(yamlBytes), nil
}

func buildCAPISpireAgentConfig(
	attestationMethod db.SpireAttestationMethod,
	serverAddress, serverPort, trustDomain, spireJoinToken string,
) string {
	data := struct {
		ServerAddress   string
		ServerPort      string
		TrustDomain     string
		TrustBundlePath string
		SpireJoinToken  string
		UseJoinToken    bool
	}{
		ServerAddress:   serverAddress,
		ServerPort:      serverPort,
		TrustDomain:     trustDomain,
		TrustBundlePath: constants.CAPISpireTrustBundlePath,
		SpireJoinToken:  spireJoinToken,
		UseJoinToken:    attestationMethod == db.SpireAttestationMethodJoinToken,
	}

	var buf bytes.Buffer
	tmpl := template.Must(template.New("capi-spire-agent-config").Parse(capiSpireAgentConfigTemplateText))
	if err := tmpl.Execute(&buf, data); err != nil {
		panic(fmt.Sprintf("failed to render CAPI SPIRE agent config template: %v", err))
	}

	return buf.String()
}

func buildCAPINativeSpireEnvironment(
	attestationMethod db.SpireAttestationMethod,
	serverAddress, serverPort, trustDomain, spireJoinToken string,
) (string, error) {
	if err := validateCAPINativeSpireServerAddress(serverAddress); err != nil {
		return "", fmt.Errorf("%s: %w", capiNativeSpireServerAddressEnvironmentKey, err)
	}
	if err := validateCAPINativeSpireServerPort(serverPort); err != nil {
		return "", fmt.Errorf("%s: %w", capiNativeSpireServerPortEnvironmentKey, err)
	}
	parsedTrustDomain, err := spiffeid.TrustDomainFromString(trustDomain)
	if err != nil {
		return "", fmt.Errorf("%s: must be a valid SPIFFE trust domain: %w", capiNativeSpireTrustDomainEnvironmentKey, err)
	}
	if parsedTrustDomain.String() != trustDomain {
		return "", fmt.Errorf("%s: must be a bare SPIFFE trust domain", capiNativeSpireTrustDomainEnvironmentKey)
	}
	if strings.ContainsAny(trustDomain, "&|\"\\") {
		return "", fmt.Errorf("%s: must not contain native SPIRE configuration metacharacters", capiNativeSpireTrustDomainEnvironmentKey)
	}

	values := []struct {
		key   string
		value string
	}{
		{key: capiNativeSpireServerAddressEnvironmentKey, value: serverAddress},
		{key: capiNativeSpireServerPortEnvironmentKey, value: serverPort},
		{key: capiNativeSpireTrustDomainEnvironmentKey, value: trustDomain},
	}
	if attestationMethod == db.SpireAttestationMethodJoinToken {
		values = append(values, struct {
			key   string
			value string
		}{key: "SPIRE_JOIN_TOKEN", value: spireJoinToken})
	}

	var env strings.Builder
	for _, value := range values {
		quotedValue, err := quoteCAPINativeSpireEnvironmentValue(value.value)
		if err != nil {
			return "", fmt.Errorf("%s: %w", value.key, err)
		}
		env.WriteString(value.key)
		env.WriteByte('=')
		env.WriteString(quotedValue)
		env.WriteByte('\n')
	}
	return env.String(), nil
}

func validateCAPINativeSpireServerAddress(address string) error {
	if net.ParseIP(address) != nil {
		return nil
	}

	hostname := strings.ToLower(strings.TrimSuffix(address, "."))
	if len(validation.IsDNS1123Subdomain(hostname)) != 0 {
		return fmt.Errorf("must be a valid host name or IP address")
	}
	return nil
}

func validateCAPINativeSpireServerPort(port string) error {
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 {
		return fmt.Errorf("must be a decimal port between 1 and 65535")
	}
	return nil
}

func quoteCAPINativeSpireEnvironmentValue(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("must not contain NUL or line breaks")
	}

	replacer := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		`$`, `\$`,
		"`", "\\`",
	)
	return `"` + replacer.Replace(value) + `"`, nil
}
