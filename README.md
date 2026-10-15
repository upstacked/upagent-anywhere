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
  SSH keys that may log in as `upstacked`
- `facter.json`: its hardware, as nixos-anywhere found it
- `flake.nix` and `flake.lock`: agent-hosts, locked at the commit it runs

The agent's auth key is written to `/var/lib/upagent/secrets/authkey`,
readable only by root. It is spent the first time the agent enrols.

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
printf '%s' "$KEY" | upagent-anywhere --target-host 10.30.100.20 --user ubuntu \
  --hostname agent-07 --mode collector,monitor --updates pull \
  --disk /dev/sda --erase /dev/sda --auth-key-stdin --yes

upagent-anywhere status --target-host 10.30.100.20 --json
upagent-anywhere update --target-host 10.30.100.20
```

Exit codes: 0 success, 1 failure, 2 usage, 3 cannot log in, 5 the machine is
not in a state the command applies to.
