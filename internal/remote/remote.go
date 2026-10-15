// Package remote runs commands on a target over the operator's own ssh, so
// that their config, agent and known hosts apply as they would by hand.
package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Target is a machine reached as User@Host.
type Target struct {
	Host string
	User string
	// Identity is an ssh private key, empty for whatever ssh picks.
	Identity string
	// control is the directory holding the shared connection, so that the
	// many commands one run makes authenticate once.
	control string
}

func New(host, user, identity string) (*Target, error) {
	// In /tmp rather than $TMPDIR: a socket path is limited to 104 bytes, and
	// macOS's $TMPDIR alone takes half of that.
	dir, err := os.MkdirTemp("/tmp", "upagent-ssh-")
	if err != nil {
		return nil, err
	}
	return &Target{Host: host, User: user, Identity: identity, control: dir}, nil
}

// Close ends the shared connection.
func (t *Target) Close() {
	exec.Command("ssh", append(t.options(), "-O", "exit", t.Address())...).Run()
	os.RemoveAll(t.control)
}

func (t *Target) Address() string {
	if t.User == "" {
		return t.Host
	}
	return t.User + "@" + t.Host
}

// WithUser is the same machine as another user, sharing nothing.
func (t *Target) WithUser(user string) (*Target, error) {
	return New(t.Host, user, t.Identity)
}

// SSHOptions are the options every ssh to this target takes, for tools such
// as nixos-anywhere that run ssh themselves.
func (t *Target) SSHOptions() []string {
	var out []string
	if t.Identity != "" {
		out = append(out, "-i", t.Identity)
	}
	return out
}

func (t *Target) options() []string {
	opts := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(t.control, "%C"),
		"-o", "ControlPersist=120",
	}
	return append(opts, t.SSHOptions()...)
}

// Error is a command that ran and failed, with what it printed.
type Error struct {
	Command string
	Code    int
	Stderr  string
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Command, msg)
}

// Unreachable is ssh failing to connect or authenticate, which it reports as
// exit status 255.
func Unreachable(err error) bool {
	e, ok := err.(*Error)
	return ok && e.Code == 255
}

// Run runs a shell command and returns its output.
func (t *Target) Run(ctx context.Context, command string) (string, error) {
	return t.RunInput(ctx, command, nil)
}

// RunInput runs a shell command with stdin.
func (t *Target) RunInput(ctx context.Context, command string, stdin io.Reader) (string, error) {
	args := append(t.options(), t.Address(), "--", command)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &out, &errOut, stdin
	if err := cmd.Run(); err != nil {
		code := -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return out.String(), &Error{Command: firstWord(command), Code: code, Stderr: errOut.String()}
	}
	return out.String(), nil
}

// Stream runs a command with its output going straight to the operator, for
// the long ones such as a rebuild.
func (t *Target) Stream(ctx context.Context, command string, stdout, stderr io.Writer) error {
	args := append(t.options(), "-t", t.Address(), "--", command)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = stdout, stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		code := -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return &Error{Command: firstWord(command), Code: code}
	}
	return nil
}

// WriteFile writes content to path as root, with mode, through sudo when the
// user is not root.
func (t *Target) WriteFile(ctx context.Context, path string, mode os.FileMode, content []byte) error {
	cmd := fmt.Sprintf("%s install -D -m %04o /dev/stdin %s", t.Sudo(), mode, quote(path))
	_, err := t.RunInput(ctx, cmd, bytes.NewReader(content))
	return err
}

// Sudo is the prefix that makes a command run as root.
func (t *Target) Sudo() string {
	if t.User == "root" {
		return ""
	}
	return "sudo -n"
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	for _, p := range []string{"sudo -n ", "sh -c "} {
		s = strings.TrimPrefix(s, p)
	}
	if i := strings.IndexAny(s, " \n;|"); i > 0 {
		return s[:i]
	}
	return s
}
