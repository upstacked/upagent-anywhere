package inspect

import "testing"

func TestAnUbuntuMachineIsForeign(t *testing.T) {
	m := parse(`os=ubuntu
os_name=Ubuntu 26.04 LTS
arch=x86_64
hostname=upstacked-monitoring
disk=/dev/sda 34359738368 QEMU HARDDISK
agent_version=0.3.0
agent_active=1
agent_status={"delivery":{"enrolled":true,"agent_id":5}}
`)
	if got := m.State("abc"); got != Foreign {
		t.Fatalf("state = %v, want foreign", got)
	}
	if len(m.Disks) != 1 || m.Disks[0].Path != "/dev/sda" || m.Disks[0].Model != "QEMU HARDDISK" {
		t.Fatalf("disks = %+v", m.Disks)
	}
	if !m.Agent.Enrolled || m.Agent.AgentID != 5 || !m.Agent.Active {
		t.Fatalf("agent = %+v", m.Agent)
	}
	if m.System() != "x86_64-linux" {
		t.Fatalf("system = %s", m.System())
	}
}

func TestAManagedMachineIsJudgedByItsRevision(t *testing.T) {
	m := parse(`os=nixos
arch=aarch64
release={"fleet":"1111111","agent":"0.3.0"}
host={"hostname":"agent-01","updates":{"mode":"pull","interval":"1h"},"agent":{"settings":{"mode":"collector"}}}
last_update={"at":"2026-10-15T12:00:00Z","ok":false,"fleet":"1111111","message":"could not fetch agent-hosts"}
pending=1
`)
	if got := m.State("1111111"); got != Current {
		t.Fatalf("at its own revision: %v, want current", got)
	}
	if got := m.State("2222222"); got != Outdated {
		t.Fatalf("behind: %v, want outdated", got)
	}
	if m.Host == nil || m.Host.Updates.Mode != "pull" || m.Host.Agent.Settings["mode"] != "collector" {
		t.Fatalf("host = %+v", m.Host)
	}
	if m.LastUpdate == nil || m.LastUpdate.OK || !m.Pending {
		t.Fatalf("last update %+v, pending %t", m.LastUpdate, m.Pending)
	}
	if m.System() != "aarch64-linux" {
		t.Fatalf("system = %s", m.System())
	}
}

func TestNixOSWithoutTheMarkerIsUnmanaged(t *testing.T) {
	if got := parse("os=nixos\n").State("abc"); got != Unmanaged {
		t.Fatalf("state = %v, want unmanaged", got)
	}
}
