// Package upstacked talks to the Upstacked API: logging in with a device code,
// listing the infrastructures the user may act on, and minting the auth key a
// new agent joins with.
package upstacked

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ClientID names this tool to Upstacked, which shows it on the approval page.
const ClientID = "upagent-anywhere"

// DeviceCode is what the user takes to a browser.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// Tokens are a logged-in session.
type Tokens struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
}

// Errors a login can end with that mean something to the user.
var (
	ErrDenied  = errors.New("the login was denied in the browser")
	ErrExpired = errors.New("the code expired before it was approved")
)

type Client struct {
	Base string
	HTTP *http.Client
	// Agent is the User-Agent, which carries the version.
	Agent string
}

func New(base, version string) *Client {
	return &Client{
		Base:  strings.TrimRight(base, "/"),
		HTTP:  &http.Client{Timeout: 30 * time.Second},
		Agent: "upagent-anywhere/" + version,
	}
}

// StartLogin asks for a device code. hostname is the operator's machine,
// which the approval page shows so they can tell their own login from
// someone else's.
func (c *Client) StartLogin(ctx context.Context, hostname string) (*DeviceCode, error) {
	var out DeviceCode
	body := map[string]string{"client_id": ClientID, "hostname": hostname}
	if err := c.post(ctx, "/api/auth/device/", "", body, &out); err != nil {
		return nil, err
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	return &out, nil
}

// WaitForLogin polls until the code is approved, denied or expired, at the
// pace the server asks for (RFC 8628 §3.5).
func (c *Client) WaitForLogin(ctx context.Context, code *DeviceCode) (*Tokens, error) {
	interval := time.Duration(code.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		var tok Tokens
		err := c.post(ctx, "/api/auth/device/token/", "", map[string]string{
			"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
			"device_code": code.DeviceCode,
			"client_id":   ClientID,
		}, &tok)
		var oe *OAuthError
		switch {
		case err == nil:
			return &tok, nil
		case errors.As(err, &oe) && oe.Code == "authorization_pending":
		case errors.As(err, &oe) && oe.Code == "slow_down":
			interval += 5 * time.Second
		case errors.As(err, &oe) && oe.Code == "access_denied":
			return nil, ErrDenied
		case errors.As(err, &oe) && oe.Code == "expired_token":
			return nil, ErrExpired
		default:
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, ErrExpired
		}
	}
}

// Refresh exchanges a refresh token for a new pair.
func (c *Client) Refresh(ctx context.Context, refresh string) (*Tokens, error) {
	var tok Tokens
	if err := c.post(ctx, "/api/auth/device/refresh/", "", map[string]string{"refresh": refresh}, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

// OAuthError is a token endpoint's refusal, by its RFC 6749 code.
type OAuthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return e.Code + ": " + e.Description
	}
	return e.Code
}

// HTTPError is any other refusal.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Upstacked answered %d: %s", e.Status, e.Body)
}

// Unauthorized reports whether err means the session is no longer good.
func Unauthorized(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == http.StatusUnauthorized
}

func (c *Client) post(ctx context.Context, path, access string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, path, access, bytes.NewReader(b), out)
}

func (c *Client) do(ctx context.Context, method, path, access string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.Agent)
	if access != "" {
		req.Header.Set("Authorization", "Bearer "+access)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("reaching Upstacked at %s: %w", c.Base, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("Upstacked's answer to %s is not what was expected: %w", path, err)
		}
		return nil
	}
	if resp.StatusCode == http.StatusBadRequest {
		var oe OAuthError
		if json.Unmarshal(raw, &oe) == nil && oe.Code != "" {
			return &oe
		}
	}
	msg := strings.TrimSpace(string(raw))
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	return &HTTPError{Status: resp.StatusCode, Body: msg}
}
