// Package install puts NixOS on a machine with nixos-anywhere, and updates a
// machine that already runs it.
package install

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/upstacked/upagent-anywhere/internal/fleet"
	"github.com/upstacked/upagent-anywhere/internal/remote"
)

// NixosAnywhere is pinned: an install wipes a disk, and what does it should
// not change underneath an operator between two runs.
const NixosAnywhere = "github:nix-community/nixos-anywhere/1.13.0"

// Fresh replaces whatever runs on target with NixOS built from the staged
// root, as fleet.Stage lays it out. It kexecs into an installer, partitions
// the disk host.json names, and builds on the machine itself, so the operator
// needs no Linux builder.
//
// The machine's flake is locked to rev, the revision this run judged the
// machine against, rather than whatever main is when the build starts. The
// lock, not flake.nix, carries it, so pull mode still moves on from there.
func Fresh(ctx context.Context, t *remote.Target, root, ref, rev string, stdout, stderr io.Writer) error {
	flakeDir := filepath.Join(root, fleet.FlakeDir)

	lockArgs := []string{"flake", "lock", flakeDir}
	if rev != "" {
		lockArgs = append(lockArgs, "--override-input", "fleet", fleet.Pinned(ref, rev))
	}
	lock := exec.CommandContext(ctx, "nix", lockArgs...)
	lock.Stdout, lock.Stderr = stdout, stderr
	if err := lock.Run(); err != nil {
		return fmt.Errorf("locking the machine's flake: %w", err)
	}

	args := []string{
		"run", NixosAnywhere, "--",
		"--flake", flakeDir + "#host",
		"--target-host", t.Address(),
		"--build-on", "remote",
		"--generate-hardware-config", "nixos-facter", filepath.Join(flakeDir, "facter.json"),
		"--extra-files", root,
	}
	if t.Identity != "" {
		args = append(args, "-i", t.Identity)
	}
	cmd := exec.CommandContext(ctx, "nix", args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = stdout, stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nixos-anywhere: %w", err)
	}
	return nil
}

// Update moves a managed machine to rev of agent-hosts, or the latest when rev
// is empty, by running the same upagent-update its pull timer runs.
func Update(ctx context.Context, t *remote.Target, rev string, stdout, stderr io.Writer) error {
	cmd := t.Sudo() + " upagent-update"
	if rev != "" {
		cmd += " --rev " + rev
	}
	return t.Stream(ctx, cmd, stdout, stderr)
}
