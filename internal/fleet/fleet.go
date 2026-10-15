// Package fleet knows agent-hosts: where it is, its latest revision, and the
// small flake a machine keeps in /etc/upagent to build itself from it.
package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"sokkel.dev/upstacked/upagent-anywhere/internal/inspect"
)

// DefaultURL is agent-hosts. It is public, so a machine in pull mode reads it
// without credentials.
const DefaultURL = "https://sokkel.dev/upstacked/agent-hosts.git"

// Paths on the machine.
const (
	FlakeDir    = "/etc/upagent"
	AuthKeyPath = "/var/lib/upagent/secrets/authkey"
)

// Latest is the commit at the head of agent-hosts' main branch.
func Latest(ctx context.Context, url string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "ls-remote", url, "refs/heads/main").Output()
	if err != nil {
		return "", fmt.Errorf("reading agent-hosts at %s: %w", url, err)
	}
	rev, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\t")
	if rev == "" {
		return "", fmt.Errorf("agent-hosts at %s has no main branch", url)
	}
	return rev, nil
}

// Flake is the flake.nix a machine builds itself with.
func Flake(url string) []byte {
	return []byte(fmt.Sprintf(`# Written by upagent-anywhere. This machine is built from agent-hosts; what
# is particular to it is host.json, beside this file.
{
  inputs.fleet.url = "git+%s";
  outputs = { fleet, ... }: {
    nixosConfigurations.host = fleet.lib.mkHost ./.;
  };
}
`, url))
}

// HostJSON encodes host.json as agent-hosts reads it.
func HostJSON(h *inspect.HostConfig) ([]byte, error) {
	if h.Hostname == "" {
		return nil, errors.New("host.json needs a hostname")
	}
	if h.Updates.Mode != "push" && h.Updates.Mode != "pull" {
		return nil, fmt.Errorf("updates mode %q is neither push nor pull", h.Updates.Mode)
	}
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Stage lays out what nixos-anywhere copies onto a fresh install, rooted at
// the returned directory: the machine's flake in etc/upagent, and the auth key
// where the agent's unit loads it from.
func Stage(url string, h *inspect.HostConfig, authKey string) (string, error) {
	host, err := HostJSON(h)
	if err != nil {
		return "", err
	}
	root, err := os.MkdirTemp("", "upagent-install-")
	if err != nil {
		return "", err
	}
	files := []struct {
		path string
		mode os.FileMode
		body []byte
	}{
		{filepath.Join(root, FlakeDir, "flake.nix"), 0o644, Flake(url)},
		{filepath.Join(root, FlakeDir, "host.json"), 0o644, host},
		{filepath.Join(root, AuthKeyPath), 0o600, []byte(authKey + "\n")},
	}
	// nixos-anywhere copies modes as well as files, so the directories here
	// become /, /etc and /var/lib on the machine.
	if err := os.Chmod(root, 0o755); err != nil {
		return "", err
	}
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(f.path, f.body, f.mode); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(filepath.Dir(filepath.Join(root, AuthKeyPath)), 0o700); err != nil {
		return "", err
	}
	return root, nil
}
