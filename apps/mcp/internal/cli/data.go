package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/apps/mcp/internal/config"
	"github.com/percona/pds/apps/mcp/internal/connections"
	"github.com/percona/pds/apps/mcp/internal/data"
	"github.com/percona/pds/apps/mcp/internal/messages"
	"github.com/percona/pds/apps/mcp/internal/oauth"
	"github.com/percona/pds/apps/mcp/internal/publicip"
	"github.com/percona/pds/apps/mcp/internal/vault"
)

// toolListCheck is how often the data server compares its tool list with the saved sign in.
const toolListCheck = 5 * time.Second

// runData is pds-mcp data: the local data server, over stdio.
func runData(ctx context.Context, env Env, cfg config.Config, args []string) int {
	if len(args) > 0 || env.Interactive {
		return usageError(env, messages.DataUsage)
	}
	logins, err := store(env)
	if err != nil {
		_, _ = fmt.Fprintln(env.Stderr, err)

		return 1
	}
	connectionsFile, err := config.ConnectionsFile(env.Getenv)
	if err != nil {
		_, _ = fmt.Fprintln(env.Stderr, err)

		return 1
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	hc := oauth.NewHTTPClient(cfg.Origin, userAgent(env), requestTimeout)
	log := slog.New(slog.NewTextHandler(env.Stderr, nil))
	server := data.New(data.Deps{
		Origin: cfg.Origin, Version: env.Version, Logins: logins, Tokens: oauth.NewLazySource(hc, logins, cfg.Origin), HTTP: hc,
		Vault: vault.OS(), Connections: connections.New(connectionsFile), Runner: data.DatabaseRunner{},
		PublicIP: func(ctx context.Context) (string, error) {
			return publicip.Lookup(ctx, publicip.NewClient(), publicip.Endpoint)
		},
		WorkDir: env.WorkDir, Log: log, SyncEvery: toolListCheck,
	})

	if err := server.Run(ctx, &sdk.IOTransport{Reader: env.Stdin, Writer: env.Stdout}); err != nil && ctx.Err() == nil {
		_, _ = fmt.Fprintln(env.Stderr, messages.Unreachable)

		return 1
	}

	return 0
}
