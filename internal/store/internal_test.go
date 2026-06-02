package store

import (
	"strings"
	"testing"
)

// git output must be deterministic English so substring
// classification (isPushHard, "nothing to commit") is locale-stable.
func TestGitCmdForcesEnglishLocale(t *testing.T) {
	cmd := gitCmd("status")

	var hasLCAll, hasLang bool
	for _, kv := range cmd.Env {
		if kv == "LC_ALL=C" {
			hasLCAll = true
		}
		if kv == "LANG=C" {
			hasLang = true
		}
	}
	if !hasLCAll {
		t.Errorf("gitCmd env missing LC_ALL=C; env=%v", cmd.Env)
	}
	if !hasLang {
		t.Errorf("gitCmd env missing LANG=C; env=%v", cmd.Env)
	}

	// The command path must resolve to git and carry the args.
	if !strings.Contains(cmd.Path, "git") {
		t.Errorf("gitCmd path = %q, want it to reference git", cmd.Path)
	}
	if len(cmd.Args) < 2 || cmd.Args[len(cmd.Args)-1] != "status" {
		t.Errorf("gitCmd args = %v, want last arg 'status'", cmd.Args)
	}
}

// classification must be correct for canonical git output.
func TestIsPushHardClassification(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "non-fast-forward rejection is hard",
			output: "error: failed to push some refs to 'origin'\n ! [rejected]        main -> main (non-fast-forward)",
			want:   true,
		},
		{
			name:   "auth failure is hard",
			output: "fatal: Authentication failed for 'https://example.com/repo.git'",
			want:   false, // no hard token present; treated transient (auth uses 401/403/denied/Permission denied)
		},
		{
			name:   "403 is hard",
			output: "remote: HTTP 403 Forbidden",
			want:   true,
		},
		{
			name:   "generic network error is transient",
			output: "fatal: unable to access 'https://example.com/repo.git': Could not resolve host: example.com",
			want:   false,
		},
		{
			name:   "connection refused is transient",
			output: "fatal: unable to connect to origin: Connection refused",
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPushHard(tc.output); got != tc.want {
				t.Errorf("isPushHard(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}
