package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

// organizationUsage is what the subcommand prints with no arguments. It is the only
// documentation an operator reading `kubectl exec` output gets, so it says what
// each command does rather than only naming it.
const organizationUsage = `Onboard a customer and mint the link they set a password with.

  adapter organization create <id>     register an organization and print a fresh invite link
  adapter organization invite <id>     print a fresh invite link for an organization that exists
  adapter organization list            list organizations and whether each has set a password
  adapter organization bootstrap <id>  set a password from BOOTSTRAP_PASSWORD, for development only

An organization id is 3 to 63 characters: lower case letters, digits and
hyphens, starting with a letter. It is the username the customer types.

An invite link is single use and expires. Claiming one replaces the
organization's password and signs out every session it had, so the same command
is how a password is reset.

Reads DATABASE_URL, and HUB_PUBLIC_URL to build the link.
`

// organizationCommandTimeout bounds one command. These run as a Job or through
// kubectl exec, where a hang is invisible until somebody goes looking.
const organizationCommandTimeout = 30 * time.Second

// isOrganizationCommand reports whether the first argument names this
// subcommand.
func isOrganizationCommand(arg string) bool {
	return arg == "organization"
}

// runOrganization is the organization subcommand. It keeps no logic of its own beyond
// argument handling: creating an organization, minting a link and setting a password
// all belong to the identity service, which is where they are tested.
func runOrganization(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(out, organizationUsage)

		return nil
	}

	command := args[0]
	rest := args[1:]

	switch command {
	case "create", "invite", "list", "bootstrap":
	default:
		return fmt.Errorf("unknown command %q, run adapter organization help", command)
	}

	// The id is checked before anything opens a database, so a typo answers at
	// once rather than after a connection timeout.
	var id string
	if command != "list" {
		if len(rest) == 0 {
			return fmt.Errorf("adapter organization %s needs an organization id", command)
		}
		id = strings.TrimSpace(rest[0])
		if err := auth.ValidateUsername(id); err != nil {
			return err
		}
	}

	// The password is an argument in all but name, so it is read here with the
	// rest of them rather than after a database connection the command would
	// not need if it is missing.
	password := ""
	if command == "bootstrap" {
		password = strings.TrimSpace(os.Getenv("BOOTSTRAP_PASSWORD"))
		if password == "" {
			return errors.New("BOOTSTRAP_PASSWORD is required for adapter organization bootstrap")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), organizationCommandTimeout)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	db, identity, err := openIdentity(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	switch command {
	case "create":
		return createOrganization(ctx, identity, cfg, id, out)
	case "invite":
		return printInvite(ctx, identity, cfg, id, out)
	case "list":
		return listOrganizations(ctx, identity, out)
	default:
		return bootstrapOrganization(ctx, identity, id, password, out)
	}
}

// openIdentity connects to the database and returns the login service. It
// connects rather than waiting in the background: a command has nothing to
// serve while it waits, so a failure to connect is a failure of the command.
func openIdentity(ctx context.Context, cfg config.Config) (*store.Store, *service.Identity, error) {
	if !cfg.DatabaseConfigured() {
		return nil, nil, errors.New("DATABASE_URL is required to manage organizations")
	}

	db, err := store.Open(ctx, store.Options{URL: cfg.DatabaseURL, Organization: cfg.Organization})
	if err != nil {
		return nil, nil, err
	}
	if err := db.Migrate(ctx); err != nil {
		_ = db.Close()

		return nil, nil, err
	}

	// No organization is named here. The command works on whichever one it was
	// given, where a request is pinned to the one this deployment serves.
	identity := service.NewIdentity(db.Identity(), service.WithInviteTTL(cfg.InviteTTL))

	return db, identity, nil
}

func createOrganization(
	ctx context.Context,
	identity *service.Identity,
	cfg config.Config,
	id string,
	out io.Writer,
) error {
	created, err := identity.CreateLogin(ctx, id)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(out, "created organization %s\n", id)
	} else {
		fmt.Fprintf(out, "organization %s already exists\n", id)
	}

	return printInvite(ctx, identity, cfg, id, out)
}

func printInvite(
	ctx context.Context,
	identity *service.Identity,
	cfg config.Config,
	id string,
	out io.Writer,
) error {
	token, expires, err := identity.MintInvite(ctx, id)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "\ninvite link for %s, single use, expires %s\n\n  %s\n\n",
		id, expires.UTC().Format(time.RFC3339), inviteLink(cfg.HubPublicURL, token))
	fmt.Fprint(out, "Send it to the customer. Percona never sees the password they set.\n")

	return nil
}

func listOrganizations(ctx context.Context, identity *service.Identity, out io.Writer) error {
	rows, err := identity.Logins(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprint(out, "no organizations yet, run adapter organization create <id>\n")

		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprint(w, "ORGANIZATION\tPASSWORD SET\tCREATED\n")
	for _, row := range rows {
		set := "no"
		if row.Claimed {
			set = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", row.Username, set, row.Created.UTC().Format(time.RFC3339))
	}

	return w.Flush()
}

// bootstrapOrganization sets a password nobody chose, for the development organization
// the chart seeds so kind, scripts/up.sh and the Playwright run can sign in.
//
// The password comes from the environment rather than an argument, so it stays
// out of the process list on a shared machine.
func bootstrapOrganization(
	ctx context.Context,
	identity *service.Identity,
	id, password string,
	out io.Writer,
) error {
	if _, err := identity.CreateLogin(ctx, id); err != nil {
		return err
	}
	if err := identity.SetPassword(ctx, id, password); err != nil {
		return err
	}

	fmt.Fprintf(out, "organization %s can sign in with the bootstrap password\n", id)
	fmt.Fprint(out, "This is for development. Do not enable it on a customer deployment.\n")

	return nil
}

// inviteLink builds the URL the customer opens. The console is served under
// /app on the public origin, with the landing page at the root, so the claim
// page is /app/claim. The token rides in the query
// rather than the path because that is what the claim page reads, and a path
// segment would land in every proxy access log by default.
func inviteLink(base, token string) string {
	return strings.TrimSuffix(strings.TrimSpace(base), "/") +
		"/app/claim?token=" + url.QueryEscape(token)
}
