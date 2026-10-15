package sshkeys

import (
	"os"
	"path/filepath"
	"testing"
)

const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDDXfT23Ne7Rl/rdiqrLf4vWfQqgUCo26+NcL41PVSag operator"

func TestFingerprintsMatchSSHKeygen(t *testing.T) {
	// From ssh-keygen -lf on the same key.
	if got := Fingerprint(key); got != "SHA256:S/T9UzZhFXmoh3iX8eTovHPMe1uTsNYbhWEHC4l+2CE" {
		t.Fatalf("fingerprint = %s", got)
	}
	if got := Fingerprint(Management().Line); got != "SHA256:2wrh0NhQt7Y/VQvyylP/OdmXiqYbRhaFltALIOYtyMo" {
		t.Fatalf("the management key is %s, not the one published", got)
	}
}

func TestTheSameKeyUnderAnotherNameIsTheSame(t *testing.T) {
	if !Same(key, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDDXfT23Ne7Rl/rdiqrLf4vWfQqgUCo26+NcL41PVSag laptop") {
		t.Fatal("a renamed key is not the same key")
	}
	if Same(key, Management().Line) {
		t.Fatal("two keys are the same")
	}
}

func TestParseRefusesWhatIsNotAKey(t *testing.T) {
	for _, bad := range []string{"", "hunter2", "ssh-ed25519", "ssh-ed25519 !!!notbase64", "-----BEGIN OPENSSH PRIVATE KEY-----"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("parsed %q", bad)
		}
	}
}

func TestAFlagMayNameAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id_ed25519.pub")
	os.WriteFile(path, []byte(key+"\n"), 0o644)
	got, err := FromFlag(path)
	if err != nil || got != key {
		t.Fatalf("got %q, %v", got, err)
	}
}
