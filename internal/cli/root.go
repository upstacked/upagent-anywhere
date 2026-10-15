package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/upstacked/upagent-anywhere/internal/errs"
)

// NewRoot builds the command tree. The root command itself is the
// interactive one: it looks at the machine and offers what fits.
func NewRoot(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:   "upagent-anywhere --target-host <address>",
		Short: "Install and keep up machines that run upstacked-agent",
		Long: `upagent-anywhere puts a machine under agent-hosts and keeps it there.

It looks at the machine over SSH first and offers what fits: installing
NixOS with upstacked-agent on one that runs anything else, updating one
that is behind agent-hosts, changing the settings of one that is current.

Running it again is always safe. Installing erases a disk, so it happens
only once the machine's name is typed, or --erase names the disk.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.provision()
		},
	}

	f := root.PersistentFlags()
	f.StringVar(&app.Target, "target-host", "", "address of the machine")
	f.StringVar(&app.User, "user", "", "user to log in as (default: upstacked, then root)")
	f.StringVarP(&app.Identity, "identity", "i", "", "ssh private key to log in with")
	f.StringVar(&app.Fleet, "fleet", app.Fleet, "agent-hosts, as a flake reference")
	f.StringVar(&app.Rev, "rev", "", "agent-hosts commit to install or update to (default: latest main)")
	f.BoolVar(&app.AssumeYes, "yes", false, "do not prompt for confirmation (never skips --erase)")
	f.StringVar(&app.UpstackedURL, "upstacked-url", "", "Upstacked API, which mints auth keys (default: $UPSTACKED_API_URL, else "+defaultUpstacked+")")

	l := root.Flags()
	l.StringVar(&app.Hostname, "hostname", "", "the machine's hostname (default: what it has)")
	l.StringVar(&app.Modes, "mode", "", "what the agent does: collector, monitor, local, or several separated by commas")
	l.StringVar(&app.APIURL, "api-url", "", "agent-api URL (default: "+defaultAPI+")")
	l.StringVar(&app.Updates, "updates", "", "pull (follow agent-hosts on a timer) or push (only when this tool is run)")
	l.StringVar(&app.Interval, "interval", "", "how often a pull machine updates, as a systemd time span (default: 1h)")
	l.StringArrayVar(&app.SSHKeys, "ssh-key", nil, "public key, or a .pub file, that may log in as upstacked; repeatable")
	l.BoolVar(&app.ManagementKey, "management-key", false, "also let Upstacked's operators log in with the Upstacked management key")
	l.StringVar(&app.Disk, "disk", "", "disk to install on")
	l.StringVar(&app.Erase, "erase", "", "confirms installing by naming the disk it erases; needed without a terminal")
	l.BoolVar(&app.AuthKeyStdin, "auth-key-stdin", false, "read the agent's upsk- auth key from stdin instead of minting one")
	l.Int64Var(&app.Infrastructure, "infrastructure", 0, "Upstacked infrastructure the agent joins (default: asks)")

	root.AddCommand(
		newLoginCmd(app),
		newLogoutCmd(app),
		newStatusCmd(app),
		newUpdateCmd(app),
		newCompletionCmd(),
		newVersionCmd(app),
	)
	return root
}

// Execute runs the CLI and maps errors onto the exit-code contract.
func Execute(version string) int {
	Version = version
	app := newApp()
	root := NewRoot(app)
	root.Version = version

	err := root.Execute()
	if err == nil {
		return errs.CodeOK
	}
	theme := app.Theme()
	fmt.Fprintf(app.Stderr, "%s %s\n", theme.Red.Apply("error:"), err.Error())
	if hint := errs.HintOf(err); hint != "" {
		fmt.Fprintf(app.Stderr, "  %s %s\n", theme.Dim.Apply("hint:"), hint)
	}
	return errs.CodeOf(err)
}

func newStatusCmd(app *App) *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "Say where the machine stands, changing nothing",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.status()
		},
	}
	c.Flags().BoolVar(&app.AsJSON, "json", false, "emit JSON")
	return c
}

func newUpdateCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Move a managed machine to the latest agent-hosts, or to --rev",
		Long: `update runs upagent-update on the machine, the same command its pull timer
runs, without asking anything. It refuses a machine agent-hosts did not
build; run upagent-anywhere without a subcommand to install one.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.update()
		},
	}
}

func newVersionCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(app.Stdout, "upagent-anywhere %s\n", Version)
			return nil
		},
	}
}

func newCompletionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "completion [bash|zsh|fish]",
		Short: "Generate a shell completion script",
		Long: `Generate a shell completion script.

  zsh:   upagent-anywhere completion zsh  > "${fpath[1]}/_upagent-anywhere"
  bash:  upagent-anywhere completion bash > /usr/local/etc/bash_completion.d/upagent-anywhere
  fish:  upagent-anywhere completion fish > ~/.config/fish/completions/upagent-anywhere.fish`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletionV2(os.Stdout, true)
			case "zsh":
				return cmd.Root().GenZshCompletion(os.Stdout)
			case "fish":
				return cmd.Root().GenFishCompletion(os.Stdout, true)
			}
			return errs.Usage("unknown shell %q", args[0])
		},
	}
}
