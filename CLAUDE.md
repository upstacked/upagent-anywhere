# CLAUDE.md

Guidance for Claude Code working in this repository.

## What this is

`upagent-anywhere` installs NixOS with upstacked-agent on a machine over SSH,
and keeps it current with agent-hosts. Installing erases a disk on a real
machine, often one in a customer's network.

## Releasing

Releases are cut by pushing an annotated tag to `main`. GoReleaser then builds four
platform archives, publishes a GitHub Release, and updates the Homebrew cask in
`upstacked/homebrew-tools`. History is linear and commits land directly on `main`.

```sh
gofmt -l . && go vet ./... && go test -race -count=1 ./...   # what CI runs
git push origin main
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
gh run watch --repo upstacked/upagent-anywhere               # confirm it published
```

Only tag a commit that is already pushed and green: the tag is what publishes, and a
release cannot be quietly amended once the cask has moved. `flake.nix` pins the Go
modules by `vendorHash`; change it with go.mod, or `nix build` fails.

Full detail: `docs/releasing.md`.

## The rules the code is built around

- **Erasing a disk is confirmed by naming it.** The operator types the machine's name, or
  passes `--erase <disk>`. `--yes` never reaches it.
- **Never prompt when stdin is not a terminal.** Fail with the flag the caller needed.
- **Secrets come from stdin, never argv.**
- **Running it again is safe.** It inspects first and offers only what fits the state.
- **The machine's state lives on the machine.** Nothing per host is committed to
  agent-hosts; `inspect` reads it back over SSH.

## Conventions

- Commands in `internal/cli/`; SSH in `internal/remote`, reading a machine in
  `internal/inspect`, agent-hosts in `internal/fleet`, nixos-anywhere in `internal/install`.
- `internal/errs` is the exit-code contract, shared with the Upstacked CLI.
- Comments explain non-obvious behaviour and the reason a guard exists. Do not narrate code
  that speaks for itself.
