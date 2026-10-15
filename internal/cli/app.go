// Package cli wires the commands to SSH, agent-hosts and the terminal.
package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/upstacked/upagent-anywhere/internal/errs"
	"github.com/upstacked/upagent-anywhere/internal/fleet"
	"github.com/upstacked/upagent-anywhere/internal/remote"
	"github.com/upstacked/upagent-anywhere/internal/ui"
)

// Version is set at build time.
var Version = "dev"

// App is the per-invocation environment shared by all commands.
type App struct {
	// Stdin stays a file because reading a secret without echo needs a fd.
	Stdin  *os.File
	Stdout io.Writer
	Stderr io.Writer

	// Reaching the machine.
	Target   string
	User     string
	Identity string

	// Which agent-hosts, and which commit of it.
	Fleet string
	Rev   string

	AssumeYes bool
	AsJSON    bool

	// The machine's settings, each asked for when not given.
	Hostname     string
	Modes        string
	APIURL       string
	Updates      string
	Interval     string
	SSHKeys      []string
	Disk         string
	Erase        string
	AuthKeyStdin bool

	theme *ui.Theme
	sym   ui.Symbols
}

func newApp() *App {
	return &App{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Fleet: fleet.DefaultRef}
}

// Theme returns the colour theme for stderr-facing UI.
func (a *App) Theme() *ui.Theme {
	if a.theme == nil {
		a.theme = ui.NewTheme(a.Stderr)
	}
	return a.theme
}

// Sym returns status glyphs appropriate to the terminal.
func (a *App) Sym() ui.Symbols {
	if a.sym.OK == "" {
		a.sym = ui.NewSymbols(ui.UnicodeOK())
	}
	return a.sym
}

// Ctx is cancelled by Ctrl-C. There is no overall timeout: an install takes
// as long as building a system on the machine takes.
func (a *App) Ctx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// Interactive reports whether prompting is possible. Non-TTY stdin is never
// prompted; the command fails naming the flag it needed instead.
func (a *App) Interactive() bool {
	return a.Stdin != nil && ui.IsTerminalFd(int(a.Stdin.Fd()))
}

// Confirm asks before acting.
func (a *App) Confirm(prompt string) error {
	if a.AssumeYes {
		return nil
	}
	if !a.Interactive() {
		return errs.Usage("%s requires confirmation, but stdin is not a terminal", prompt).
			WithHint("pass --yes to proceed non-interactively")
	}
	fmt.Fprintf(a.Stderr, "%s [y/N]: ", prompt)
	line, err := bufio.NewReader(a.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return errs.General("cannot read confirmation").Wrapping(err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return errs.General("aborted; nothing changed")
}

// ReadSecret takes a secret from stdin, never from argv, where it would show
// in the process list and shell history.
func (a *App) ReadSecret(prompt string) (string, error) {
	if !a.Interactive() {
		line, err := bufio.NewReader(a.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", errs.Usage("no value on stdin for %s", prompt).
				WithHint("pipe it in, e.g.: printf '%%s' \"$KEY\" | upagent-anywhere ... --auth-key-stdin")
		}
		return strings.TrimSpace(line), nil
	}
	fmt.Fprintf(a.Stderr, "%s: ", prompt)
	secret, err := ui.ReadPassword(int(a.Stdin.Fd()))
	fmt.Fprintln(a.Stderr)
	if err != nil {
		return "", errs.General("cannot read %s", prompt).Wrapping(err)
	}
	return strings.TrimSpace(secret), nil
}

// Ask reads a visible line, offering def. A value given as a flag is used as
// it is; without a terminal the default is taken.
func (a *App) Ask(label, given, def string) (string, error) {
	if given != "" {
		return given, nil
	}
	if !a.Interactive() {
		return def, nil
	}
	if def != "" {
		fmt.Fprintf(a.Stderr, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(a.Stderr, "%s: ", label)
	}
	line, err := bufio.NewReader(a.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", errs.General("cannot read input").Wrapping(err)
	}
	if line = strings.TrimSpace(line); line == "" {
		return def, nil
	}
	return line, nil
}

// Choose picks one of choices, or fails naming the flag that would have.
func (a *App) Choose(title, flag string, choices []ui.Choice) (string, error) {
	if len(choices) == 1 {
		return choices[0].Value, nil
	}
	if !a.Interactive() {
		return "", errs.Usage("%s needs an answer, but stdin is not a terminal", strings.ToLower(title)).
			WithHint("pass %s", flag)
	}
	v, err := ui.Select(a.Stdin, a.Stderr, title, choices)
	if err != nil {
		return "", errs.General("%s", err)
	}
	return v, nil
}

// ChooseMany is Choose for several.
func (a *App) ChooseMany(title, flag string, choices []ui.MultiChoice) ([]string, error) {
	if !a.Interactive() {
		return nil, errs.Usage("%s needs an answer, but stdin is not a terminal", strings.ToLower(title)).
			WithHint("pass %s", flag)
	}
	v, err := ui.MultiSelect(a.Stdin, a.Stderr, title, choices)
	if err != nil {
		return nil, errs.General("%s", err)
	}
	return v, nil
}

// Connect logs in to the target as the first user that works: the one given,
// or upstacked (a managed machine), then root, then whoever the operator
// names.
func (a *App) Connect(ctx context.Context) (*remote.Target, error) {
	if a.Target == "" {
		return nil, errs.Usage("no machine given").WithHint("pass --target-host <address>")
	}
	users := []string{a.User}
	if a.User == "" {
		users = []string{"upstacked", "root"}
	}
	for _, u := range users {
		t, err := remote.New(a.Target, u, a.Identity)
		if err != nil {
			return nil, err
		}
		_, err = t.Run(ctx, "true")
		if err == nil {
			return t, nil
		}
		t.Close()
		if !remote.Unreachable(err) {
			return nil, errs.General("logging in to %s as %s: %s", a.Target, u, err)
		}
	}
	if a.User != "" || !a.Interactive() {
		return nil, errs.Auth("cannot log in to %s as %s", a.Target, strings.Join(users, " or ")).
			WithHint("check the address, or pass --user with one that can log in with your ssh key")
	}
	user, err := a.Ask(fmt.Sprintf("Neither upstacked nor root can log in to %s. User that can (it needs passwordless sudo)", a.Target), "", "")
	if err != nil {
		return nil, err
	}
	if user == "" {
		return nil, errs.Usage("no user to log in as").WithHint("pass --user")
	}
	a.User = user
	return a.Connect(ctx)
}
