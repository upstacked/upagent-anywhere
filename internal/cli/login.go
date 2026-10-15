package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/upstacked/upagent-anywhere/internal/authkey"
	"github.com/upstacked/upagent-anywhere/internal/errs"
	"github.com/upstacked/upagent-anywhere/internal/inspect"
	"github.com/upstacked/upagent-anywhere/internal/ui"
	"github.com/upstacked/upagent-anywhere/internal/upstacked"
)

// defaultUpstacked is where the Upstacked API is, unless --upstacked-url or
// UPSTACKED_API_URL (which the ups CLI reads too) says otherwise.
const defaultUpstacked = "https://api.upstacked-staging.sokl.dev"

func (a *App) upstackedURL() string {
	if a.UpstackedURL != "" {
		return a.UpstackedURL
	}
	if v := os.Getenv("UPSTACKED_API_URL"); v != "" {
		return v
	}
	return defaultUpstacked
}

func (a *App) client() (*upstacked.Client, *upstacked.Store, error) {
	store, err := upstacked.DefaultStore()
	if err != nil {
		return nil, nil, err
	}
	return upstacked.New(a.upstackedURL(), Version), store, nil
}

func newLoginCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Log in to Upstacked, approving the login in the portal",
		Long: `login shows a code and the portal's address. Approve it there, where you
are logged in already, and this tool can mint the auth keys new agents join
with, for the infrastructures you may update.

The session is kept in ~/.config/upagent-anywhere/credentials.json, readable
only by you, until it expires or you run logout.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := app.Ctx()
			defer cancel()
			_, err := app.login(ctx)
			return err
		},
	}
}

func newLogoutCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the Upstacked session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, store, err := app.client()
			if err != nil {
				return err
			}
			if err := store.Put(c.Base, nil); err != nil {
				return err
			}
			fmt.Fprintf(app.Stderr, "Logged out of %s.\n", c.Base)
			return nil
		},
	}
}

// login runs the device flow: no password passes through this tool, and the
// portal's MFA applies.
func (a *App) login(ctx context.Context) (*upstacked.Session, error) {
	c, store, err := a.client()
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	code, err := c.StartLogin(ctx, host)
	if err != nil {
		return nil, errs.General("starting a login with %s: %s", c.Base, err)
	}
	th := a.Theme()
	fmt.Fprintf(a.Stderr, "\nTo log in, open %s\nand enter the code %s\n\n",
		th.Bold.Apply(code.VerificationURI), th.Bold.Apply(code.UserCode))
	if a.Interactive() {
		openBrowser(code.VerificationURIComplete)
	}
	sp := ui.NewSpinner(a.Stderr, "Waiting for you to approve it")
	sp.Start()
	tok, err := c.WaitForLogin(ctx, code)
	sp.Stop()
	switch {
	case errors.Is(err, upstacked.ErrDenied):
		return nil, errs.Auth("the login was denied in the portal")
	case errors.Is(err, upstacked.ErrExpired):
		return nil, errs.Auth("the code expired before it was approved").WithHint("run it again for a new code")
	case err != nil:
		return nil, errs.General("logging in: %s", err)
	}
	s, err := upstacked.Begin(c, store, tok)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(a.Stderr, "%s Logged in to %s.\n", a.Sym().OK, c.Base)
	return s, nil
}

// session is the saved one, or a new login when there is a terminal to
// approve it from.
func (a *App) session(ctx context.Context) (*upstacked.Session, error) {
	c, store, err := a.client()
	if err != nil {
		return nil, err
	}
	s, err := upstacked.Resume(c, store)
	if err == nil {
		return s, nil
	}
	if !errors.Is(err, upstacked.ErrLoggedOut) {
		return nil, err
	}
	if !a.Interactive() {
		return nil, errs.Auth("not logged in to Upstacked at %s", c.Base).
			WithHint("run upagent-anywhere login first, or pipe a key in with --auth-key-stdin")
	}
	return a.login(ctx)
}

// authKey gets the key the new agent enrols with: from stdin when asked to,
// otherwise minted by Upstacked for the infrastructure the user picks, which
// they must be allowed to update.
func (a *App) authKey(ctx context.Context, h *inspect.HostConfig) (string, error) {
	if a.AuthKeyStdin {
		key, err := a.ReadSecret("Auth key for the agent (upsk-...)")
		if err != nil {
			return "", err
		}
		if key, err = authkey.Check(key); err != nil {
			return "", errs.Usage("%s", err)
		}
		return key, nil
	}
	if !a.Interactive() && a.Infrastructure == 0 {
		return "", errs.Usage("the agent needs an auth key, and stdin is not a terminal to choose its infrastructure").
			WithHint("pass --infrastructure <id> (after upagent-anywhere login), or pipe a key in with --auth-key-stdin")
	}

	s, err := a.session(ctx)
	if err != nil {
		return "", err
	}
	infra, err := a.chooseInfrastructure(ctx, s)
	if err != nil {
		return "", err
	}
	name := ""
	if h != nil {
		name = h.Hostname
	}
	minted, err := s.MintAuthKey(ctx, infra, name)
	if errors.Is(err, upstacked.ErrLoggedOut) {
		return "", errs.Auth("the Upstacked session ended").WithHint("run upagent-anywhere login")
	}
	if err != nil {
		return "", errs.General("minting an auth key: %s", err).
			WithHint("or pipe one in with --auth-key-stdin")
	}
	fmt.Fprintf(a.Stderr, "%s Minted auth key %d for infrastructure %d; it is spent when the agent joins.\n",
		a.Sym().OK, minted.ID, infra)
	return minted.Key, nil
}

func (a *App) chooseInfrastructure(ctx context.Context, s *upstacked.Session) (int64, error) {
	if a.Infrastructure != 0 {
		return a.Infrastructure, nil
	}
	list, err := s.Infrastructures(ctx)
	if err != nil {
		return 0, errs.General("listing infrastructures: %s", err)
	}
	if len(list) == 0 {
		return 0, errs.Auth("your Upstacked user can see no infrastructures to add an agent to")
	}
	var choices []ui.Choice
	for _, inf := range list {
		desc := inf.Customer
		if inf.Code != "" {
			desc = inf.Code + "  " + desc
		}
		choices = append(choices, ui.Choice{Label: inf.Name, Desc: desc, Value: strconv.FormatInt(inf.ID, 10)})
	}
	v, err := a.Choose("Infrastructure the agent joins", "--infrastructure", choices)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

// openBrowser is a convenience; the address is printed either way.
func openBrowser(url string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	if _, err := exec.LookPath(name); err == nil {
		exec.Command(name, url).Start()
	}
}
