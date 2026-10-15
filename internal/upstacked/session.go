package upstacked

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// ErrLoggedOut is a session that is gone: never made, or refused on refresh.
var ErrLoggedOut = errors.New("not logged in to Upstacked")

// Store keeps sessions by API URL, readable only by the operator, so that a
// second run does not ask them to log in again.
type Store struct {
	Path string
	mu   sync.Mutex
}

// DefaultStore is ~/.config/upagent-anywhere/credentials.json, or under
// $XDG_CONFIG_HOME.
func DefaultStore() (*Store, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".config")
	}
	return &Store{Path: filepath.Join(dir, "upagent-anywhere", "credentials.json")}, nil
}

func (s *Store) read() (map[string]Tokens, error) {
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Tokens{}, nil
	}
	if err != nil {
		return nil, err
	}
	all := map[string]Tokens{}
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("%s is not readable: %w", s.Path, err)
	}
	return all, nil
}

func (s *Store) Get(base string) (Tokens, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.read()
	if err != nil {
		return Tokens{}, false, err
	}
	t, ok := all[base]
	return t, ok, nil
}

// Put saves base's session, or forgets it when t is nil.
func (s *Store) Put(base string, t *Tokens) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.read()
	if err != nil {
		return err
	}
	if t == nil {
		delete(all, base)
	} else {
		all[base] = *t
	}
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

// Session is a logged-in Client. It refreshes once when the access token is
// refused, and saves what it is given.
type Session struct {
	c     *Client
	store *Store
	tok   Tokens
}

// Resume picks up a saved session, or returns ErrLoggedOut.
func Resume(c *Client, store *Store) (*Session, error) {
	tok, ok, err := store.Get(c.Base)
	if err != nil {
		return nil, err
	}
	if !ok || tok.Refresh == "" {
		return nil, ErrLoggedOut
	}
	return &Session{c: c, store: store, tok: tok}, nil
}

// Begin starts a session from a completed login and saves it.
func Begin(c *Client, store *Store, tok *Tokens) (*Session, error) {
	if err := store.Put(c.Base, tok); err != nil {
		return nil, err
	}
	return &Session{c: c, store: store, tok: *tok}, nil
}

func (s *Session) call(ctx context.Context, method, path string, in, out any) error {
	try := func() error {
		if in == nil {
			return s.c.do(ctx, method, path, s.tok.Access, nil, out)
		}
		return s.c.post(ctx, path, s.tok.Access, in, out)
	}
	err := try()
	if !Unauthorized(err) {
		return err
	}
	tok, rerr := s.c.Refresh(ctx, s.tok.Refresh)
	if rerr != nil {
		// A refused refresh ends the session; keeping it would only fail the
		// same way next time.
		s.store.Put(s.c.Base, nil)
		return ErrLoggedOut
	}
	s.tok = *tok
	if err := s.store.Put(s.c.Base, tok); err != nil {
		return err
	}
	return try()
}

// Infrastructure is one the user may see.
type Infrastructure struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Code     string `json:"infra_code"`
	Customer string `json:"customer"`
}

// Infrastructures lists what the user may see, every page of it.
func (s *Session) Infrastructures(ctx context.Context) ([]Infrastructure, error) {
	var all []Infrastructure
	for offset := 0; ; {
		var page struct {
			Count   int              `json:"count"`
			Next    *string          `json:"next"`
			Results []Infrastructure `json:"results"`
		}
		q := url.Values{"limit": {"200"}, "offset": {strconv.Itoa(offset)}}
		if err := s.call(ctx, http.MethodGet, "/api/infrastructure/?"+q.Encode(), nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		offset += len(page.Results)
		if page.Next == nil || len(page.Results) == 0 {
			return all, nil
		}
	}
}

// AuthKey is a freshly minted key, shown once.
type AuthKey struct {
	Key            string `json:"key"`
	ID             int64  `json:"id"`
	Infrastructure int64  `json:"infrastructure"`
	ExpiresAt      string `json:"expires_at"`
}

// MintAuthKey asks Upstacked for the key one new agent joins with.
func (s *Session) MintAuthKey(ctx context.Context, infrastructure int64, hostname string) (*AuthKey, error) {
	var out AuthKey
	err := s.call(ctx, http.MethodPost, "/api/agent/auth-keys", map[string]any{
		"infrastructure": infrastructure, "hostname": hostname,
	}, &out)
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.Status == http.StatusForbidden {
			return nil, fmt.Errorf("you may not add agents to infrastructure %d", infrastructure)
		}
		return nil, err
	}
	return &out, nil
}
