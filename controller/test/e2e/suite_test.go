// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"flag"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/dsx-ai-factory/nke/controller/test/e2e/internal/kube"
	"github.com/dsx-ai-factory/nke/controller/test/e2e/internal/qualification"
)

var cfgPath string

// The qualification interface. `qualify run` passes the resolved selection
// through these three flags, and passes the same ones to the --dry-run that
// asks what a selection needs, so the specs a requirement describes are by
// construction the specs that will run.
//
// WHAT IS HONOURED TODAY. The selection is parsed, validated and logged, and
// `selection` is readable by any spec. No spec consults the versions yet, so
// a run qualifies the versions the working tree builds rather than the ones
// named. That gap is why BeforeSuite says so in the log rather than leaving
// it to be inferred: a run that names versions it did not deploy would record
// a pass the candidate never earned.
var (
	versionsFlag     string
	capabilitiesFlag string
	environmentFlag  string

	selection qualification.Selection
)

func init() {
	flag.StringVar(&cfgPath, "e2e.config", "e2e-config.yaml", "path to the E2E config file (missing file -> all defaults)")
	flag.StringVar(&versionsFlag, "e2e.versions", "", "component versions under qualification, as comma-separated name=version")
	flag.StringVar(&capabilitiesFlag, "e2e.capabilities", "", "the stream's capabilities, as comma-separated name=value")
	flag.StringVar(&environmentFlag, "e2e.environment", "", "the environment config to run against, by name")
}

var _ = BeforeSuite(func() {
	var err error
	selection, err = qualification.Parse(versionsFlag, capabilitiesFlag)
	// Expect rather than a returned error: a selection that does not parse
	// means the qualify tool and this suite disagree about the format, and
	// running the specs anyway would qualify something other than what was
	// asked for.
	Expect(err).NotTo(HaveOccurred())
	selection.Environment = environmentFlag

	GinkgoWriter.Println("qualification:", selection)
	if len(selection.Versions) > 0 {
		GinkgoWriter.Println("qualification: no spec pins versions yet; " +
			"this run qualifies what the working tree builds")
	}
})

// Specs reach NKE over a port-forward they share and establish lazily; this
// closes it. A no-op when no spec ever talked to NKE.
var _ = AfterSuite(func() {
	kube.CloseSharedForwarders()
})

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "NKE E2E Suite")
}
