package fakecp

import (
	"net/http"
	"slices"
	"strings"
)

// Database is what the fake holds for one database id.
type Database struct {
	// URI is the connection string the credential route returns.
	URI string
	// Private makes the route answer as a database whose password is not held on the control
	// plane does.
	Private bool
	// Fail makes the route answer with this status and the control plane's sentence for it.
	Fail int
}

// The sentences the real control plane answers the credential route with.
const (
	PasswordsNotAllowed = "This agent is not allowed to read database passwords. Ask an organization admin to allow them, then sign in again."
	NoPasswordHeld      = "No password is held here. Read the connection Secret on the Kubernetes cluster the database runs on."
	NoSuchDatabase      = "That database was not found."
	NotAvailable        = "The control plane is not available. Try again in a moment."
)

// AddDatabase registers a database id such as env/ns.name.
func (s *Server) AddDatabase(id string, db Database) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.databases == nil {
		s.databases = map[string]Database{}
	}
	s.databases[id] = db
}

// CredentialReads lists the requests the credential route received.
func (s *Server) CredentialReads() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.reads)
}

func (s *Server) credential(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.reads = append(s.reads, Request{Method: r.Method, Authorization: r.Header.Get("Authorization"), UserAgent: r.Header.Get("User-Agent"), Header: r.Header.Clone()})
	s.mu.Unlock()

	scope, ok := s.scopeOf(r.Header.Get("Authorization"))
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"code": "unauthorized", "message": "Sign in to continue."})

		return
	}
	if !slices.Contains(strings.Fields(scope), "pds:credentials") {
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "forbidden", "message": PasswordsNotAllowed})

		return
	}

	s.mu.Lock()
	db, found := s.databases[r.PathValue("env")+"/"+r.PathValue("id")]
	s.mu.Unlock()
	switch {
	case !found:
		writeJSON(w, http.StatusNotFound, map[string]string{"code": "not_found", "message": NoSuchDatabase})
	case db.Fail != 0:
		writeJSON(w, db.Fail, map[string]string{"code": "downstream", "message": NotAvailable})
	case db.Private:
		writeJSON(w, http.StatusNotImplemented, map[string]string{"code": "not_implemented", "message": NoPasswordHeld})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"uri": db.URI})
	}
}
