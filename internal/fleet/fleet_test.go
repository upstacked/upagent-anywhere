package fleet

import (
	"os"
	"path/filepath"
	"testing"

	"sokkel.dev/upstacked/upagent-anywhere/internal/inspect"
)

func TestStageLaysOutWhatTheMachineNeeds(t *testing.T) {
	h := &inspect.HostConfig{Hostname: "agent-01", System: "x86_64-linux", Disk: "/dev/sda"}
	h.Updates.Mode = "pull"
	root, err := Stage(DefaultURL, h, "upsk-abc")
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
