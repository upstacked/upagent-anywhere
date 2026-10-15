// Package authkey checks the upsk- key a new agent enrols with. Each machine
// gets a key of its own, spent by its first enrolment, so an infrastructure
// can have as many agents as it has machines.
package authkey

import (
	"errors"
	"strings"
)

// Check trims a key and refuses anything that is not one.
func Check(key string) (string, error) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "upsk-") {
		return "", errors.New("an auth key starts with upsk-")
	}
	return key, nil
}
