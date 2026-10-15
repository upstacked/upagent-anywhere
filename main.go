// Command upagent-anywhere puts a machine under agent-hosts and keeps it there.
//
//	upagent-anywhere --target-host 10.30.100.15
//
// It looks at the machine first and offers what fits: installing NixOS on one
// that runs anything else, updating one that is behind, changing the settings
// of one that is current. Running it again is always safe; nothing that
// erases a disk happens without the operator typing the machine's name.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"

	"sokkel.dev/upstacked/upagent-anywhere/internal/authkey"
	"sokkel.dev/upstacked/upagent-anywhere/internal/fleet"
	"sokkel.dev/upstacked/upagent-anywhere/internal/inspect"
	"sokkel.dev/upstacked/upagent-anywhere/internal/install"
	"sokkel.dev/upstacked/upagent-anywhere/internal/remote"
)

var version = "dev"

const defaultAPI = "https://agent-api.upstacked-staging.sokl.dev"

func main() {
	if err := run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			fmt.Fprintln(os.Stderr, "Nothing changed.")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "upagent-anywhere:", err)
		os.Exit(1)
	}
}

type options struct {
	host, user, identity string
	fleetURL, rev        string
	status               bool
}

func run() error {
	var o options
	flag.StringVar(&o.host, "target-host", "", "address of the machine")
	flag.StringVar(&o.user, "user", "", "user to log in as (default: upstacked, then root, then asks)")
	flag.StringVar(&o.identity, "i", "", "ssh private key to log in with")
	flag.StringVar(&o.fleetURL, "fleet", fleet.DefaultURL, "agent-hosts repository")
	flag.StringVar(&o.rev, "rev", "", "agent-hosts commit to install or update to (default: latest main)")
	flag.BoolVar(&o.status, "status", false, "only say where the machine stands")
	showVersion := flag.Bool("version", false, "print the version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if o.host == "" {
		return errors.New("--target-host is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	t, err := connect(ctx, o)
	if err != nil {
		return err
	}
	defer t.Close()

	m, err := inspect.Inspect(ctx, t)
	if err != nil {
		return err
	}
	latest := o.rev
	if latest == "" {
		if latest, err = fleet.Latest(ctx, o.fleetURL); err != nil {
			return err
		}
	}
	report(t, m, latest)
	if o.status {
		return nil
	}

	switch m.State(latest) {
	case inspect.Foreign, inspect.Unmanaged:
		return offerInstall(ctx, t, m, o, latest)
	default:
		return offerManaged(ctx, t, m, o, latest)
	}
}

// connect logs in as the first user that works.
func connect(ctx context.Context, o options) (*remote.Target, error) {
	users := []string{o.user}
	if o.user == "" {
		users = []string{"upstacked", "root"}
	}
	for _, u := range users {
		t, err := remote.New(o.host, u, o.identity)
		if err != nil {
			return nil, err
		}
		if _, err := t.Run(ctx, "true"); err == nil {
			return t, nil
		} else if !remote.Unreachable(err) {
			t.Close()
			return nil, err
		}
		t.Close()
	}
	if o.user != "" {
		return nil, fmt.Errorf("cannot log in to %s as %s", o.host, o.user)
	}
	var user string
	err := huh.NewInput().
		Title(fmt.Sprintf("Neither upstacked nor root can log in to %s. Which user can?", o.host)).
		Description("It needs passwordless sudo. A fresh cloud image usually has one: ubuntu, debian, admin.").
		Value(&user).Run()
	if err != nil {
		return nil, err
	}
	o.user = strings.TrimSpace(user)
	if o.user == "" {
		return nil, errors.New("no user to log in as")
	}
	return connect(ctx, o)
}

func report(t *remote.Target, m *inspect.Machine, latest string) {
	fmt.Printf("%s (%s, logged in as %s)\n", t.Host, m.Hostname, t.User)
	fmt.Printf("  system   %s, %s\n", m.OSName, m.Arch)
	fmt.Printf("  state    %s\n", m.State(latest))
	if m.Managed {
		fmt.Printf("  built    agent-hosts %s, latest %s\n", short(m.FleetRevision), short(latest))
		if m.Host != nil {
			mode := m.Host.Updates.Mode
			if mode == "pull" {
				mode += " every " + orDefault(m.Host.Updates.Interval, "1h")
			}
			fmt.Printf("  updates  %s\n", mode)
		}
		if u := m.LastUpdate; u != nil {
			state := "ok"
			if !u.OK {
				state = "failed: " + u.Message
			}
			fmt.Printf("  last     %s, %s\n", u.At, state)
		}
		if m.Pending {
			fmt.Println("  pending  a newer system is built but not running; it starts on reboot")
		}
	}
	if a := m.Agent; a.Installed {
		state := "stopped"
		if a.Active {
			state = "running"
		}
		enrolled := "not enrolled"
		if a.Enrolled {
			enrolled = fmt.Sprintf("enrolled as agent %d", a.AgentID)
		}
		fmt.Printf("  agent    %s, %s, %s\n", a.Version, state, enrolled)
	} else {
		fmt.Println("  agent    not installed")
	}
	fmt.Println()
}

func offerInstall(ctx context.Context, t *remote.Target, m *inspect.Machine, o options, rev string) error {
	var proceed bool
	err := huh.NewConfirm().
		Title(fmt.Sprintf("Install NixOS with upstacked-agent on %s?", t.Host)).
		Description("This replaces " + m.OSName + " and erases the disk you choose next.").
		Value(&proceed).Run()
	if err != nil || !proceed {
		return err
	}
	if t.User != "root" {
		if _, err := t.Run(ctx, "sudo -n true"); err != nil {
			return fmt.Errorf("%s cannot sudo without a password, which installing needs", t.User)
		}
	}

	h, err := askHost(m, nil)
	if err != nil {
		return err
	}
	if len(m.Disks) == 0 {
		return errors.New("the machine lists no disks to install on")
	}
	var disks []huh.Option[string]
	for _, d := range m.Disks {
		disks = append(disks, huh.NewOption(fmt.Sprintf("%s  %s  %s", d.Path, humanBytes(d.Bytes), d.Model), d.Path))
	}
	h.Disk = m.Disks[0].Path
	if err := huh.NewSelect[string]().Title("Disk to erase and install on").Options(disks...).Value(&h.Disk).Run(); err != nil {
		return err
	}

	src := authkey.Prompt{Ask: func(q string) (string, error) {
		var key string
		err := huh.NewInput().Title(q).
			Description("Mint one with: upstacked-agent-api authkey create -infrastructure <id>").
			EchoMode(huh.EchoModePassword).Value(&key).Run()
		return key, err
	}}
	key, err := src.AuthKey(ctx, authkey.Request{Hostname: h.Hostname})
	if err != nil {
		return err
	}

	var typed string
	err = huh.NewInput().
		Title(fmt.Sprintf("Type %s to erase %s on %s and install", h.Hostname, h.Disk, t.Host)).
		Value(&typed).Run()
	if err != nil {
		return err
	}
	if strings.TrimSpace(typed) != h.Hostname {
		return errors.New("the name did not match; nothing changed")
	}

	root, err := fleet.Stage(o.fleetURL, h, key)
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	if err := install.Fresh(ctx, t, root, o.fleetURL, rev, os.Stdout, os.Stderr); err != nil {
		return err
	}
	fmt.Printf("\nInstalled. Checking %s as it comes back...\n", t.Host)
	return waitAndReport(ctx, o, rev)
}

func offerManaged(ctx context.Context, t *remote.Target, m *inspect.Machine, o options, latest string) error {
	type action string
	const (
		update   action = "update"
		settings action = "settings"
		key      action = "key"
		nothing  action = "nothing"
	)
	var opts []huh.Option[action]
	if m.FleetRevision != latest {
		opts = append(opts, huh.NewOption("Update to agent-hosts "+short(latest), update))
	}
	opts = append(opts, huh.NewOption("Change this machine's settings", settings))
	if !m.Agent.Enrolled {
		opts = append(opts, huh.NewOption("Give the agent a new auth key", key))
	}
	opts = append(opts, huh.NewOption("Nothing", nothing))

	var choice action
	if err := huh.NewSelect[action]().Title("What now?").Options(opts...).Value(&choice).Run(); err != nil {
		return err
	}
	switch choice {
	case update:
		return install.Update(ctx, t, o.rev, os.Stdout, os.Stderr)

	case settings:
		h, err := askHost(m, m.Host)
		if err != nil {
			return err
		}
		body, err := fleet.HostJSON(h)
		if err != nil {
			return err
		}
		if err := t.WriteFile(ctx, fleet.FlakeDir+"/host.json", 0o644, body); err != nil {
			return err
		}
		// Settings alone change; the machine stays on the agent-hosts it has.
		return install.Update(ctx, t, m.FleetRevision, os.Stdout, os.Stderr)

	case key:
		var k string
		err := huh.NewInput().Title("Auth key for the agent (upsk-...)").EchoMode(huh.EchoModePassword).Value(&k).Run()
		if err != nil {
			return err
		}
		if k, err = authkey.Check(k); err != nil {
			return err
		}
		if err := t.WriteFile(ctx, fleet.AuthKeyPath, 0o600, []byte(k+"\n")); err != nil {
			return err
		}
		_, err = t.Run(ctx, t.Sudo()+" systemctl restart upstacked-agent")
		return err
	}
	return nil
}

// askHost asks for host.json's settings, starting from what the machine has.
func askHost(m *inspect.Machine, have *inspect.HostConfig) (*inspect.HostConfig, error) {
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
	if h.SSHKeys == nil {
		h.SSHKeys = agentKeys()
	}

	modes := strings.Split(orDefault(h.Agent.Settings["mode"], "collector"), ",")
	api := orDefault(h.Agent.Settings["api.url"], defaultAPI)
	keys := strings.Join(h.SSHKeys, "\n")
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("Hostname").Value(&h.Hostname).Validate(func(s string) error {
				if strings.TrimSpace(s) == "" || strings.ContainsAny(s, " ./") {
					return errors.New("a hostname is one label: letters, digits and dashes")
				}
				return nil
			}),
			huh.NewMultiSelect[string]().Title("What the agent does").Options(
				huh.NewOption("collector: receive syslog and flows from the network", "collector"),
				huh.NewOption("monitor: poll devices with SNMP, ICMP and HTTP", "monitor"),
				huh.NewOption("local: ship this machine's own logs", "local"),
			).Value(&modes).Validate(func(v []string) error {
				if len(v) == 0 {
					return errors.New("choose at least one")
				}
				return nil
			}),
			huh.NewInput().Title("agent-api URL").Value(&api),
		),
		huh.NewGroup(
			huh.NewSelect[string]().Title("Updates").Options(
				huh.NewOption("pull: the machine follows agent-hosts on a timer", "pull"),
				huh.NewOption("push: only when upagent-anywhere is run against it", "push"),
			).Value(&h.Updates.Mode),
			huh.NewInput().Title("Pull interval").Description("A systemd time span: 30m, 1h, 1d. Ignored for push.").Value(&h.Updates.Interval),
			huh.NewText().Title("SSH keys that may log in as upstacked").
				Description("One per line, besides the fleet's own keys in agent-hosts. Filled from your ssh agent.").
				Value(&keys),
		),
	)
	if err := form.Run(); err != nil {
		return nil, err
	}
	h.Hostname = strings.TrimSpace(h.Hostname)
	h.Agent.Settings["mode"] = strings.Join(modes, ",")
	h.Agent.Settings["api.url"] = strings.TrimSpace(api)
	h.SSHKeys = nil
	for _, k := range strings.Split(keys, "\n") {
		if k = strings.TrimSpace(k); k != "" {
			h.SSHKeys = append(h.SSHKeys, k)
		}
	}
	return h, nil
}

// agentKeys are the public keys in the operator's ssh agent, so that they can
// still log in once the machine is installed.
func agentKeys() []string {
	out, err := exec.Command("ssh-add", "-L").Output()
	if err != nil {
		return nil
	}
	var keys []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(l, "ssh-") || strings.HasPrefix(l, "ecdsa-") {
			keys = append(keys, l)
		}
	}
	return keys
}

// waitAndReport logs in as upstacked once the installed system has booted.
func waitAndReport(ctx context.Context, o options, latest string) error {
	o.user = "upstacked"
	deadline := time.Now().Add(10 * time.Minute)
	for {
		t, err := remote.New(o.host, o.user, o.identity)
		if err != nil {
			return err
		}
		if _, err := t.Run(ctx, "true"); err == nil {
			defer t.Close()
			m, err := inspect.Inspect(ctx, t)
			if err != nil {
				return err
			}
			report(t, m, latest)
			return nil
		}
		t.Close()
		if time.Now().After(deadline) {
			return fmt.Errorf("%s has not let upstacked log in after 10 minutes", o.host)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
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
	const gib = 1 << 30
	return fmt.Sprintf("%.0f GiB", float64(n)/gib)
}
