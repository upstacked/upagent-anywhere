package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/upstacked/upagent-anywhere/internal/authkey"
	"github.com/upstacked/upagent-anywhere/internal/errs"
	"github.com/upstacked/upagent-anywhere/internal/fleet"
	"github.com/upstacked/upagent-anywhere/internal/inspect"
	"github.com/upstacked/upagent-anywhere/internal/install"
	"github.com/upstacked/upagent-anywhere/internal/remote"
	"github.com/upstacked/upagent-anywhere/internal/ui"
)

const defaultAPI = "https://agent-api.upstacked-staging.sokl.dev"

var modes = []string{"collector", "monitor", "local"}

// look connects and inspects, and finds the revision to judge against.
func (a *App) look(ctx context.Context) (*remote.Target, *inspect.Machine, string, error) {
	t, err := a.Connect(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	m, err := inspect.Inspect(ctx, t)
	if err != nil {
		t.Close()
		return nil, nil, "", errs.General("inspecting %s: %s", a.Target, err)
	}
	latest := a.Rev
	if latest == "" {
		if latest, err = fleet.Latest(ctx, a.Fleet); err != nil {
			t.Close()
			return nil, nil, "", errs.General("%s", err).WithHint("pass --rev to judge against a known commit")
		}
	}
	return t, m, latest, nil
}

func (a *App) status() error {
	ctx, cancel := a.Ctx()
	defer cancel()
	t, m, latest, err := a.look(ctx)
	if err != nil {
		return err
	}
	defer t.Close()
	if a.AsJSON {
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"address": t.Host, "user": t.User, "state": m.State(latest).String(),
			"latest": latest, "machine": m,
		})
	}
	a.report(t, m, latest)
	return nil
}

func (a *App) update() error {
	ctx, cancel := a.Ctx()
	defer cancel()
	t, m, latest, err := a.look(ctx)
	if err != nil {
		return err
	}
	defer t.Close()
	if !m.Managed {
		return errs.Conflict("%s is %s, so there is nothing to update", a.Target, m.State(latest)).
			WithHint("run upagent-anywhere --target-host %s to install it", a.Target)
	}
	if m.FleetRevision == latest {
		fmt.Fprintf(a.Stderr, "%s is already at agent-hosts %s.\n", a.Target, short(latest))
		return nil
	}
	return a.runUpdate(ctx, t, latest)
}

func (a *App) runUpdate(ctx context.Context, t *remote.Target, rev string) error {
	if err := install.Update(ctx, t, rev, a.Stdout, a.Stderr); err != nil {
		return errs.General("updating %s: %s", a.Target, err).
			WithHint("the machine keeps running what it ran; /var/lib/upagent/last-update.json says why")
	}
	fmt.Fprintf(a.Stderr, "%s %s is at agent-hosts %s.\n", a.Sym().OK, a.Target, short(rev))
	return nil
}

// provision is the interactive whole: look, then offer what fits.
func (a *App) provision() error {
	ctx, cancel := a.Ctx()
	defer cancel()
	t, m, latest, err := a.look(ctx)
	if err != nil {
		return err
	}
	defer t.Close()
	a.report(t, m, latest)

	switch m.State(latest) {
	case inspect.Foreign, inspect.Unmanaged:
		return a.install(ctx, t, m, latest)
	}
	return a.manage(ctx, t, m, latest)
}

