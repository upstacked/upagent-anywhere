package upstacked

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fake is Upstacked as far as this package can tell: the device flow, a
// rotating refresh, the infrastructure list and the key endpoint.
type fake struct {
	mu       sync.Mutex
	polls    int
	approve  bool // decided once polled this many times
	access   string
	refresh  string
	refreshs int
	minted   []map[string]any
}

func (f *fake) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /api/auth/device/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["client_id"] != ClientID {
			reply(w, 400, map[string]string{"error": "invalid_client"})
			return
		}
		reply(w, 200, DeviceCode{DeviceCode: "dc", UserCode: "BCDF-GHJK", VerificationURI: "https://portal/device",
			VerificationURIComplete: "https://portal/device?user_code=BCDF-GHJK", ExpiresIn: 60, Interval: 0})
	})
	mux.HandleFunc("POST /api/auth/device/token/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.polls++
		switch {
		case f.polls == 1:
			reply(w, 400, map[string]string{"error": "authorization_pending"})
		case f.polls == 2:
			reply(w, 400, map[string]string{"error": "slow_down"})
		case !f.approve:
			reply(w, 400, map[string]string{"error": "access_denied"})
		default:
			f.access, f.refresh = "a1", "r1"
			reply(w, 200, Tokens{Access: "a1", Refresh: "r1"})
		}
	})
	mux.HandleFunc("POST /api/auth/device/refresh/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["refresh"] != f.refresh {
			reply(w, 401, map[string]string{"detail": "Token reuse detected"})
			return
		}
		f.refreshs++
		f.access, f.refresh = "a2", "r2"
		reply(w, 200, Tokens{Access: "a2", Refresh: "r2"})
	})
	authed := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			ok := r.Header.Get("Authorization") == "Bearer "+f.access
			f.mu.Unlock()
			if !ok {
				reply(w, 401, map[string]string{"detail": "expired"})
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("GET /api/infrastructure/", authed(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") == "0" {
			next := "more"
			reply(w, 200, map[string]any{"count": 2, "next": next, "results": []Infrastructure{{ID: 8, Name: "Staging"}}})
			return
		}
		reply(w, 200, map[string]any{"count": 2, "next": nil, "results": []Infrastructure{{ID: 9, Name: "Lab"}}})
	}))
	mux.HandleFunc("POST /api/agent/auth-keys", authed(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["infrastructure"].(float64) == 1 {
			reply(w, 403, map[string]string{"detail": "no"})
			return
		}
		f.mu.Lock()
		f.minted = append(f.minted, body)
		f.mu.Unlock()
		reply(w, 201, AuthKey{Key: "upsk-new", ID: 3, Infrastructure: 8})
	}))
	return mux
}

func setup(t *testing.T, approve bool) (*fake, *Client, *Store) {
	t.Helper()
	f := &fake{approve: approve}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	return f, New(srv.URL, "test"), &Store{Path: filepath.Join(t.TempDir(), "credentials.json")}
}

func TestALoginWaitsOutPendingAndSlowDown(t *testing.T) {
	_, c, _ := setup(t, true)
	code, err := c.StartLogin(context.Background(), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	code.Interval = 0 // keep the test fast; slow_down still adds its 5s
	tok, err := c.WaitForLogin(context.Background(), code)
	if err != nil || tok.Access != "a1" {
		t.Fatalf("tokens %+v, err %v", tok, err)
	}
}

func TestADeniedLoginSaysSo(t *testing.T) {
	_, c, _ := setup(t, false)
	code, _ := c.StartLogin(context.Background(), "laptop")
	code.Interval = 0
	if _, err := c.WaitForLogin(context.Background(), code); !errors.Is(err, ErrDenied) {
		t.Fatalf("err = %v, want denied", err)
	}
}

func TestASessionRefreshesWhenItsAccessTokenIsRefused(t *testing.T) {
	f, c, store := setup(t, true)
	f.access, f.refresh = "a1", "r1"
	s, err := Begin(c, store, &Tokens{Access: "stale", Refresh: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.Infrastructures(context.Background())
	if err != nil || len(list) != 2 || list[1].ID != 9 {
		t.Fatalf("infrastructures %+v, err %v", list, err)
	}
	if f.refreshs != 1 {
		t.Fatalf("refreshed %d times, want once", f.refreshs)
	}
	saved, _, _ := store.Get(c.Base)
	if saved.Refresh != "r2" {
		t.Fatalf("saved %+v, want the rotated refresh token", saved)
	}
}

func TestARefusedRefreshEndsTheSession(t *testing.T) {
	f, c, store := setup(t, true)
	f.access, f.refresh = "a1", "r-current"
	s, _ := Begin(c, store, &Tokens{Access: "stale", Refresh: "r-old"})
	if _, err := s.Infrastructures(context.Background()); !errors.Is(err, ErrLoggedOut) {
		t.Fatalf("err = %v, want logged out", err)
	}
	if _, ok, _ := store.Get(c.Base); ok {
		t.Fatal("the dead session was kept")
	}
	if _, err := Resume(c, store); !errors.Is(err, ErrLoggedOut) {
		t.Fatalf("resume: %v", err)
	}
}

func TestAKeyIsMintedForAnInfrastructure(t *testing.T) {
	f, c, store := setup(t, true)
	f.access = "a1"
	s, _ := Begin(c, store, &Tokens{Access: "a1", Refresh: "r1"})
	key, err := s.MintAuthKey(context.Background(), 8, "agent-07")
	if err != nil || key.Key != "upsk-new" {
		t.Fatalf("key %+v, err %v", key, err)
	}
	if f.minted[0]["hostname"] != "agent-07" {
		t.Fatalf("sent %+v", f.minted[0])
	}
	_, err = s.MintAuthKey(context.Background(), 1, "agent-07")
	if err == nil || !strings.Contains(err.Error(), "may not add agents") {
		t.Fatalf("err = %v, want a permission refusal", err)
	}
}

func TestTheStoreIsReadableOnlyByItsOwner(t *testing.T) {
	_, c, store := setup(t, true)
	if err := store.Put(c.Base, &Tokens{Access: "a", Refresh: "r"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o, want 600", fi.Mode().Perm())
	}
}
