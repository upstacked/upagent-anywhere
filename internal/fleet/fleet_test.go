package fleet

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/upstacked/upagent-anywhere/internal/inspect"
)

func TestStageLaysOutWhatTheMachineNeeds(t *testing.T) {
	h := &inspect.HostConfig{Hostname: "agent-01", System: "x86_64-linux", Disk: "/dev/sda"}
	h.Updates.Mode = "pull"
	root, err := Stage(DefaultRef, h, "upsk-abc")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)

	modes := map[string]os.FileMode{
		".":                               0o755,
		"etc":                             0o755,
		"etc/upagent":                     0o755,
		"etc/upagent/host.json":           0o644,
		"var/lib/upagent/secrets":         0o700,
		"var/lib/upagent/secrets/authkey": 0o600,
	}
	for p, want := range modes {
		fi, err := os.Stat(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s has mode %o, want %o", p, got, want)
		}
	}
	key, _ := os.ReadFile(filepath.Join(root, AuthKeyPath))
	if string(key) != "upsk-abc\n" {
		t.Errorf("authkey = %q", key)
	}
}

func TestHostJSONRefusesAnUnknownUpdateMode(t *testing.T) {
	h := &inspect.HostConfig{Hostname: "a"}
	h.Updates.Mode = "sometimes"
	if _, err := HostJSON(h); err == nil {
		t.Fatal("accepted an update mode of sometimes")
	}
}

func TestReferencesPinAndResolve(t *testing.T) {
	cases := []struct{ ref, git, pinned string }{
		{"github:upstacked/agent-hosts", "https://github.com/upstacked/agent-hosts.git", "github:upstacked/agent-hosts/abc"},
		{"git+file:///src/agent-hosts?ref=main", "file:///src/agent-hosts", "git+file:///src/agent-hosts?rev=abc"},
	}
	for _, c := range cases {
		git, err := GitURL(c.ref)
		if err != nil || git != c.git {
			t.Errorf("GitURL(%s) = %s, %v; want %s", c.ref, git, err, c.git)
		}
		if got := Pinned(c.ref, "abc"); got != c.pinned {
			t.Errorf("Pinned(%s) = %s, want %s", c.ref, got, c.pinned)
		}
	}
	if _, err := GitURL("https://example.com/x"); err == nil {
		t.Error("accepted a bare URL")
	}
}
