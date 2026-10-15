// Package sshkeys finds the public keys an operator could log in with, so a
// machine is installed trusting exactly the keys someone chose for it.
package sshkeys

import (
	"bufio"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// management is the public half of the Upstacked management key. It is
// published with the tool so that anyone installing a machine can let
// Upstacked's operators in; the private half stays in Upstacked's vault.
//
//go:embed upstacked-management.pub
var management string

// Key is one public key, and where it was found.
type Key struct {
	Line   string // as authorized_keys takes it: type, base64, comment
	Source string // ~/.ssh/id_ed25519.pub, the ssh agent, 1Password, ...
}

var types = []string{
	"ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
	"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com",
}

// Parse reads one authorized_keys line, refusing anything that is not a key.
func Parse(line string) (string, error) {
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) < 2 || !slices.Contains(types, f[0]) {
		return "", fmt.Errorf("not an SSH public key: %.40q", line)
	}
	if _, err := base64.StdEncoding.DecodeString(f[1]); err != nil {
		return "", fmt.Errorf("not an SSH public key: its key data is not base64")
	}
	return strings.Join(f, " "), nil
}

// Fingerprint is the key's SHA256 fingerprint, as ssh-keygen -l shows it.
func Fingerprint(line string) string {
	f := strings.Fields(line)
	if len(f) < 2 {
		return ""
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// Comment is the key's comment, or empty.
func Comment(line string) string {
	f := strings.Fields(line)
	if len(f) < 3 {
		return ""
	}
	return strings.Join(f[2:], " ")
}

// Same reports whether two lines hold the same key, whatever their comments.
func Same(a, b string) bool {
	fa, fb := strings.Fields(a), strings.Fields(b)
	return len(fa) >= 2 && len(fb) >= 2 && fa[0] == fb[0] && fa[1] == fb[1]
}

// Discover finds the keys in the default places: the public keys in ~/.ssh,
// and those in the agent ssh uses for host. Each key appears once.
func Discover(host string) []Key {
	var out []Key
	add := func(line, source string) {
		k, err := Parse(line)
		if err != nil {
			return
		}
		for _, have := range out {
			if Same(have.Line, k) {
				return
			}
		}
		out = append(out, Key{Line: k, Source: source})
	}

	if home, err := os.UserHomeDir(); err == nil {
		files, _ := filepath.Glob(filepath.Join(home, ".ssh", "*.pub"))
		slices.Sort(files)
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(strings.NewReader(string(raw)))
			for sc.Scan() {
				add(sc.Text(), "~/.ssh/"+filepath.Base(f))
			}
		}
	}
	for _, line := range agentKeys(host) {
		add(line, "ssh agent")
	}
	return out
}

// agentKeys lists the agent ssh uses for host. That may not be
// $SSH_AUTH_SOCK: an IdentityAgent in ssh_config, such as 1Password's, wins.
func agentKeys(host string) []string {
	cmd := exec.Command("ssh-add", "-L")
	if out, err := exec.Command("ssh", "-G", host).Output(); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if sock, ok := strings.CutPrefix(l, "identityagent "); ok && sock != "none" && sock != "SSH_AUTH_SOCK" {
				if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(sock, "~") {
					sock = home + sock[1:]
				}
				cmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+sock)
			}
		}
	}
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// Management is the Upstacked management key.
func Management() Key {
	k, err := Parse(management)
	if err != nil {
		panic("the embedded management key does not parse: " + err.Error())
	}
	return Key{Line: k, Source: "Upstacked management key"}
}

// FromFlag takes a key, or the path of a .pub file holding one.
func FromFlag(v string) (string, error) {
	if k, err := Parse(v); err == nil {
		return k, nil
	}
	path := v
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, path[2:])
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("--ssh-key %s is neither a public key nor a file holding one", v)
	}
	return Parse(string(raw))
}
