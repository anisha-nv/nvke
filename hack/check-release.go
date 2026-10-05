// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command check-release validates a publication tag using the workspace's pinned SemVer library.
package main

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
)

func managed(component string) bool {
	switch component {
	case "controller", "agent", "cli", "os-image", "cluster-api-provider-provisioned":
		return true
	}
	return false
}

func prefix(component string) string {
	if component == "repository" {
		return "v"
	}
	return component + "/v"
}

func parseTag(component, tag string) (*semver.Version, error) {
	if !strings.HasPrefix(tag, prefix(component)) {
		return nil, fmt.Errorf("tag %q does not belong to %s", tag, component)
	}
	v, err := semver.StrictNewVersion(strings.TrimPrefix(tag, prefix(component)))
	if err != nil {
		return nil, fmt.Errorf("parsing %q: %w", tag, err)
	}
	if v.Metadata() != "" {
		return nil, fmt.Errorf("release tags cannot contain build metadata")
	}
	if v.Prerelease() != "" {
		n, ok := strings.CutPrefix(v.Prerelease(), "rc.")
		value, parseErr := strconv.ParseUint(n, 10, 64)
		if !ok || parseErr != nil || n != strconv.FormatUint(value, 10) {
			return nil, fmt.Errorf("release tags require rc.N")
		}
	}
	return v, nil
}

func check(component, base, tag, head string, refs map[string]string) error {
	v, err := parseTag(component, tag)
	if err != nil {
		return err
	}
	commit := refs[tag]
	if peeled := refs[tag+"^{}"]; peeled != "" {
		commit = peeled
	}
	if commit == "" || commit != head {
		return fmt.Errorf("remote tag %s must name the checked-out commit", tag)
	}
	for prior := range refs {
		stable, parseErr := parseTag(component, prior)
		if prior != tag && parseErr == nil && stable.Prerelease() == "" &&
			stable.Major() == v.Major() && stable.Minor() == v.Minor() && !v.GreaterThan(stable) {
			return fmt.Errorf("rejecting %s: %s is already stable", tag, prior)
		}
	}
	if managed(component) {
		configured, parseErr := semver.StrictNewVersion(strings.TrimPrefix(base, "v"))
		if parseErr != nil || !strings.HasPrefix(base, "v") || configured.Prerelease() != "" || configured.Metadata() != "" {
			return fmt.Errorf("%s/VERSION must contain a release base vX.Y.Z", component)
		}
		core := fmt.Sprintf("%d.%d.%d", v.Major(), v.Minor(), v.Patch())
		if configured.String() != core {
			return fmt.Errorf("%s must match %s/VERSION (%s)", tag, component, base)
		}
		first := prefix(component) + core + "-rc.0"
		if v.Prerelease() != "" && v.Prerelease() != "rc.0" && refs[first] == "" {
			return fmt.Errorf("release series must start with %s", first)
		}
	}
	return nil
}

func git(args ...string) (string, error) {
	command := exec.Command("git", args...)
	// checkout disables persisted credentials. Keep the existing job token in
	// this subprocess environment, scoped to GitHub, never in argv or on disk.
	if args[0] == "ls-remote" && os.Getenv("GH_TOKEN") != "" {
		count := 0
		if value := os.Getenv("GIT_CONFIG_COUNT"); value != "" {
			var err error
			count, err = strconv.Atoi(value)
			if err != nil || count < 0 || count == int(^uint(0)>>1) {
				return "", fmt.Errorf("invalid GIT_CONFIG_COUNT")
			}
		}
		header := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+os.Getenv("GH_TOKEN")))
		command.Env = append(os.Environ(), fmt.Sprintf("GIT_CONFIG_COUNT=%d", count+1),
			fmt.Sprintf("GIT_CONFIG_KEY_%d=http.https://github.com/.extraheader", count),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", count, header))
	}
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: check-release <component>")
	}
	component := os.Args[1]
	var base []byte
	var err error
	if managed(component) {
		base, err = os.ReadFile(component + "/VERSION")
		if err != nil {
			return fmt.Errorf("reading VERSION: %w", err)
		}
	}
	output, err := git("ls-remote", "--tags", "origin", "refs/tags/"+prefix(component)+"*")
	if err != nil {
		return err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		sha, ref, ok := strings.Cut(line, "\trefs/tags/")
		if ok {
			refs[ref] = sha
		}
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	return check(component, strings.TrimSpace(string(base)), strings.TrimPrefix(os.Getenv("GITHUB_REF"), "refs/tags/"), head, refs)
}

func main() {
	if err := run(); err != nil {
		slog.Error("release tag check failed", "error", err)
		os.Exit(1)
	}
}
