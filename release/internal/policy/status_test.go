// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package policy

import "testing"

func TestResultSettled(t *testing.T) {
	// Settled is what the Planner uses to decide against rerunning a cell.
	// error and unschedulable must not be settled: the answer is still
	// unknown, and skipping them would freeze a cell as never-run.
	for _, tc := range []struct {
		result Result
		want   bool
	}{
		{ResultPassed, true},
		{ResultFailed, true},
		{ResultError, false},
		{ResultUnschedulable, false},
		{ResultNone, false},
	} {
		t.Run(string(tc.result), func(t *testing.T) {
			if got := tc.result.Settled(); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func status(combination string, streams ...StreamResult) *Status {
	return &Status{Combinations: []StatusCombination{{ID: combination, Streams: streams}}}
}

func TestMergeUnionsStreamsUnderOneCombination(t *testing.T) {
	into := status("c1", StreamResult{Name: "a", Result: ResultPassed})
	into.Merge(status("c1", StreamResult{Name: "b", Result: ResultFailed}))

	if len(into.Combinations) != 1 {
		t.Fatalf("got %d combinations, want 1", len(into.Combinations))
	}
	if got := len(into.Combinations[0].Streams); got != 2 {
		t.Fatalf("got %d streams, want 2", got)
	}
	if got := into.Outcome("c1", "a"); got != ResultPassed {
		t.Errorf("stream a: got %q, want passed", got)
	}
	if got := into.Outcome("c1", "b"); got != ResultFailed {
		t.Errorf("stream b: got %q, want failed", got)
	}
}

func TestMergeKeepsCombinationsApart(t *testing.T) {
	into := status("c1", StreamResult{Name: "a", Result: ResultPassed})
	into.Merge(status("c2", StreamResult{Name: "a", Result: ResultFailed}))

	if len(into.Combinations) != 2 {
		t.Fatalf("got %d combinations, want 2", len(into.Combinations))
	}
	// Same stream name, different dependency versions. Collapsing these
	// would report one combination's failure against the other.
	if got := into.Outcome("c1", "a"); got != ResultPassed {
		t.Errorf("c1/a: got %q, want passed", got)
	}
	if got := into.Outcome("c2", "a"); got != ResultFailed {
		t.Errorf("c2/a: got %q, want failed", got)
	}
}

func TestMergeLaterStreamReplacesEarlier(t *testing.T) {
	// A rerun of one cell is the newer answer about it. Keeping both would
	// leave the aggregate choosing between two results for one stream.
	into := status("c1", StreamResult{Name: "a", Result: ResultError})
	into.Merge(status("c1", StreamResult{Name: "a", Result: ResultPassed}))

	if got := len(into.Combinations[0].Streams); got != 1 {
		t.Fatalf("got %d streams, want 1", got)
	}
	if got := into.Outcome("c1", "a"); got != ResultPassed {
		t.Errorf("got %q, want passed", got)
	}
}

func TestPassedRequiresEveryStream(t *testing.T) {
	s := status("c1",
		StreamResult{Name: "a", Result: ResultPassed},
		StreamResult{Name: "b", Result: ResultUnschedulable},
	)

	if s.Passed([]string{"a"}) != true {
		t.Error("a alone passed, so a scope selecting only a is covered")
	}
	if s.Passed([]string{"a", "b"}) != false {
		t.Error("b did not pass, so a scope selecting both is not covered")
	}
	if s.Passed([]string{"a", "c"}) != false {
		t.Error("c has no result at all, which is not a pass")
	}
}

func TestPassedOnAnEmptySelectionIsNotVacuousTruth(t *testing.T) {
	// A scope whose selector matches no stream proves nothing, and must not
	// report every candidate qualified at it. This is the same hazard as a
	// label filter that selects no spec.
	s := status("c1", StreamResult{Name: "a", Result: ResultPassed})
	if s.Passed(nil) {
		t.Error("an empty selection must not count as qualified")
	}
}

func TestOutcomeOfWhatWasNeverRun(t *testing.T) {
	s := status("c1", StreamResult{Name: "a", Result: ResultPassed})
	if got := s.Outcome("c1", "zz"); got != ResultNone {
		t.Errorf("unknown stream: got %q, want empty", got)
	}
	if got := s.Outcome("zz", "a"); got != ResultNone {
		t.Errorf("unknown combination: got %q, want empty", got)
	}
}

func TestResultName(t *testing.T) {
	if got, want := ResultName("controller", "v1.0.0"), "controller-v1.0.0"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
