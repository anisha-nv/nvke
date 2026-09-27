// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package record

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nke/release/internal/policy"
)

// Handoff is what `run` leaves on disk for `publish` to read.
//
// The two verbs are separate processes in the same job, so the outcome has to
// survive between them. It is deliberately not the finished record: publish
// builds that from the plan, which is what lets a stream that never started
// be published as unschedulable by a step that never saw a suite run.
type Handoff struct {
	Result   policy.Result    `json:"result"`
	Summary  *policy.Summary  `json:"summary,omitempty"`
	Teardown *policy.Teardown `json:"teardown,omitempty"`
}

// HandoffFile is where the outcome sits inside an entry's artifact directory.
const HandoffFile = "outcome.json"

// EntryDir is one entry's directory under --artifacts. Two dimensions,
// because a stream name is unique only within a combination.
func EntryDir(artifacts, combination, stream string) string {
	return filepath.Join(artifacts, sanitise(combination), sanitise(stream))
}

// WriteHandoff records an outcome and, when there is one, the suite's report.
func WriteHandoff(dir string, h Handoff, report []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("making %s: %w", dir, err)
	}
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the outcome: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, HandoffFile), append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing the outcome: %w", err)
	}
	if len(report) == 0 {
		return nil
	}
	path := filepath.Join(dir, filepath.FromSlash(ReportFile))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("making %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, report, 0o644); err != nil {
		return fmt.Errorf("writing the report: %w", err)
	}
	return nil
}

// ReadHandoff reads back what run left.
//
// A missing outcome file means the run never reached the point of writing
// one: the job was cancelled, or the runner died. That is reported as an
// error rather than defaulted, because defaulting it would publish a
// judgement nothing produced.
func ReadHandoff(dir string) (Handoff, []byte, error) {
	b, err := os.ReadFile(filepath.Join(dir, HandoffFile))
	if err != nil {
		return Handoff{}, nil, fmt.Errorf("reading the outcome: %w", err)
	}
	var h Handoff
	if err := json.Unmarshal(b, &h); err != nil {
		return Handoff{}, nil, fmt.Errorf("parsing the outcome: %w", err)
	}
	if h.Result == policy.ResultNone {
		return Handoff{}, nil, fmt.Errorf("%s names no result", filepath.Join(dir, HandoffFile))
	}
	// An absent report is normal: a stream that was never scheduled has an
	// outcome and nothing to report.
	report, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ReportFile)))
	if err != nil && !os.IsNotExist(err) {
		return Handoff{}, nil, fmt.Errorf("reading the report: %w", err)
	}
	return h, report, nil
}
