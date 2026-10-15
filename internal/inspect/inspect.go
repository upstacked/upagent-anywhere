// Package inspect works out what a machine is from what it says over SSH:
// whether it runs NixOS, whether agent-hosts built it and from which revision,
// and how its agent is doing.
package inspect

import (
	"bufio"
	"context"
	"encoding/json"
	"strconv"
	"strings"
)

// State is where a machine stands, and so what upagent-anywhere offers.
type State int

const (
	// Foreign runs something other than NixOS. Installing replaces it.
	Foreign State = iota
	// Unmanaged runs NixOS that agent-hosts did not build.
	Unmanaged
	// Outdated was built from an older agent-hosts than the latest.
	Outdated
	// Current was built from the latest agent-hosts.
	Current
)

func (s State) String() string {
	switch s {
	case Foreign:
		return "not NixOS"
	case Unmanaged:
		return "NixOS, not managed by agent-hosts"
	case Outdated:
		return "managed, behind agent-hosts"
	case Current:
		return "managed and current"
	}
	return "unknown"
}

// Machine is what one inspection found.
type Machine struct {
	Hostname string
	OS       string // os-release ID: nixos, ubuntu, ...
	OSName   string // os-release PRETTY_NAME
	Arch     string // uname -m
	UEFI     bool
	Disks    []Disk

	// Set on a machine agent-hosts built.
	Managed bool
	// FleetRevision is the agent-hosts commit the running system was built
	// from. Pending is true when a newer system is built but not switched to.
	FleetRevision string
	Pending       bool
	Host          *HostConfig
	LastUpdate    *LastUpdate

	Agent Agent
}

type Disk struct {
	Path  string
	Bytes int64
	Model string
}

type Agent struct {
	Installed bool
	Version   string
	Active    bool
	// Enrolled is the agent's own word, from its status endpoint.
	Enrolled bool
	AgentID  uint64
}

type LastUpdate struct {
	At      string `json:"at"`
	OK      bool   `json:"ok"`
	Fleet   string `json:"fleet"`
	Message string `json:"message"`
}

// HostConfig is /etc/upagent/host.json, as agent-hosts reads it.
type HostConfig struct {
	Hostname     string   `json:"hostname"`
	System       string   `json:"system"`
	Disk         string   `json:"disk"`
	SSHKeys      []string `json:"sshKeys"`
	StateVersion string   `json:"stateVersion,omitempty"`
	Agent        struct {
		Settings map[string]string `json:"settings"`
	} `json:"agent"`
	Updates struct {
		Mode     string `json:"mode"`
		Interval string `json:"interval,omitempty"`
	} `json:"updates"`
}

// State judges the machine against the latest agent-hosts revision.
func (m *Machine) State(latest string) State {
	switch {
	case m.OS != "nixos":
		return Foreign
	case !m.Managed:
		return Unmanaged
	case latest != "" && m.FleetRevision != latest:
		return Outdated
	}
	return Current
}

// Runner is what Inspect needs from a target.
type Runner interface {
	Run(ctx context.Context, command string) (string, error)
}

// script prints one `key=value` per line. Every probe tolerates its tool being
// absent, since the machine may be anything.
const script = `
. /etc/os-release 2>/dev/null
echo "os=${ID:-unknown}"
echo "os_name=${PRETTY_NAME:-unknown}"
echo "arch=$(uname -m)"
echo "hostname=$(hostname 2>/dev/null || cat /etc/hostname)"
[ -d /sys/firmware/efi ] && echo "uefi=1"
if command -v lsblk >/dev/null 2>&1; then
  lsblk -dbnpo NAME,SIZE,TYPE,MODEL 2>/dev/null | while read -r name size type model; do
    [ "$type" = disk ] && echo "disk=$name $size $model"
  done
fi
if [ -f /etc/upagent-release.json ]; then
  echo "release=$(tr -d '\n' < /etc/upagent-release.json)"
fi
if [ -f /etc/upagent/host.json ]; then
  echo "host=$(tr -d '\n' < /etc/upagent/host.json)"
fi
if [ -f /var/lib/upagent/last-update.json ]; then
  echo "last_update=$(tr -d '\n' < /var/lib/upagent/last-update.json)"
fi
if [ -e /run/current-system ] && [ -e /nix/var/nix/profiles/system ]; then
  [ "$(readlink -f /run/current-system)" != "$(readlink -f /nix/var/nix/profiles/system)" ] && echo "pending=1"
fi
if command -v upstacked-agent >/dev/null 2>&1; then
  echo "agent_version=$(upstacked-agent --version 2>/dev/null | head -n1)"
  systemctl is-active --quiet upstacked-agent 2>/dev/null && echo "agent_active=1"
  echo "agent_status=$(upstacked-agent status --json 2>/dev/null | tr -d '\n')"
fi
true
`

// Inspect reads the machine.
func Inspect(ctx context.Context, r Runner) (*Machine, error) {
	out, err := r.Run(ctx, "sh -s <<'UPAGENT'\n"+script+"\nUPAGENT")
	if err != nil {
		return nil, err
	}
	return parse(out), nil
}

func parse(out string) *Machine {
	m := &Machine{}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch key {
		case "os":
			m.OS = value
		case "os_name":
			m.OSName = value
		case "arch":
			m.Arch = value
		case "hostname":
			m.Hostname = value
		case "uefi":
			m.UEFI = true
		case "disk":
			f := strings.Fields(value)
			if len(f) < 2 {
				continue
			}
			size, _ := strconv.ParseInt(f[1], 10, 64)
			m.Disks = append(m.Disks, Disk{Path: f[0], Bytes: size, Model: strings.Join(f[2:], " ")})
		case "release":
			var rel struct {
				Fleet string `json:"fleet"`
			}
			if json.Unmarshal([]byte(value), &rel) == nil {
				m.Managed, m.FleetRevision = true, rel.Fleet
			}
		case "host":
			var h HostConfig
			if json.Unmarshal([]byte(value), &h) == nil {
				m.Host = &h
			}
		case "last_update":
			var u LastUpdate
			if json.Unmarshal([]byte(value), &u) == nil {
				m.LastUpdate = &u
			}
		case "pending":
			m.Pending = true
		case "agent_version":
			m.Agent.Installed, m.Agent.Version = true, value
		case "agent_active":
			m.Agent.Active = true
		case "agent_status":
			var st struct {
				Delivery *struct {
					Enrolled bool   `json:"enrolled"`
					AgentID  uint64 `json:"agent_id"`
				} `json:"delivery"`
			}
			if json.Unmarshal([]byte(value), &st) == nil && st.Delivery != nil {
				m.Agent.Enrolled, m.Agent.AgentID = st.Delivery.Enrolled, st.Delivery.AgentID
			}
		}
	}
	return m
}

// System is the Nix system for the machine's architecture.
func (m *Machine) System() string {
	switch m.Arch {
	case "aarch64", "arm64":
		return "aarch64-linux"
	}
	return "x86_64-linux"
}
