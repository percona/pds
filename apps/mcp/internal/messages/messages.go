// Package messages holds every fixed sentence pds-mcp says to a person or to an agent, so one test
// can hold them to the customer copy rules.
package messages

import (
	"fmt"
	"strings"
)

// Settings earlier versions read. Each sentence follows the setting's name.
const (
	RemovedSignIn = "is no longer used. pds-mcp signs in through the browser: run pds-mcp login once, then start the MCP server without it."
	RemovedChoice = "is no longer used. The organization and team are chosen when you run pds-mcp login."
	RemovedAccess = "is no longer used. What an agent may change is chosen when you run pds-mcp login."
)

// Control plane address.
const (
	BadControlPlane      = "PDS_URL is not a control plane address. Set it to an address such as https://pds.example.com, with no path."
	InsecureControlPlane = "PDS_URL must use https. Plain http is allowed only for localhost."
	NoHomeDirectory      = "pds-mcp cannot find your home directory. Set HOME or XDG_CONFIG_HOME."
)

// What an agent is told when a request cannot be made. Each one names the command that fixes it,
// because the agent cannot sign in and the person can.
const (
	NotSignedIn     = "This computer is not signed in to the control plane. Ask the user to run pds-mcp login in a terminal, then try again."
	SessionEnded    = "The control plane ended this sign in, or it expired. Ask the user to run pds-mcp login again, then try again."
	NotServed       = "This control plane does not serve AI agents. Ask the person who runs it to turn on AI agent access."
	Unreachable     = "The control plane did not answer. Try again in a moment."
	Refused         = "The control plane did not accept that request. Try again in a moment."
	LoginUnreadable = "The saved sign in could not be read. Ask the user to run pds-mcp login again, then try again."
)

// NeedsPermission is what an agent is told when the sign in does not allow what it asked for.
func NeedsPermission(scope string) string {
	return fmt.Sprintf("This needs the permission %s, which the sign in does not allow. "+
		"Ask the user to run pds-mcp login again and allow it, then try again.", cleanScope(scope))
}

// cleanScope keeps a scope name to the characters one can hold, because it comes from the
// control plane and is repeated to an agent.
func cleanScope(scope string) string {
	var b strings.Builder
	for _, r := range scope {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == ':', r == '_', r == '.', r == '-', r == ' ':
			b.WriteRune(r)
		}
		if b.Len() >= 64 {
			break
		}
	}
	if b.Len() == 0 {
		return "it needs"
	}

	return strings.TrimSpace(b.String())
}

// What an agent is told when the data server refuses a statement before it reaches the database.
const (
	SQLEmpty     = "Send one SQL statement."
	SQLMultiple  = "Send one SQL statement at a time. A request with several statements is refused."
	SQLNotRead   = "This statement is not a query. run_read_query takes SELECT, WITH, SHOW, EXPLAIN, VALUES, TABLE and DESCRIBE. run_query takes one statement that is not a transaction command, when the sign in allows SQL writes."
	SQLForbidden = "This statement changes data inside a query, or reads or writes files on the database host, and the data server never runs those. Rewrite it as a plain query."
	SQLTooLong   = "The statement is too long. Send a shorter one."
)

// What an agent is told by the data server. Each one says what to do next.
const (
	DataNeedsLogin       = "This computer is not signed in to the control plane. Ask the user to run pds-mcp login --passwords in a terminal, then try again."
	DataSignInPrompt     = "Sign in to the control plane so the data server can fetch a database password and keep it in this computer's credential store."
	DataSignInWaiting    = "Finish signing in on the page your client opened, then try again."
	DataSignInDeclined   = "The sign in was declined. Ask the user to run pds-mcp login --passwords if they want to sign in."
	NoCredentialStore    = "This computer has no credential store the data server can use, so no password was saved. On macOS it uses the Keychain. On Linux it needs a Secret Service such as GNOME Keyring or KWallet."
	NoConnection         = "No connection is saved for that database. Run connect_database first."
	PasswordMissing      = "The saved password for that database is not in the credential store. Run connect_database again."
	NoSQLWrite           = "This sign in does not allow SQL writes. An organization admin has to allow them, then the user runs pds-mcp login --sql-write and ticks the SQL writes box on the sign in page."
	CredentialRefused    = "The database refused the user or the password it was given. Nothing was saved."
	StoredRefused        = "The database refused the saved user or password. Run connect_database again to fetch the current ones."
	UnreachableHost      = "The database host could not be reached from this computer. If the database is public, check that this computer's address is allowed: call get_my_ip and read the database_access_advice prompt."
	TLSFailed            = "The database could not be reached over TLS, and the data server never falls back to plain text."
	StatementTimedOut    = "The statement ran longer than the limit and was stopped."
	NoPublicAddress      = "This computer's public address could not be found. Look it up another way and use it as address/32."
	NameTheDatabase      = "The credential does not name a database. Pass database with the name to use."
	BadCredential        = "The control plane gave a credential the data server could not use."
	UnknownEngine        = "Only MySQL and PostgreSQL databases are supported."
	BadInput             = "The input is not valid. Check the database id, which looks like environment/namespace.name, and the other values."
	EnvBadPath           = "The path has to be an env file inside the working directory, such as .env or .env.local."
	EnvSymlink           = "That path is a symbolic link, and the data server does not write through links."
	EnvBadValue          = "A value has characters that cannot be written to an env file and read back unchanged. Add that value yourself."
	EnvNotWritten        = "The env file could not be written."
	EnvNeedsConfirmation = "Writing the password or replacing keys needs the user to confirm, and this client cannot ask. Write the other settings, and ask the user to add the password themselves."
	EnvNotConfirmed      = "The password was not written, because the user did not confirm it. Nothing was changed."
	EnvNote              = "Do not commit this file. Add it to .gitignore."
	DataUnreadable       = "The saved connections could not be read. They are kept in a file in your config directory."
	QueryFailed          = "The database did not complete the statement."
)

