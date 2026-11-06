package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/log"
)

// supportNoteTimeout bounds a note, so a slow ServiceNow cannot hold up the
// request or the heartbeat that opened or ended access.
const supportNoteTimeout = 15 * time.Second

// SupportAccessNote is what a note on a case says about a window. It holds
// nothing that authenticates: no key, no key id and no connection string.
type SupportAccessNote struct {
	CaseReference string
	Environment   string
	// Level is the access level as a customer reads it, such as "Read only".
	Level string
	// AddressHost is the DNS name of the private address support reaches the
	// environment at. It reaches only the case note and the open window's
	// response to a member who may change access.
	AddressHost string
	ExpiresAt   time.Time
	// GuideURL is where the engineer's connection steps are written. Empty
	// leaves the line out.
	GuideURL string
}

// NoteAccessOpened tells the case that access is open and where to reach it.
func (s *Support) NoteAccessOpened(ctx context.Context, n SupportAccessNote) {
	s.noteCase(ctx, n.CaseReference, n.OpenedText()+"\n"+pdsTicketMarker)
}

// NoteAccessEndedByAdmin tells the case that an admin ended access early.
func (s *Support) NoteAccessEndedByAdmin(ctx context.Context, n SupportAccessNote) {
	s.noteCase(ctx, n.CaseReference, endedNote(n, "was ended by an admin"))
}

// NoteAccessExpired tells the case that access ran out.
func (s *Support) NoteAccessExpired(ctx context.Context, n SupportAccessNote) {
	s.noteCase(ctx, n.CaseReference, endedNote(n, "expired"))
}

// OpenedText says where and how long access is open, in words an engineer can
// act on. The case gets it when access opens, and the console shows the same
// text to a member who may change access, to send on by hand when the case
// cannot be told.
func (n SupportAccessNote) OpenedText() string {
	lines := []string{
		"Support access is open on environment " + n.Environment + ".",
		"Access level: " + n.Level + ".",
		"Open until: " + n.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC") + ".",
		"Private address: " + n.AddressHost + ".",
		"Support connects to it over Percona's private network with their own identity.",
	}
	if n.GuideURL != "" {
		lines = append(lines, "Steps for the engineer: "+n.GuideURL)
	}

	return strings.Join(lines, "\n")
}

// endedNote says what happened to access in a clause that follows the
// environment, such as "expired".
func endedNote(n SupportAccessNote, happened string) string {
	return strings.Join([]string{
		"Support access to environment " + n.Environment + " " + happened + ".",
		"Percona can no longer reach the environment.",
		pdsTicketMarker,
	}, "\n")
}

// noteCase posts a note to a case and never fails the caller: a window opens and
// ends whether or not its case could be told. A window with no case reference,
// or a deployment with no ServiceNow, posts nothing.
func (s *Support) noteCase(ctx context.Context, caseReference, body string) {
	caseReference = strings.TrimSpace(caseReference)
	if s == nil || caseReference == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), supportNoteTimeout)
	defer cancel()

	err := s.Reply(ctx, caseReference, body)
	if err == nil || errors.Is(err, ErrSupportUnavailable) {
		return
	}
	log.FromContext(ctx).Warn("the support access note could not be posted to the case",
		"case", caseReference, "error", err.Error())
}
