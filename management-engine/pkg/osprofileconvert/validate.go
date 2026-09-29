// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package osprofileconvert

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits and patterns match the MachineImage, ClusterRelease, and
// NodePoolRelease CRD markers. Failing here names the profile; applying an
// invalid manifest would fail later with a schema error that does not.
const (
	maxResourceNameLen = 253
	maxArchitectureLen = 32
	maxVariantLen      = 63
	maxOSVersionLen    = 32
	maxIPXELen         = 16384
	maxUserDataLen     = 65536
)

var (
	resourceName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	dnsLabel     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	osVersion    = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)
)

func validateProfile(name string, meta profileMetadata) error {
	if name == "" || utf8.RuneCountInString(name) > maxResourceNameLen || !resourceName.MatchString(name) {
		return fmt.Errorf("profile %q: name must be a Kubernetes DNS subdomain of at most %d characters", name, maxResourceNameLen)
	}
	if meta.Architecture == "" || utf8.RuneCountInString(meta.Architecture) > maxArchitectureLen || !dnsLabel.MatchString(meta.Architecture) {
		return fmt.Errorf("profile %q: architecture %q must be a lowercase DNS label of at most %d characters", name, meta.Architecture, maxArchitectureLen)
	}
	if meta.Variant != "" && (utf8.RuneCountInString(meta.Variant) > maxVariantLen || !dnsLabel.MatchString(meta.Variant)) {
		return fmt.Errorf("profile %q: variant %q must be a lowercase DNS label of at most %d characters", name, meta.Variant, maxVariantLen)
	}
	if meta.UbuntuVersion == "" || utf8.RuneCountInString(meta.UbuntuVersion) > maxOSVersionLen || !osVersion.MatchString(meta.UbuntuVersion) {
		return fmt.Errorf("profile %q: ubuntu_version %q must be a dotted numeric version", name, meta.UbuntuVersion)
	}
	if _, err := time.Parse(time.DateOnly, meta.BuildDate); err != nil {
		return fmt.Errorf("profile %q: build_date %q must be YYYY-MM-DD", name, meta.BuildDate)
	}
	return nil
}

func validateIPXE(script string) error {
	if utf8.RuneCountInString(script) == 0 || utf8.RuneCountInString(script) > maxIPXELen {
		return fmt.Errorf("ipxeScript must be 1 to %d characters", maxIPXELen)
	}
	if !strings.HasPrefix(script, "#!ipxe\n") && !strings.HasPrefix(script, "#!ipxe\r\n") {
		return errors.New("ipxeScript must begin with a #!ipxe line of its own")
	}
	if strings.TrimSpace(strings.ReplaceAll(script, "#!ipxe", "")) == "" {
		return errors.New("ipxeScript must contain a command after the #!ipxe line")
	}
	if err := balancedDelimiters(script); err != nil {
		return fmt.Errorf("ipxeScript %w", err)
	}
	return nil
}

func validateUserData(script string) error {
	if strings.TrimSpace(script) == "" || utf8.RuneCountInString(script) > maxUserDataLen {
		return fmt.Errorf("userDataTemplate must be 1 to %d non-blank characters", maxUserDataLen)
	}
	headers := []string{
		"#cloud-config\n",
		"#cloud-config\r\n",
		"#!",
		"#include",
		"#cloud-boothook",
		"#part-handler",
		"#cloud-config-archive",
		"Content-Type:",
	}
	ok := false
	for _, header := range headers {
		if strings.HasPrefix(script, header) {
			ok = true
			break
		}
	}
	if !ok {
		return errors.New("userDataTemplate must begin with a cloud-init header")
	}
	if err := balancedDelimiters(script); err != nil {
		return fmt.Errorf("userDataTemplate %w", err)
	}
	return nil
}

// balancedDelimiters matches the CRD rule that counts '{{' and '}}' splits.
// For a non-empty separator, equal split sizes are equal counts.
func balancedDelimiters(script string) error {
	if strings.Count(script, "{{") != strings.Count(script, "}}") {
		return errors.New("has unbalanced Go template delimiters")
	}
	return nil
}