func (a *App) report(t *remote.Target, m *inspect.Machine, latest string) {
	th, w := a.Theme(), a.Stderr
	fmt.Fprintf(w, "%s %s\n", th.Bold.Apply(t.Host), th.Dim.Apply(fmt.Sprintf("(%s, logged in as %s)", m.Hostname, t.User)))
	line := func(k, v string) { fmt.Fprintf(w, "  %-8s %s\n", k, v) }
	line("system", m.OSName+", "+m.Arch)
	line("state", m.State(latest).String())
	if m.Managed {
		line("built", fmt.Sprintf("agent-hosts %s, latest %s", short(m.FleetRevision), short(latest)))
		if h := m.Host; h != nil {
			mode := h.Updates.Mode
			if mode == "pull" {
				mode += " every " + orDefault(h.Updates.Interval, "1h")
			}
			line("updates", mode)
		}
		if u := m.LastUpdate; u != nil {
			state := "ok"
			if !u.OK {
				state = th.Red.Apply("failed: " + u.Message)
			}
			line("last", u.At+", "+state)
		}
		if m.Pending {
			line("pending", "a newer system is built but not running; it starts on reboot")
		}
	}
	if ag := m.Agent; ag.Installed {
		state := th.Red.Apply("stopped")
		if ag.Active {
			state = "running"
		}
		enrolled := th.Yellow.Apply("not enrolled")
		if ag.Enrolled {
			enrolled = fmt.Sprintf("enrolled as agent %d", ag.AgentID)
		}
		line("agent", ag.Version+", "+state+", "+enrolled)
	} else {
		line("agent", "not installed")
	}
	fmt.Fprintln(w)
}

func (a *App) install(ctx context.Context, t *remote.Target, m *inspect.Machine, rev string) error {
	if err := a.Confirm(fmt.Sprintf("Install NixOS with upstacked-agent on %s, replacing %s?", t.Host, m.OSName)); err != nil {
		return err
	}
	if t.User != "root" {
		if _, err := t.Run(ctx, "sudo -n true"); err != nil {
			return errs.Auth("%s cannot sudo without a password, which installing needs", t.User)
		}
	}
	h, err := a.askHost(m, nil)
	if err != nil {
		return err
	}
	if h.Disk, err = a.askDisk(m); err != nil {
		return err
	}

	key, err := a.authKey(ctx, h)
	if err != nil {
		return err
	}

	// --yes does not reach this far: erasing a disk is confirmed by naming
	// what is erased, never by a flag that also answers everything else.
	switch {
	case a.Erase != "":
		if a.Erase != h.Disk {
			return errs.Usage("--erase names %s, but the install would erase %s", a.Erase, h.Disk)
		}
	case a.Interactive():
		typed, err := a.Ask(fmt.Sprintf("Type %s to erase %s on %s and install", h.Hostname, h.Disk, t.Host), "", "")
		if err != nil {
			return err
		}
		if typed != h.Hostname {
			return errs.General("the name did not match; nothing changed")
		}
	default:
		return errs.Usage("installing erases %s, and stdin is not a terminal to confirm it", h.Disk).
			WithHint("pass --erase %s", h.Disk)
	}

	root, err := fleet.Stage(a.Fleet, h, key)
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if err := install.Fresh(ctx, t, root, a.Fleet, rev, a.Stdout, a.Stderr); err != nil {
		return errs.General("%s", err).
			WithHint("the machine may no longer run its old system; run this again to finish the install")
	}
	fmt.Fprintf(a.Stderr, "\nInstalled. Waiting for %s to let upstacked log in...\n", t.Host)
	return a.waitAndReport(ctx, rev)
}

func (a *App) manage(ctx context.Context, t *remote.Target, m *inspect.Machine, latest string) error {
	var choices []ui.Choice
	if m.FleetRevision != latest {
		choices = append(choices, ui.Choice{Label: "Update", Desc: "to agent-hosts " + short(latest), Value: "update"})
	}
	choices = append(choices, ui.Choice{Label: "Change settings", Desc: "what the agent does, updates, SSH keys", Value: "settings"})
	if !m.Agent.Enrolled {
		choices = append(choices, ui.Choice{Label: "New auth key", Desc: "the agent has not enrolled", Value: "key"})
	}
	choices = append(choices, ui.Choice{Label: "Nothing", Value: "nothing"})
	if !a.Interactive() {
		return nil
	}
	choice, err := a.Choose("What now?", "a subcommand: update", choices)
	if err != nil {
		return err
	}
	switch choice {
	case "update":
		return a.runUpdate(ctx, t, latest)

	case "settings":
		h, err := a.askHost(m, m.Host)
		if err != nil {
			return err
		}
		body, err := fleet.HostJSON(h)
		if err != nil {
			return err
		}
		if err := t.WriteFile(ctx, fleet.FlakeDir+"/host.json", 0o644, body); err != nil {
			return errs.General("writing host.json: %s", err)
		}
		// Only the settings change; the machine stays on its agent-hosts.
		return a.runUpdate(ctx, t, m.FleetRevision)

	case "key":
		key, err := a.authKey(ctx, m.Host)
		if err != nil {
			return err
		}
		if err := t.WriteFile(ctx, fleet.AuthKeyPath, 0o600, []byte(key+"\n")); err != nil {
			return errs.General("writing the auth key: %s", err)
		}
		if _, err := t.Run(ctx, t.Sudo()+" systemctl restart upstacked-agent"); err != nil {
			return errs.General("restarting the agent: %s", err)
		}
		fmt.Fprintf(a.Stderr, "%s The agent restarted with the new key.\n", a.Sym().OK)
	}
	return nil
}

