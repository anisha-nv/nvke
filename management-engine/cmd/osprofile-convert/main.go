// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command osprofile-convert reads an OS profile tree and writes one
// MachineImage, one ClusterRelease, and one NodePoolRelease per profile.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/dsx-ai-factory/nke/management-engine/pkg/osprofileconvert"
)

func main() {
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "osprofile-convert: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("osprofile-convert", flag.ContinueOnError)
	var profiles, output string
	var check bool
	flags.StringVar(&profiles, "profiles", "", "Profiles directory containing osProfiles (controller PROFILES_DIR)")
	flags.StringVar(&output, "output", "", "Directory where generated manifests are written")
	flags.BoolVar(&check, "check", false, "Fail if output is stale or a published spec would change; write nothing")
	flags.Usage = func() {
		out := flags.Output()
		if _, err := fmt.Fprint(out, `Usage: osprofile-convert --profiles <dir> --output <dir> [--check]

Reads metadata.yaml, optional deprecated.yaml, ipxeScript, and userData.tmpl.
Writes machineimages, clusterreleases, and nodepoolreleases named after each profile.
An existing manifest may change only its release deprecation state.

`); err != nil {
			return
		}
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if profiles == "" || output == "" {
		return errors.New("both --profiles and --output are required")
	}

	catalog, err := osprofileconvert.Convert(profiles)
	if err != nil {
		return err
	}
	result, err := catalog.Sync(output, osprofileconvert.SyncOptions{Check: check})
	if err != nil {
		return err
	}
	if check || result.Written == 0 {
		fmt.Println("os profile manifests are current")
		return nil
	}
	noun := "profiles"
	if len(catalog.Profiles) == 1 {
		noun = "profile"
	}
	fmt.Printf("wrote %d %s (%d manifests)\n", len(catalog.Profiles), noun, result.Written)
	return nil
}
