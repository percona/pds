// Package cli is pds-mcp's command line: the commands, what each reads from the environment, and
// where each writes. The commands are registered in one table, so the data server that joins this
// binary adds its own beside them.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/percona/pds/apps/mcp/internal/bridge"
	"github.com/percona/pds/apps/mcp/internal/config"
	"github.com/percona/pds/apps/mcp/internal/messages"
	"github.com/percona/pds/apps/mcp/internal/oauth"
	"github.com/percona/pds/apps/mcp/internal/tokenstore"
)

// Env is everything a command touches outside its own arguments, so a test can run one with
// nothing real behind it.
type Env struct {
	Stdin  io.ReadCloser
	Stdout io.WriteCloser
	Stderr io.Writer
	Getenv func(string) string
	// Version is the pds-mcp version, as the release stamped it.
	Version string
	// Interactive is true when standard input is a terminal, which is not where the bridge runs.
	Interactive bool
	// WorkDir is the directory the program was started in, where the data server may write a .env file.
	WorkDir string
	// OpenBrowser opens the system browser at an address.
	OpenBrowser func(address string) error
}

// control plane request and sign in timeouts.
const requestTimeout = 30 * time.Second

type command struct {
	// signsIn commands need a control plane: they refuse the removed settings and read PDS_URL.
	signsIn bool
	run     func(ctx context.Context, env Env, cfg config.Config, args []string) int
}

// commands is the table of subcommands. Running pds-mcp with none is the bridge.
var commands = map[string]command{
	"login":  {signsIn: true, run: runLogin},
	"logout": {signsIn: true, run: runLogout},
	"data":   {signsIn: true, run: runData},
}

// Run runs the command the arguments name and returns the process's exit code. Standard output
// carries protocol messages when the bridge runs, so every diagnostic goes to standard error.
func Run(ctx context.Context, env Env, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version":
			_, _ = fmt.Fprintf(env.Stdout, "pds-mcp %s\n", env.Version)

			return 0
		case "help", "--help", "-h":
			_, _ = io.WriteString(env.Stdout, messages.Usage)

			return 0
		}
	}

	var cmd command
	switch {
	case len(args) == 0:
		if env.Interactive {
			_, _ = io.WriteString(env.Stderr, messages.Usage)

			return 2
		}
		cmd = command{signsIn: true, run: runBridge}
	default:
		var ok bool
		if cmd, ok = commands[args[0]]; !ok {
			_, _ = fmt.Fprintf(env.Stderr, "Unknown command %q.\n%s", args[0], messages.Usage)

			return 2
		}
		args = args[1:]
	}

	cfg, err := config.Load(env.Getenv)
	if err != nil {
		_, _ = fmt.Fprintln(env.Stderr, err)

		return 2
	}

	return cmd.run(ctx, env, cfg, args)
}

// store opens the login file the environment points at.
func store(env Env) (*tokenstore.Store, error) {
	path, err := config.LoginFile(env.Getenv)
	if err != nil {
		return nil, err
	}

	return tokenstore.New(path), nil
}

func userAgent(env Env) string { return "pds-mcp/" + env.Version }

// runBridge is pds-mcp with no command: stdio to the control plane's /mcp.
func runBridge(ctx context.Context, env Env, cfg config.Config, _ []string) int {
	logins, err := store(env)
	if err != nil {
		_, _ = fmt.Fprintln(env.Stderr, err)

		return 1
	}
	if _, err := logins.Load(cfg.Origin); err != nil {
		// The bridge still starts: a person can sign in while the agent is open, and the next
		// request finds the login.
		_, _ = fmt.Fprintln(env.Stderr, messages.NoLoginAtStart)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = bridge.Run(ctx, bridge.Options{
		Endpoint: cfg.MCPURL,
		Tokens:   oauth.NewLazySource(oauth.NewHTTPClient(cfg.Origin, userAgent(env), requestTimeout), logins, cfg.Origin),
		Version:  env.Version,
		// A call can run as long as the agent waits, so this client carries no overall timeout.
		HTTP:   oauth.NewHTTPClient(cfg.Origin, userAgent(env), 0),
		Stdin:  env.Stdin,
		Stdout: env.Stdout,
		Log:    slog.New(slog.NewTextHandler(env.Stderr, nil)),
	})
	if err != nil {
		_, _ = fmt.Fprintln(env.Stderr, messages.Unreachable)

		return 1
	}

	return 0
}

// signInFailure is the sentence for a failed login.
func signInFailure(err error) string {
	switch {
	case errors.Is(err, oauth.ErrDenied):
		return messages.LoginDenied
	case errors.Is(err, oauth.ErrLoginTimedOut):
		return messages.LoginTimedOut
	case errors.Is(err, oauth.ErrLoginRefused):
		return messages.LoginRefused
	case errors.Is(err, oauth.ErrNoRefreshToken):
		return messages.LoginNotKept
	case errors.Is(err, oauth.ErrNotServed):
		return messages.NotServed
	default:
		return messages.LoginFailed
	}
}

// usageError prints a command's usage to standard error.
func usageError(env Env, usage string) int {
	_, _ = io.WriteString(env.Stderr, usage)

	return 2
}
