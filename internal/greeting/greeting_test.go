// Copyright (c) 2026, NVIDIA CORPORATION & AFFILIATES.  All rights reserved.

package greeting

import "testing"

func TestGreet(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "named recipient", input: "NKE", want: "Hello, NKE!"},
		{name: "empty falls back to world", input: "", want: "Hello, world!"},
		{name: "whitespace is preserved", input: "  ", want: "Hello,   !"},
		{name: "unicode recipient", input: "wörld", want: "Hello, wörld!"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Greet(tt.input); got != tt.want {
				t.Errorf("Greet(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
