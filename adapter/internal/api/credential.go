package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/service"
)

// credentialResponse carries the one value the connect card copies.
//
// This is the only response body in the adapter that contains a password, and
// keeping it the only one is the point: every other payload can be checked by
// the absence of the credential rather than by reading it. The connect card
// asks for this on click, so a page view never moves a credential.
type credentialResponse struct {
	URI string `json:"uri"`
}

// handleDatabaseCredential answers with the connection string, password
// included. Host, port and username stay on the database payload, because the
// card renders those and none of them is a secret.
//
// Every read is recorded in Activity, by a person or by an agent, whether or
// not it produced a string, and never with the string.
func (s *Server) handleDatabaseCredential(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id := databaseID(r)

	uri, appErr := s.databaseCredential(r, id)

	event := service.ActivityEvent{
		Action: service.ActionDatabaseCredential, DatabaseID: id, Target: service.NameFromID(id),
		Outcome: service.OutcomeSucceeded,
	}
	if appErr != nil {
		event.Outcome = service.OutcomeFailed
		event.Error = "the connection string could not be read"
	}
	s.activity.Record(r.Context(), event)

	if appErr != nil {
		Respond(w, r, nil, appErr)

		return
	}

	Respond(w, r, credentialResponse{URI: uri}, nil)
}

func (s *Server) databaseCredential(r *http.Request, id string) (string, *AppError) {
	// A spoke strips the connection string before its heartbeat leaves the
	// environment, and the hub strips it again on the way in, except for a public
	// database that already has its address. The hub keeps that string in memory
	// only, so it is missing after a restart until the next beat. Everything
	// else stays on the environment, and saying so beats a 404 that reads like
	// the database is still coming up.
	if _, err := s.hub(r); err != nil {
		return "", hubUnavailable(err)
	}

	instance, getErr := s.databases.Get(r.Context(), id)
	if getErr == nil && instance.Exposure.PubliclyReachable() {
		if instance.Connection == nil || instance.Connection.URI == "" {
			return "", NotFound("The connection string is not ready yet. Try again in a moment.")
		}

		return instance.Connection.URI, nil
	}
	if getErr != nil && !errors.Is(getErr, engine.ErrNotFound) {
		return "", databaseError(getErr, service.NameFromID(id))
	}

	return "", NotImplemented(
		"No password is held here. Read the connection Secret on the Kubernetes cluster the database runs on.")
}

// passwordMask stands in for the password on screen. Fixed width, so it leaks
// no length.
const passwordMask = "••••••••"

// maskURI replaces the password in a connection string so the connect card can
// show the shape of the URI without the adapter ever putting a credential in a
// list or a detail payload.
//
// A string with no scheme:// prefix comes back unchanged: it has no user info
// to hide. Anything that looks like it may still carry user info, but cannot
// be parsed safely, returns empty so a password cannot appear in a list payload.
func maskURI(uri string) string {
	schemeEnd := strings.Index(uri, "://")
	if schemeEnd == -1 {
		return uri
	}

	authorityStart := schemeEnd + len("://")
	authority := uri[authorityStart:]
	if cut := strings.IndexAny(authority, "/?#"); cut != -1 {
		// A delimiter inside userinfo hides the real at sign. Masking from
		// the truncated prefix would keep the rest of the password in the
		// list payload, even when that prefix also contains an at sign
		// (p@ss/word@host). Fail closed whenever the cut remainder still
		// has one.
		if strings.Contains(authority[cut:], "@") {
			return ""
		}
		authority = authority[:cut]
	}

	// A password may contain an at sign, so the last one ends the user info.
	userInfoEnd := strings.LastIndex(authority, "@")
	if userInfoEnd == -1 {
		// No user info inside the authority, but an at sign further along
		// means one may still be there in a shape this cannot read. Returning
		// the input would put that password in the database list for every
		// row on the page, so say nothing instead. The card renders host,
		// port and username, and the real string is a click away on the
		// credential route.
		if strings.Contains(uri[authorityStart:], "@") {
			return ""
		}

		return uri
	}

	separator := strings.Index(authority[:userInfoEnd], ":")
	if separator == -1 {
		return uri
	}

	return uri[:authorityStart+separator+1] + passwordMask + uri[authorityStart+userInfoEnd:]
}