// authKey gets the key the new agent enrols with.
func (a *App) authKey(ctx context.Context, h *inspect.HostConfig) (string, error) {
	if !a.AuthKeyStdin && !a.Interactive() {
		return "", errs.Usage("the agent needs an auth key, and stdin is not a terminal to ask for one").
			WithHint("pipe one in with --auth-key-stdin")
	}
	var src authkey.Source = authkey.Prompt{Ask: func(q string) (string, error) {
		if a.Interactive() {
			fmt.Fprintln(a.Stderr, a.Theme().Dim.Apply("Mint one with: upstacked-agent-api authkey create -infrastructure <id>"))
		}
		return a.ReadSecret(q)
	}}
	name := ""
	if h != nil {
		name = h.Hostname
	}
	key, err := src.AuthKey(ctx, authkey.Request{Hostname: name})
	if err != nil {
		return "", errs.Usage("%s", err)
	}
	return key, nil
}

// askHost asks for host.json's settings, starting from what the machine has.
func (a *App) askHost(m *inspect.Machine, have *inspect.HostConfig) (*inspect.HostConfig, error) {
	h := &inspect.HostConfig{Hostname: m.Hostname, System: m.System()}
	h.Updates.Mode, h.Updates.Interval = "pull", "1h"
	h.Agent.Settings = map[string]string{"mode": "collector", "api.url": defaultAPI}
	if have != nil {
		*h = *have
		h.Agent.Settings = map[string]string{}
		for k, v := range have.Agent.Settings {
			h.Agent.Settings[k] = v
		}
	}

	var err error
	if h.Hostname, err = a.Ask("Hostname", a.Hostname, h.Hostname); err != nil {
		return nil, err
	}
	if h.Hostname == "" || strings.ContainsAny(h.Hostname, " ./") {
		return nil, errs.Usage("%q is not a hostname: one label of letters, digits and dashes", h.Hostname)
	}

	chosen := splitModes(orDefault(a.Modes, h.Agent.Settings["mode"]))
	if a.Modes == "" && a.Interactive() {
		var opts []ui.MultiChoice
		desc := map[string]string{
			"collector": "receive syslog and flows from the network",
			"monitor":   "poll devices with SNMP, ICMP and HTTP",
			"local":     "ship this machine's own logs",
		}
		for _, md := range modes {
			opts = append(opts, ui.MultiChoice{Label: md, Desc: desc[md], Value: md, Selected: slices.Contains(chosen, md)})
		}
		if chosen, err = a.ChooseMany("What the agent does", "--mode", opts); err != nil {
			return nil, err
		}
	}
	for _, md := range chosen {
		if !slices.Contains(modes, md) {
			return nil, errs.Usage("%q is not a mode; use %s", md, strings.Join(modes, ", "))
		}
	}
	if len(chosen) == 0 {
		return nil, errs.Usage("the agent needs at least one mode").WithHint("pass --mode collector")
	}
	h.Agent.Settings["mode"] = strings.Join(chosen, ",")

	if h.Agent.Settings["api.url"], err = a.Ask("agent-api URL", a.APIURL, orDefault(h.Agent.Settings["api.url"], defaultAPI)); err != nil {
		return nil, err
	}

	h.Updates.Mode = orDefault(a.Updates, h.Updates.Mode)
	if a.Updates == "" && a.Interactive() {
		h.Updates.Mode, err = a.Choose("Updates", "--updates", []ui.Choice{
			{Label: "pull", Desc: "the machine follows agent-hosts on a timer", Value: "pull"},
			{Label: "push", Desc: "only when upagent-anywhere is run against it", Value: "push"},
		})
		if err != nil {
			return nil, err
		}
	}
	if h.Updates.Mode != "pull" && h.Updates.Mode != "push" {
		return nil, errs.Usage("--updates is pull or push, not %q", h.Updates.Mode)
	}
	if h.Updates.Mode == "pull" {
		if h.Updates.Interval, err = a.Ask("Pull interval (30m, 1h, 1d)", a.Interval, orDefault(h.Updates.Interval, "1h")); err != nil {
			return nil, err
		}
	}

	switch {
	case len(a.SSHKeys) > 0:
		h.SSHKeys = a.SSHKeys
	case have == nil:
		// Without them the operator could not log in once the machine is
		// installed, unless their key is one of the fleet's.
		h.SSHKeys = agentKeys(a.Target)
	}
	if h.SSHKeys == nil {
		h.SSHKeys = []string{}
	}
	return h, nil
}

