package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
)

// supportAccessResponse is the window an environment has, or null when it has
// never had one. It carries nothing the hub uses to reach the environment.
type supportAccessResponse struct {
	Window *supportWindowView `json:"window"`
}

// supportWindowView is a window as the console reads it. ShareNote is what to
// send a support engineer so they can connect. It is set only on an open window
// and only for a caller who may change support access.
type supportWindowView struct {
	hub.SupportWindow
	ShareNote string `json:"shareNote,omitempty"`
}

type supportAccessRequest struct {
	Level         string `json:"level"`
	Hours         int    `json:"hours"`
	CaseReference string `json:"caseReference"`
}

// supportExtendRequest is all an extension may change: when the window ends.
// Level and case reference stay as approved.
type supportExtendRequest struct {
	Hours int `json:"hours"`
}

// requireSupportAccessWriter is the one place that decides who may open and end
// support access. Today that is anyone signed in, as it is for monitoring
// settings. It becomes Team admin here and nowhere else.
func (s *Server) requireSupportAccessWriter(next http.Handler) http.Handler {
	return s.requireCaller(next)
}

// openedNote is what the case and the console say about an open window.
func openedNote(environment, addressHost, guideURL string, window hub.SupportWindow) service.SupportAccessNote {
	return service.SupportAccessNote{
		GuideURL:      guideURL,
		CaseReference: window.CaseReference,
		Environment:   environment,
		Level:         supportLevelLabel(window.Level),
		AddressHost:   addressHost,
		ExpiresAt:     window.ExpiresAt,
	}
}

// supportWindowView adds what only a caller who may change access sees. The
// address is private, so the text that carries it goes to them and nobody else.
// A caller may change access under the same rule requireSupportAccessWriter
// enforces, so both change together. The text is a convenience: when it cannot
// be built the window is returned without it.
func (s *Server) supportWindowView(
	r *http.Request, hubSvc *hub.Service, environment string, window *hub.SupportWindow,
) *supportWindowView {
	if window == nil {
		return nil
	}
	view := &supportWindowView{SupportWindow: *window}
	if window.State != hub.SupportStateOpen || !s.isCaller(r) {
		return view
	}
	host, err := hubSvc.SupportAddressHost(r.Context(), environment)
	if err != nil {
		log.FromContext(r.Context()).Warn("the address to share could not be read",
			"environment", environment, "error", err.Error())

		return view
	}
	if host != "" {
		view.ShareNote = openedNote(environment, host, s.support.GuideURL(), *window).OpenedText()
	}

	return view
}

func supportAccessError(err error) *AppError {
	switch {
	case errors.Is(err, hub.ErrInvalidEnvironment), errors.Is(err, engine.ErrNotFound):
		return NotFound("That environment does not exist. Check its name and try again.").WithCause(err)

	case errors.Is(err, hub.ErrUnreachable):
		return Conflict("Support access needs a connected environment. Try again once it reports in.").
			WithCause(err)

	case errors.Is(err, hub.ErrSupportAccessActive):
		return Conflict("Support access is already active on this environment. End it to open another.").
			WithCause(err)

	case errors.Is(err, hub.ErrSupportNoWindow):
		return Conflict("There is no support access to extend. Open it first.").WithCause(err)

	case errors.Is(err, hub.ErrSupportNotOpen):
		return Conflict("Support access is still starting. Extend it once it is open.").WithCause(err)

	case errors.Is(err, hub.ErrSupportExtendRange):
		return invalidSupportAccess(
			fmt.Sprintf("Choose a time up to %d hours from now.", hub.MaxSupportHours), err)

	case errors.Is(err, hub.ErrSupportExtendNotLater):
		return invalidSupportAccess("Choose a time later than when access ends now.", err)

	case errors.Is(err, hub.ErrInvalidSupportLevel):
		return invalidSupportAccess("Choose read only or admin access.", err)

	case errors.Is(err, hub.ErrInvalidSupportHours):
		return invalidSupportAccess("Choose 1, 4, 24 or 72 hours.", err)

	case errors.Is(err, hub.ErrInvalidSupportCaseReference):
		return invalidSupportAccess(
			fmt.Sprintf("Shorten the case reference to %d characters or fewer.", hub.MaxSupportCaseReference), err)

	case errors.Is(err, hub.ErrSupportNetwork):
		// What failed is for the hub's log. A customer cannot act on the
		// private network, and is not told what runs it.
		return Downstream("Contact Percona support.", err)

	case errors.Is(err, hub.ErrPersist):
		return hubUnavailable(err)

	default:
		return Internal(somethingWentWrong, err)
	}
}

// invalidSupportAccess is a value the customer can fix. It is 422, not the 400
// of a body that is not the documented shape.
func invalidSupportAccess(message string, cause error) *AppError {
	return Validation(message).WithCause(cause).WithStatus(http.StatusUnprocessableEntity)
}

// handleGetSupportAccess answers with an environment's current window.
func (s *Server) handleGetSupportAccess(w http.ResponseWriter, r *http.Request) {
	hubSvc, err := s.hub(r)
	if err != nil {
		Respond(w, r, nil, hubUnavailable(err))

		return
	}
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	window, err := hubSvc.SupportAccess(r.Context(), name)
	if err != nil {
		Respond(w, r, nil, supportAccessError(err))

		return
	}

	Respond(w, r, supportAccessResponse{Window: s.supportWindowView(r, hubSvc, name, window)}, nil)
}

