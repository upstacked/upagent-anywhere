// Package authkey gets the upsk- key a new agent enrols with. Each machine
// gets a key of its own, spent by its first enrolment, so an infrastructure
// can have as many agents as it has machines.
package authkey

import (
	"context"
	"errors"
	"strings"
)

// Request says what the key is for.
type Request struct {
	// Infrastructure is the Upstacked infrastructure the agent joins.
	Infrastructure int64
	// Hostname names the machine, for the audit trail.
	Hostname string
}

// Source mints or obtains one key.
type Source interface {
	AuthKey(ctx context.Context, req Request) (string, error)
}

// Prompt asks the operator for a key minted elsewhere, with
// `upstacked-agent-api authkey create`, until the Upstacked API can mint one
// after a device login.
type Prompt struct {
	Ask func(question string) (string, error)
}

func (p Prompt) AuthKey(ctx context.Context, req Request) (string, error) {
	key, err := p.Ask("Auth key for the agent (upsk-...)")
	if err != nil {
		return "", err
	}
	return Check(key)
}

// Check trims a key and refuses anything that is not one.
func Check(key string) (string, error) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "upsk-") {
		return "", errors.New("an auth key starts with upsk-")
	}
	return key, nil
}
