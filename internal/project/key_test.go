package project_test

import (
	"strings"
	"testing"

	"github.com/CyberSecAuto-Labs/aimd/internal/project"
)

func TestDeriveKey_SSHUrl(t *testing.T) {
	key, err := project.DeriveKey("git@github.com:org/backend.git")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "github.com~org~backend"
	if key != want {
		t.Errorf("got %q, want %q", key, want)
	}
}

func TestDeriveKey_HTTPSWithDotGit(t *testing.T) {
	key, err := project.DeriveKey("https://github.com/org/backend.git")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "github.com~org~backend"
	if key != want {
		t.Errorf("got %q, want %q", key, want)
	}
}

func TestDeriveKey_HTTPSWithoutDotGit(t *testing.T) {
	key, err := project.DeriveKey("https://github.com/org/backend")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "github.com~org~backend"
	if key != want {
		t.Errorf("got %q, want %q", key, want)
	}
}

func TestDeriveKey_GitlabSSH(t *testing.T) {
	key, err := project.DeriveKey("git@gitlab.com:org/backend.git")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "gitlab.com~org~backend"
	if key != want {
		t.Errorf("got %q, want %q", key, want)
	}
}

func TestDeriveKey_BitbucketHTTPSWithUserInfo(t *testing.T) {
	key, err := project.DeriveKey("https://user@bitbucket.org/org/repo.git")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "bitbucket.org~org~repo"
	if key != want {
		t.Errorf("got %q, want %q", key, want)
	}
}

func TestDeriveKey_EmptyURL(t *testing.T) {
	_, err := project.DeriveKey("")
	if err == nil {
		t.Fatal("expected error for empty URL, got nil")
	}
}

// keys must be stable across SSH port presence and must never be
// destabilised by an '@' in the path.
func TestDeriveKey_StableAcrossFormats(t *testing.T) {
	cases := []struct {
		name      string
		remoteURL string
		want      string
	}{
		{
			name:      "ssh url with explicit port drops the port",
			remoteURL: "ssh://git@host:22/org/repo.git",
			want:      "host~org~repo",
		},
		{
			name:      "scp form for the same repo (unchanged)",
			remoteURL: "git@host:org/repo.git",
			want:      "host~org~repo",
		},
		{
			name:      "https unchanged",
			remoteURL: "https://github.com/org/backend.git",
			want:      "github.com~org~backend",
		},
		{
			name:      "ssh url without port",
			remoteURL: "ssh://git@host/org/repo.git",
			want:      "host~org~repo",
		},
		{
			name:      "at-sign in the path does not truncate the host",
			remoteURL: "https://github.com/org/re@po.git",
			want:      "github.com~org~re@po",
		},
		{
			name:      "scp form with at-sign in the path",
			remoteURL: "git@host:org/re@po.git",
			want:      "host~org~re@po",
		},
		{
			name:      "ssh url with non-numeric segment after colon keeps scp-style conversion",
			remoteURL: "ssh://git@host:org/repo.git",
			want:      "host~org~repo",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := project.DeriveKey(tc.remoteURL)
			if err != nil {
				t.Fatalf("DeriveKey(%q): unexpected error: %v", tc.remoteURL, err)
			}
			if got != tc.want {
				t.Errorf("DeriveKey(%q) = %q, want %q", tc.remoteURL, got, tc.want)
			}
		})
	}
}

// the two canonical forms for one repo must collapse to one key.
func TestDeriveKey_PortAndScpFormsMatch(t *testing.T) {
	a, errA := project.DeriveKey("ssh://git@host:22/org/repo.git")
	b, errB := project.DeriveKey("git@host:org/repo.git")
	if errA != nil || errB != nil {
		t.Fatalf("unexpected errors: %v, %v", errA, errB)
	}
	if a != b {
		t.Errorf("expected identical keys for the same repo, got %q and %q", a, b)
	}
}

func TestDeriveKeyFromPath_Format(t *testing.T) {
	key := project.DeriveKeyFromPath("/home/user/projects/myrepo")
	if !strings.HasPrefix(key, "local~") {
		t.Errorf("expected prefix 'local~', got %q", key)
	}
	parts := strings.SplitN(key, "~", 3)
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts separated by '~', got %d in %q", len(parts), key)
	}
	if parts[0] != "local" {
		t.Errorf("part[0] = %q, want 'local'", parts[0])
	}
	// hash part should be 8 hex chars
	hash := parts[1]
	if len(hash) != 8 {
		t.Errorf("hash part length = %d, want 8; hash = %q", len(hash), hash)
	}
	// basename part should be lowercase repo name
	if parts[2] != "myrepo" {
		t.Errorf("basename part = %q, want 'myrepo'", parts[2])
	}
}

func TestDeriveKeyFromPath_IsLowercase(t *testing.T) {
	key := project.DeriveKeyFromPath("/home/user/projects/MyRepo")
	if key != strings.ToLower(key) {
		t.Errorf("key %q is not all lowercase", key)
	}
}

func TestDeriveKeyFromPath_Deterministic(t *testing.T) {
	path := "/some/absolute/path/to/project"
	key1 := project.DeriveKeyFromPath(path)
	key2 := project.DeriveKeyFromPath(path)
	if key1 != key2 {
		t.Errorf("DeriveKeyFromPath not deterministic: %q != %q", key1, key2)
	}
}
