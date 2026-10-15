# upagent-anywhere

Install and keep up the machines that run [upstacked-agent][releases].

```sh
upagent-anywhere --target-host 10.30.100.20
```

It logs in over SSH, works out what the machine is, and offers what fits:

| The machine | What it offers |
|---|---|
| runs anything but NixOS, or NixOS it did not build | install NixOS with upstacked-agent, erasing a disk |
| was built from an older [agent-hosts][hosts] | update it |
| is current | change its settings, or give its agent a new auth key |

Run it again whenever you like; it only asks. Installing replaces the
operating system, so it waits until you type the machine's name.

[releases]: https://github.com/upstacked/agent-releases
[hosts]: https://github.com/upstacked/agent-hosts

## Install

```sh
brew install upstacked/tools/upagent-anywhere
```

Installing runs [nixos-anywhere] through Nix, so the machine you run it from
needs Nix with flakes enabled. It needs no Linux builder: the new system is
built on the target.

[nixos-anywhere]: https://github.com/nix-community/nixos-anywhere

## What it installs

NixOS from agent-hosts, the central configuration every agent machine is
built from. Nothing about one machine is committed there. What is particular
to it lives on the machine, in `/etc/upagent`:

- `host.json`: its hostname, what the agent does, how it is updated, and the
  SSH keys that may log in as `upstacked`, which are the only ones it trusts
- `facter.json`: its hardware, as nixos-anywhere found it
- `flake.nix` and `flake.lock`: agent-hosts, locked at the commit it runs

The agent's auth key is written to `/var/lib/upagent/secrets/authkey`,
readable only by root. It is spent the first time the agent enrols.

## Auth keys, from Upstacked

Each machine's agent joins with a key of its own, minted for it when it is
installed, so an infrastructure has as many agents as you install. You log in
once with a device code, approved in the portal where you are already logged
in with MFA; no password passes through this tool:

```sh
upagent-anywhere login
```

Installing then asks which of your infrastructures the agent joins, and has
Upstacked mint a key for it. Upstacked mints only for an infrastructure your
user may update. The session is kept in
`~/.config/upagent-anywhere/credentials.json`, readable only by you, until it
expires or `upagent-anywhere logout`.

The Upstacked API is `--upstacked-url`, or `UPSTACKED_API_URL` as the `ups`
CLI reads it. A key minted elsewhere can still be piped in with
`--auth-key-stdin`.

## Who may log in

A machine trusts exactly the SSH keys chosen when it was installed. The tool
asks, in three steps:

1. which of the keys it found to trust: the public keys in `~/.ssh`, and those
   in the agent ssh uses (an `IdentityAgent` such as 1Password's included)
2. whether to also let Upstacked's operators in with the Upstacked management
   key, `SHA256:2wrh0NhQt7Y/VQvyylP/OdmXiqYbRhaFltALIOYtyMo`, whose public half
   ships with the tool (`internal/sshkeys/upstacked-management.pub`)
3. any other key, pasted or as the path of a `.pub` file

It refuses to install a machine no key could log in to. Without a terminal,
pass `--ssh-key` (a key or a `.pub` file, repeatable) and `--management-key`.
Changing settings without naming keys keeps the ones the machine has.

## Updates

Each machine is either:

- **pull**: it follows agent-hosts on a timer (every hour by default)
- **push**: it changes only when you run `upagent-anywhere update`

Both run the same `upagent-update` on the machine, and leave its outcome in
`/var/lib/upagent/last-update.json`, which `status` reports.

## Without a terminal

Every prompt has a flag, and nothing prompts when stdin is not a terminal; it
fails naming the flag instead. Secrets are read from stdin, never argv.
`--yes` answers confirmations, but never erases a disk: that takes `--erase`
naming the disk.

```sh
upagent-anywhere login            # once, approving it in the portal
upagent-anywhere --target-host 10.30.100.20 --user ubuntu \
  --hostname agent-07 --mode collector,monitor --updates pull \
  --ssh-key ~/.ssh/id_ed25519.pub --management-key --infrastructure 8 \
  --disk /dev/sda --erase /dev/sda --yes

upagent-anywhere status --target-host 10.30.100.20 --json
upagent-anywhere update --target-host 10.30.100.20
```

Exit codes: 0 success, 1 failure, 2 usage, 3 cannot log in, 5 the machine is
not in a state the command applies to.