func (a *App) askDisk(m *inspect.Machine) (string, error) {
	if len(m.Disks) == 0 {
		return "", errs.General("%s lists no disks to install on", a.Target)
	}
	if a.Disk != "" {
		for _, d := range m.Disks {
			if d.Path == a.Disk {
				return a.Disk, nil
			}
		}
		return "", errs.Usage("%s has no disk %s", a.Target, a.Disk)
	}
	var choices []ui.Choice
	for _, d := range m.Disks {
		choices = append(choices, ui.Choice{Label: d.Path, Desc: fmt.Sprintf("%s  %s", humanBytes(d.Bytes), d.Model), Value: d.Path})
	}
	return a.Choose("Disk to erase and install on", "--disk", choices)
}

// waitAndReport logs in as upstacked once the installed system has booted.
func (a *App) waitAndReport(ctx context.Context, latest string) error {
	deadline := time.Now().Add(10 * time.Minute)
	for {
		t, err := remote.New(a.Target, "upstacked", a.Identity)
		if err != nil {
			return err
		}
		if _, err := t.Run(ctx, "true"); err == nil {
			defer t.Close()
			m, err := inspect.Inspect(ctx, t)
			if err != nil {
				return err
			}
			a.report(t, m, latest)
			return nil
		}
		t.Close()
		if time.Now().After(deadline) {
			return errs.General("%s has not let upstacked log in after 10 minutes", a.Target).
				WithHint("check its console; the install itself finished")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

// agentKeys are the public keys in the agent ssh uses for host. That may not
// be $SSH_AUTH_SOCK: an IdentityAgent in ssh_config, such as 1Password's,
// wins over it, and is what logged in a moment ago.
func agentKeys(host string) []string {
	cmd := exec.Command("ssh-add", "-L")
	if out, err := exec.Command("ssh", "-G", host).Output(); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if sock, ok := strings.CutPrefix(l, "identityagent "); ok && sock != "none" && sock != "SSH_AUTH_SOCK" {
				if home, err := os.UserHomeDir(); err == nil {
					sock = strings.Replace(sock, "~", home, 1)
				}
				cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+sock)
			}
		}
	}
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var keys []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(l, "ssh-") || strings.HasPrefix(l, "ecdsa-") || strings.HasPrefix(l, "sk-") {
			keys = append(keys, l)
		}
	}
	return keys
}

func splitModes(s string) []string {
	var out []string
	for _, m := range strings.Split(s, ",") {
		if m = strings.TrimSpace(m); m != "" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

func short(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return orDefault(rev, "unknown")
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func humanBytes(n int64) string {
	return fmt.Sprintf("%.0f GiB", float64(n)/(1<<30))
}
