// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command qualify drives release qualification: it resolves what to qualify,
// runs it, records the result, and renders what is qualified.
//
// The five verbs are separate because each is a different job in the
// workflow, with a different trigger and a different failure meaning. The
// only state any of them shares is what is in the registry.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is stamped at build time. Declared so the -X the Makefile passes
// lands somewhere: the linker ignores an -X for a symbol the binary lacks,
// and it does so silently.
var version = "dev"

func main() {
	if err := root().Execute(); err != nil {
		// Cobra has already printed usage errors. Anything else is ours and
		// is printed here, once.
		var silent silentError
		if !errors.As(err, &silent) {
			fmt.Fprintln(os.Stderr, "qualify:", err)
		}
		os.Exit(1)
	}
}

// silentError marks an error cobra has already reported.
type silentError struct{ error }

func root() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "qualify",
		Short:   "Drive NKE release qualification",
		Version: version,
		Long: `Drive NKE release qualification.

The tool has five verbs, one per job in the workflow.

  plan       resolve what to qualify
  run        acquire capacity, call the suite, release
  publish    record one stream's outcome
  aggregate  rebuild one artifact from every result and decide what is qualified
  generate   render the dashboard from it

Nothing before aggregate says whether a candidate is qualified: each stream
publishes only its own outcome, and the judgement is over all of them.`,
		SilenceUsage: true,
	}
	cmd.AddCommand(planCmd(), runCmd(), publishCmd(), aggregateCmd(), generateCmd())
	return cmd
}
