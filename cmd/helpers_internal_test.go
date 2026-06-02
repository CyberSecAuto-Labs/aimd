package cmd

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestIsNothingToCommit(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"nothing to commit", errors.New("git commit: exit status 1 — nothing to commit, working tree clean"), true},
		{"nothing added", errors.New("nothing added to commit but untracked files present"), true},
		{"real failure", errors.New("git commit: fatal: unable to write"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNothingToCommit(tc.err); got != tc.want {
				t.Errorf("isNothingToCommit(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestConfirmPrompt(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"lower y", "y\n", true},
		{"upper Y", "Y\n", true},
		{"explicit n", "n\n", false},
		{"empty/EOF", "", false},
		{"other word", "maybe\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := confirmPrompt(io.Discard, strings.NewReader(tc.input), "Continue?")
			if err != nil {
				t.Fatalf("confirmPrompt error: %v", err)
			}
			if got != tc.want {
				t.Errorf("confirmPrompt(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