// handleOpenSupportAccess opens a window on an environment. The window starts
// opening: it is open once the environment's agent says it is.
func (s *Server) handleOpenSupportAccess(w http.ResponseWriter, r *http.Request) {
	hubSvc, err := s.hub(r)
	if err != nil {
		Respond(w, r, nil, hubUnavailable(err))

		return
	}
	var in supportAccessRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody))
	// Unknown fields are refused, so a request cannot pretend to choose
	// something the hub decides, such as the address.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		Respond(w, r, nil, Validation("That request body is not valid support access settings.").WithCause(err))

		return
	}

	name := strings.TrimSpace(chi.URLParam(r, "name"))
	window, err := hubSvc.OpenSupportAccess(r.Context(), name, hub.SupportOpen{
		Level: in.Level, Hours: in.Hours, CaseReference: in.CaseReference,
	})
	if err != nil {
		Respond(w, r, nil, supportAccessError(err))

		return
	}
	s.activity.Record(r.Context(), service.ActivityEvent{
		Action:      service.ActionSupportAccessOpen,
		Environment: name,
		Target:      name,
		Outcome:     service.OutcomeSucceeded,
		Detail:      openedDetail(window),
	})

	// A window just opened is still starting, so it has nothing to share yet.
	RespondWithStatus(w, r, http.StatusCreated,
		supportAccessResponse{Window: &supportWindowView{SupportWindow: window}}, nil)
}

// openedDetail says what was opened, in the words the history shows.
func openedDetail(window hub.SupportWindow) string {
	detail := supportLevelLabel(window.Level) + " access until " + window.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC")
	if window.CaseReference != "" {
		detail += " for case " + window.CaseReference
	}

	return detail + "."
}

// supportLevelLabel is an access level as a customer reads it.
func supportLevelLabel(level string) string {
	if level == hub.SupportLevelAdmin {
		return "Admin"
	}

	return "Read only"
}

// handleExtendSupportAccess asks for an open window to end later. The window
// keeps its expiry and says where it is going until the environment confirms.
func (s *Server) handleExtendSupportAccess(w http.ResponseWriter, r *http.Request) {
	hubSvc, err := s.hub(r)
	if err != nil {
		Respond(w, r, nil, hubUnavailable(err))

		return
	}
	var in supportExtendRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody))
	// Level and case reference are not extendable, so a body naming either is
	// refused rather than ignored.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		Respond(w, r, nil, Validation("That request body is not valid support access settings.").WithCause(err))

		return
	}

	name := strings.TrimSpace(chi.URLParam(r, "name"))
	window, err := hubSvc.ExtendSupportAccess(r.Context(), name, in.Hours)
	if err != nil {
		Respond(w, r, nil, supportAccessError(err))

		return
	}
	s.activity.Record(r.Context(), service.ActivityEvent{
		Action:      service.ActionSupportAccessExtend,
		Environment: name,
		Target:      name,
		Outcome:     service.OutcomeSucceeded,
		Detail:      "Access extended until " + window.ExtendingTo.UTC().Format("2006-01-02 15:04 UTC") + ".",
	})

	Respond(w, r, supportAccessResponse{Window: s.supportWindowView(r, hubSvc, name, &window)}, nil)
}

// handleEndSupportAccess ends an environment's window. Ending one that is
// already closed succeeds and records nothing, so a retry does no harm.
func (s *Server) handleEndSupportAccess(w http.ResponseWriter, r *http.Request) {
	hubSvc, err := s.hub(r)
	if err != nil {
		Respond(w, r, nil, hubUnavailable(err))

		return
	}
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	ended, err := hubSvc.EndSupportAccess(r.Context(), name)
	if err != nil {
		Respond(w, r, nil, supportAccessError(err))

		return
	}
	if ended != nil {
		s.activity.Record(r.Context(), service.ActivityEvent{
			Action:      service.ActionSupportAccessEnd,
			Environment: name,
			Target:      name,
			Outcome:     service.OutcomeSucceeded,
			Detail:      "Access ended early.",
		})
		s.support.NoteAccessEndedByAdmin(r.Context(), service.SupportAccessNote{
			CaseReference: ended.CaseReference,
			Environment:   name,
		})
	}

	RespondWithStatus(w, r, http.StatusNoContent, nil, nil)
}

// recordSupportEvents writes up what an agent's beat did to a window. The agent
// is not a User, so these are done by the system, and the detail is a safe
// sentence: what the agent said went wrong stays in the hub's log.
func (s *Server) recordSupportEvents(ctx context.Context, environment string, events []hub.SupportEvent) {
	for _, event := range events {
		entry := service.ActivityEvent{
			Actor:       service.ActorSystem,
			Environment: environment,
			Target:      environment,
			Outcome:     service.OutcomeSucceeded,
		}
		switch event.Kind {
		case hub.SupportEventInstalled:
			entry.Action = service.ActionSupportAccessInstall
			entry.Detail = "Access is open on the environment."
			s.support.NoteAccessOpened(ctx, openedNote(environment, event.AddressHost, s.support.GuideURL(), *event.Opened))
		case hub.SupportEventRemoved:
			entry.Action = service.ActionSupportAccessUninstall
			entry.Detail = "Access was removed from the environment."
		case hub.SupportEventStartFailed:
			log.FromContext(ctx).Warn("a support access workload did not start",
				"environment", environment, "window", event.Window, "reason", event.Reason)

			continue
		case hub.SupportEventInstallFailed:
			entry.Action = service.ActionSupportAccessFail
			entry.Outcome = service.OutcomeFailed
			entry.Detail = "Access could not be started, so the window closed."
		default:
			continue
		}
		s.activity.Record(ctx, entry)
	}
}