// DatabaseMissing is what an agent is told when the database named does not exist on the server.
func DatabaseMissing(name string) string {
	return "The database " + name + " does not exist on that server. Nothing was created. Create it first, or pass the name of one that exists."
}

// EnvConfirm is what the user is asked before a password is written or a key is replaced. It names
// the file and the key, so a confirmation cannot be read as agreeing to something else.
func EnvConfirm(path, passwordKey string, overwrite bool) string {
	var what []string
	if passwordKey != "" {
		what = append(what, "write the database password as "+passwordKey+", which is kept there as plain text")
	}
	if overwrite {
		what = append(what, "replace keys that already have a value")
	}

	return "Allow the data server to " + strings.Join(what, " and ") + " in " + path + "?"
}

// AccessAdvice is the data server's prompt on how a database should be reached.
const AccessAdvice = `Choose how the database is reached before opening it to anything.

- The application runs in the same VPC or private network as the database: use private access. Nothing is opened to the internet.
- The application runs on a laptop or on a host outside that network: use public access, and allow only the addresses that need it. For this computer, call get_my_ip and use the address it returns as the allowed source. For a server, use that server's address as a /32.
- Never allow 0.0.0.0/0. It puts the database's login in front of the whole internet.
- A public database is protected by its password and by TLS. Keep the connection string and the password out of source control.
- To give an application the settings, call write_env. It adds the password only when the user asked for it, and the file must stay out of Git.`

// Commands.
const (
	Usage = `Usage: pds-mcp [command]

Commands:
  (none)   Connect an AI agent to the control plane over stdio.
  login    Sign in to the control plane in your browser.
  logout   Remove the saved sign in.
  data     Run the local data server for your databases over stdio.
  version  Print the version.

Set PDS_URL to use a control plane other than https://pds.percona.com.
Run pds-mcp login [--access read|change|delete] [--passwords] [--sql-write] [--no-browser].
`
	LoginUsage      = "Usage: pds-mcp login [--access read|change|delete] [--passwords] [--sql-write] [--no-browser]\n"
	DataUsage       = "Usage: pds-mcp data\nRuns the local data server over stdio. Start it from an AI agent, not at a terminal.\n"
	LogoutUsage     = "Usage: pds-mcp logout\n"
	LoginOpening    = "Opening your browser to sign in. If it does not open, go to this address:"
	LoginPrinted    = "Open this address in a browser to sign in:"
	LoginWaiting    = "Waiting for you to finish in the browser."
	LoginDone       = "Signed in. Start the MCP server again so your agent picks it up."
	LoginDenied     = "The sign in was declined. Run pds-mcp login to try again."
	LoginTimedOut   = "The sign in was not finished in time. Run pds-mcp login to try again."
	LoginRefused    = "The control plane refused the sign in. Run pds-mcp login to try again."
	LoginNotKept    = "The control plane did not allow this sign in to be kept. Contact Percona support."
	LoginFailed     = "The sign in did not finish. Check that the control plane address is right and try again."
	LogoutDone      = "Signed out. The saved sign in was removed."
	LogoutNotNeeded = "No sign in is saved for this control plane."
	LogoutRemote    = "The saved sign in was removed here, but the control plane could not be told. Sign out on the AI agents page of the console to end it."
	NoLoginAtStart  = "No sign in is saved for this control plane. Run pds-mcp login, then start the MCP server again."
)

// LoginNarrower says which of the extra permissions the person asked for the sign in does not
// include, because the control plane's Organization has not allowed them.
func LoginNarrower(passwords, sqlWrites bool) string {
	var what string
	switch {
	case passwords && sqlWrites:
		what = "database passwords or SQL writes"
	case passwords:
		what = "database passwords"
	default:
		what = "SQL writes"
	}

	return "The sign in does not include " + what + ". An organization admin has to allow " + what + ", then run pds-mcp login again."
}

// All lists every message, for the copy test.
func All() []string {
	return []string{
		RemovedSignIn, RemovedChoice, RemovedAccess,
		BadControlPlane, InsecureControlPlane, NoHomeDirectory,
		NotSignedIn, SessionEnded, NotServed, Unreachable, Refused, LoginUnreadable,
		NeedsPermission("pds:change"),
		Usage, LoginUsage, LogoutUsage, LoginOpening, LoginPrinted, LoginWaiting, LoginDone, LoginDenied,
		LoginTimedOut, LoginRefused, LoginNotKept, LoginFailed, LogoutDone, LogoutNotNeeded, LogoutRemote,
		NoLoginAtStart,
		DataNeedsLogin, DataSignInPrompt, DataSignInWaiting, DataSignInDeclined, NoCredentialStore, NoConnection,
		PasswordMissing, NoSQLWrite, CredentialRefused, StoredRefused, UnreachableHost, TLSFailed, StatementTimedOut,
		NoPublicAddress, NameTheDatabase, BadCredential, UnknownEngine, BadInput, EnvBadPath, EnvSymlink, EnvBadValue,
		EnvNotWritten, EnvNeedsConfirmation, EnvConfirm(".env", "DB_PASSWORD", true), EnvNotConfirmed, EnvNote, DataUnreadable, QueryFailed, DatabaseMissing("shop"), AccessAdvice, DataUsage, LoginNarrower(true, true), LoginNarrower(true, false),
		LoginNarrower(false, true),
		SQLEmpty, SQLMultiple, SQLNotRead, SQLForbidden, SQLTooLong,
	}
}
