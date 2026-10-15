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

	"github.com/upstacked/upagent-anywhere/internal/inspect"
)

// DefaultRef is agent-hosts, as a flake reference. It is public, so a
// machine in pull mode reads it without credentials.
const DefaultRef = "github:upstacked/agent-hosts"

// Paths on the machine.
const (
	FlakeDir    = "/etc/upagent"
	AuthKeyPath = "/var/lib/upagent/secrets/authkey"
)

// GitURL is where git finds a flake reference: github:owner/repo, or
// git+<url> for a fork or a local checkout.
func GitURL(ref string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "github:"):
		path := strings.TrimPrefix(ref, "github:")
		if strings.Count(path, "/") != 1 {
			return "", fmt.Errorf("%s names no single repository", ref)
		}
		return "https://github.com/" + path + ".git", nil
	case strings.HasPrefix(ref, "git+"):
		url, _, _ := strings.Cut(strings.TrimPrefix(ref, "git+"), "?")
		return url, nil
	}
	return "", fmt.Errorf("%s is neither github:owner/repo nor git+<url>", ref)
}

// Pinned is ref at one commit.
func Pinned(ref, rev string) string {
	if strings.HasPrefix(ref, "github:") {
		return ref + "/" + rev
	}
	base, _, _ := strings.Cut(ref, "?")
	return base + "?rev=" + rev
}

// Latest is the commit at the head of agent-hosts' main branch.
func Latest(ctx context.Context, ref string) (string, error) {
	url, err := GitURL(ref)
	if err != nil {
		return "", err
	}
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

// Flake is the flake.nix a machine builds itself with. It names agent-hosts
// unpinned; the lock beside it holds the commit, so updating moves the lock.
func Flake(ref string) []byte {
	return []byte(fmt.Sprintf(`# Written by upagent-anywhere. This machine is built from agent-hosts; what
# is particular to it is host.json, beside this file.
{
  inputs.fleet.url = "%s";
  outputs = { fleet, ... }: {
    nixosConfigurations.host = fleet.lib.mkHost ./.;
  };
}
`, ref))
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
func Stage(ref string, h *inspect.HostConfig, authKey string) (string, error) {
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
		{filepath.Join(root, FlakeDir, "flake.nix"), 0o644, Flake(ref)},
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
