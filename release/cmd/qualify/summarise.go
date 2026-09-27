// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/dsx-ai-factory/nke/release/internal/policy"
	"github.com/dsx-ai-factory/nke/release/internal/record"
	"github.com/dsx-ai-factory/nke/release/internal/suite"
)

func summariseCmd() *cobra.Command {
	var (
		reports     string
		pattern     string
		conclusion  string
		artifacts   string
		combination string
		stream      string
		keepReport  bool
	)

	cmd := &cobra.Command{
		Use:   "summarise",
		Short: "Turn a CI lane's ginkgo reports into an outcome publish can record",
		Long: `Turn a CI lane's ginkgo reports into an outcome publish can record.

This is the half of ` + "`run`" + ` that does not run anything. Streams whose specs
need a provisioned cluster are executed by the backend E2E lane, which builds
the kind cluster, installs the Tilt providers and checks out the internal
CAPNICo source -- none of which a Go process can arrange for itself. So the
lane runs the suite and this reads what it wrote.

A lane runs ginkgo more than once. Every report matching --pattern is folded
into one summary, because a stream is qualified when everything it selected
passed rather than when its last leg did.

--conclusion is the lane job's own result. It decides between a suite that ran
and asserted false and a suite that broke, which are different answers about
the candidate: the first is a qualification result and the second is not.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ok, err := laneSucceeded(conclusion)
			if err != nil {
				return err
			}

			legs, names, err := readReports(reports, pattern)
			if err != nil {
				return err
			}

			dir := record.EntryDir(artifacts, combination, stream)

			// No report at all is an error outcome, not a failure: the lane
			// never got as far as running a spec, so nothing was asserted.
			// Writing the outcome anyway is what lets publish record the
			// unknown rather than leave the next plan to re-derive it.
			if len(legs) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(),
					"no ginkgo report under %s matching %q; recording an error\n", reports, pattern)
				return record.WriteHandoff(dir, record.Handoff{Result: policy.ResultError}, nil)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "folding %d leg(s): %v\n", len(legs), names)

			summary, err := suite.Fold(legs)
			if err != nil {
				// The reports exist and say nothing countable. Same reasoning
				// as above, and the error is still returned so the step fails.
				if writeErr := record.WriteHandoff(dir,
					record.Handoff{Result: policy.ResultError}, nil); writeErr != nil {
					return fmt.Errorf("%w (and recording it failed: %w)", err, writeErr)
				}
				return err
			}

			outcome := suite.Outcome(summary, ok)
			fmt.Fprintf(cmd.OutOrStdout(),
				"%s on %s: %s (passed %d, failed %d, skipped %d)\n",
				combination, stream, outcome, summary.Passed, summary.Failed, summary.Skipped)

			var report []byte
			if keepReport {
				if report, err = mergeReports(legs); err != nil {
					return err
				}
			}
			return record.WriteHandoff(dir, record.Handoff{
				Result:  outcome,
				Summary: &summary,
			}, report)
		},
	}

	cmd.Flags().StringVar(&reports, "reports", "", "directory holding the lane's ginkgo JSON reports (required)")
	cmd.Flags().StringVar(&pattern, "pattern", "report*.json", "glob selecting one report per leg")
	cmd.Flags().StringVar(&conclusion, "conclusion", "", "the lane job's conclusion: success, failure, cancelled, skipped (required)")
	cmd.Flags().StringVar(&artifacts, "artifacts", "artifacts", "directory to write the outcome into")
	cmd.Flags().StringVar(&combination, "combination", "", "combination id (required)")
	cmd.Flags().StringVar(&stream, "stream", "", "stream name (required)")
	// Off by default. A ginkgo report carries captured spec output and failure
	// text, and this outcome is uploaded without passing through the lane's
	// diagnostics redaction, so the default has to be the one that cannot
	// carry a secret out of a run.
	cmd.Flags().BoolVar(&keepReport, "keep-report", false, "include the merged ginkgo report alongside the outcome")

	for _, f := range []string{"reports", "conclusion", "combination", "stream"} {
		cobra.CheckErr(cmd.MarkFlagRequired(f))
	}
	return cmd
}

// laneSucceeded reads a GitHub job conclusion.
//
// Unknown values are an error rather than falsy. A conclusion this does not
// recognise most likely means the expression that produced it was wrong, and
// treating that as a failure would record a verdict on the candidate derived
// from a workflow bug.
func laneSucceeded(conclusion string) (bool, error) {
	switch conclusion {
	case "success":
		return true, nil
	case "failure", "cancelled", "skipped", "timed_out":
		return false, nil
	default:
		return false, fmt.Errorf("unrecognised lane conclusion %q", conclusion)
	}
}

// readReports loads every leg's report in a stable order, so the same legs
// produce the same merged report.
func readReports(dir, pattern string) ([][]byte, []string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return nil, nil, fmt.Errorf("matching %q under %s: %w", pattern, dir, err)
	}
	slices.Sort(matches)

	var legs [][]byte
	var names []string
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			return nil, nil, fmt.Errorf("reading %s: %w", m, err)
		}
		// A leg that was configured and never ran leaves an empty file. It is
		// not an error, and counting it as a leg would make Fold report a
		// suite that asserted nothing.
		if len(b) == 0 {
			continue
		}
		legs = append(legs, b)
		names = append(names, filepath.Base(m))
	}
	return legs, names, nil
}

// mergeReports concatenates the legs into one ginkgo report.
//
// A ginkgo report is already an array of suite reports, so three legs become
// one array of three rather than a new shape that needs its own reader.
func mergeReports(legs [][]byte) ([]byte, error) {
	var all []json.RawMessage
	for i, b := range legs {
		var suites []json.RawMessage
		if err := json.Unmarshal(b, &suites); err != nil {
			return nil, fmt.Errorf("parsing leg %d: %w", i, err)
		}
		all = append(all, suites...)
	}
	b, err := json.Marshal(all)
	if err != nil {
		return nil, fmt.Errorf("encoding the merged report: %w", err)
	}
	return b, nil
}
