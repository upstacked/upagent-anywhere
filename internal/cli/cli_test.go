package cli

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/upstacked/upagent-anywhere/internal/errs"
	"github.com/upstacked/upagent-anywhere/internal/inspect"
)

// piped is an App whose stdin is a pipe holding input, as under CI or an
// agent, where nothing can answer a prompt.
func piped(t *testing.T, input string) *App {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString(input)
	w.Close()
	t.Cleanup(func() { r.Close() })
	return &App{Stdin: r, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
}

func TestAnAuthKeyIsNeverAskedForWithoutATerminal(t *testing.T) {
	a := piped(t, "upsk-from-a-pipe\n")
	_, err := a.authKey(context.Background(), nil)
	if errs.CodeOf(err) != errs.CodeUsage || errs.HintOf(err) == "" {
		t.Fatalf("err = %v (code %d), want a usage error naming --auth-key-stdin", err, errs.CodeOf(err))
	}
}

func TestAnAuthKeyIsReadFromStdinWhenAsked(t *testing.T) {
	a := piped(t, "  upsk-from-a-pipe\n")
	a.AuthKeyStdin = true
	key, err := a.authKey(context.Background(), nil)
	if err != nil || key != "upsk-from-a-pipe" {
		t.Fatalf("key %q, err %v", key, err)
	}
}

func TestSomethingThatIsNotAKeyIsRefused(t *testing.T) {
	a := piped(t, "hunter2\n")
	a.AuthKeyStdin = true
	if _, err := a.authKey(context.Background(), nil); errs.CodeOf(err) != errs.CodeUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
}

func TestSettingsComeFromFlagsWithoutATerminal(t *testing.T) {
	a := piped(t, "")
	a.Hostname, a.Modes, a.Updates, a.Interval = "agent-01", "monitor,collector", "pull", "30m"
	a.SSHKeys = []string{"ssh-ed25519 AAAA test"}
	h, err := a.askHost(&inspect.Machine{Hostname: "ubuntu", Arch: "aarch64"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.Hostname != "agent-01" || h.System != "aarch64-linux" || h.Agent.Settings["mode"] != "monitor,collector" ||
		h.Updates.Interval != "30m" || h.Agent.Settings["api.url"] != defaultAPI || len(h.SSHKeys) != 1 {
		t.Fatalf("host = %+v", h)
	}
}

func TestAnUnknownModeIsRefused(t *testing.T) {
	a := piped(t, "")
	a.Modes = "collector,firewall"
	if _, err := a.askHost(&inspect.Machine{Hostname: "x"}, nil); errs.CodeOf(err) != errs.CodeUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
}

func TestSettingsKeepWhatTheMachineHas(t *testing.T) {
	a := piped(t, "")
	have := &inspect.HostConfig{Hostname: "agent-01", SSHKeys: []string{"ssh-ed25519 KEPT"}}
	have.Updates.Mode, have.Updates.Interval = "push", ""
	have.Agent.Settings = map[string]string{"mode": "local", "api.url": "https://agent-api.example", "probes": "true"}
	h, err := a.askHost(&inspect.Machine{Hostname: "agent-01"}, have)
	if err != nil {
		t.Fatal(err)
	}
	if h.Updates.Mode != "push" || h.Agent.Settings["probes"] != "true" || h.Agent.Settings["api.url"] != "https://agent-api.example" || h.SSHKeys[0] != "ssh-ed25519 KEPT" {
		t.Fatalf("host = %+v", h)
	}
	if have.Agent.Settings["mode"] != "local" {
		t.Fatal("askHost changed the machine's settings in place")
	}
}

func TestChoosingWithoutATerminalNamesTheFlag(t *testing.T) {
	a := piped(t, "")
	_, err := a.askDisk(&inspect.Machine{Disks: []inspect.Disk{{Path: "/dev/sda"}, {Path: "/dev/sdb"}}})
	if errs.CodeOf(err) != errs.CodeUsage || errs.HintOf(err) != "pass --disk" {
		t.Fatalf("err = %v, hint %q", err, errs.HintOf(err))
	}
}
