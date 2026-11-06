package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/store"
)

// ErrAgentSessionNotFound means no live agent session with that id is one the
// caller may see. A session of another person, of another Organization, one
// that is already gone and one that never existed read the same.
var ErrAgentSessionNotFound = errors.New("no such agent session")

// OAuthMembers names the people of an Organization. The Directory satisfies it.
type OAuthMembers interface {
	Members(ctx context.Context, organizationID uint) ([]store.Member, error)
}

// WithOAuthMembers sets where the list of agents reads who signed each one in.
func WithOAuthMembers(members OAuthMembers) OAuthOption {
	return func(o *OAuth) { o.members = members }
}

// WithOAuthRecorder sets where a revoked agent is recorded. It may be left out.
func WithOAuthRecorder(recorder ActivityRecorder) OAuthOption {
	return func(o *OAuth) { o.recorder = recorder }
}

// AgentSessionView is one connected agent as the console shows it. It holds
// what a person needs to decide whether to keep it, and no token, hash or
// client id.
type AgentSessionView struct {
	ID         string
	ClientName string
	ClientHost string
	ClientKind string
	Member     string
	Mine       bool
	TeamName   string
	Access     string
	Passwords  bool
	SQLWrites  bool
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
}

// visibleSessions is the live sessions the caller may see: their own, or every
// person's in their Organization for an admin.
func (o *OAuth) visibleSessions(ctx context.Context, scope auth.Scope) ([]store.AgentSession, error) {
	if scope.OrganizationID == 0 || scope.UserID == 0 {
		return nil, nil
	}

	owner := scope.UserID
	if scope.Admin() {
		owner = 0
	}

	now := o.now()
	rows, err := o.store.AgentSessions(ctx, scope.OrganizationID, owner, now)
	if err != nil {
		return nil, fmt.Errorf("list agent sessions: %w", err)
	}

	live := rows[:0]
	for _, row := range rows {
		if o.alive(row, now) {
			live = append(live, row)
		}
	}

	return live, nil
}

// AgentSessions lists the agents the caller may see and revoke: their own, or
// every person's in their Organization for an admin.
func (o *OAuth) AgentSessions(ctx context.Context, scope auth.Scope) ([]AgentSessionView, error) {
	rows, err := o.visibleSessions(ctx, scope)
	if err != nil || len(rows) == 0 {
		return nil, err
	}

	teams, err := o.people.Teams(ctx, scope.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("read teams: %w", err)
	}
	teamNames := make(map[uint]string, len(teams))
	for _, team := range teams {
		teamNames[team.ID] = team.Name
	}

	names, err := o.memberNames(ctx, scope)
	if err != nil {
		return nil, err
	}

	views := make([]AgentSessionView, 0, len(rows))
	for _, row := range rows {
		views = append(views, AgentSessionView{
			ID: row.ID, ClientName: row.ClientName, ClientHost: row.ClientHost, ClientKind: row.ClientKind,
			Member: names.of(row.UserID), Mine: row.UserID == scope.UserID, TeamName: teamNames[row.TeamID],
			Access: row.Access, Passwords: row.Passwords, SQLWrites: row.SQLWrites,
			CreatedAt: row.CreatedAt, LastUsedAt: row.LastUsedAt, ExpiresAt: row.ExpiresAt,
		})
	}

	return views, nil
}

// RevokeAgentSession ends one agent session and every token it holds, so the
// agent's next call is refused. It is recorded in Activity for the person in
// ctx. A session the caller may not see is ErrAgentSessionNotFound.
func (o *OAuth) RevokeAgentSession(ctx context.Context, scope auth.Scope, id string) error {
	rows, err := o.visibleSessions(ctx, scope)
	if err != nil {
		return err
	}

	for _, row := range rows {
		if row.ID != id {
			continue
		}

		if err := o.store.DeleteAgentSession(ctx, row.ID); err != nil {
			return fmt.Errorf("revoke agent session: %w", err)
		}
		o.recordRevoked(ctx, scope, row)

		return nil
	}

	return ErrAgentSessionNotFound
}

func (o *OAuth) recordRevoked(ctx context.Context, scope auth.Scope, row store.AgentSession) {
	if o.recorder == nil {
		return
	}

	event := ActivityEvent{Action: ActionAgentRevoke, Outcome: OutcomeSucceeded, Target: row.ClientName}
	if row.ClientHost != "" {
		event.Target = fmt.Sprintf("%s (%s)", row.ClientName, row.ClientHost)
	}
	if row.UserID != scope.UserID {
		if names, err := o.memberNames(ctx, scope); err == nil {
			event.Detail = "Agent signed in by " + names.of(row.UserID) + "."
		}
	}
	event.Target = clampActivityText(Sanitize(event.Target))
	o.recorder.Record(ctx, event)
}

// sessionOwners maps a person's id to the name the console shows.
type sessionOwners map[uint]string

func (n sessionOwners) of(user uint) string {
	if name, ok := n[user]; ok {
		return name
	}

	return ActorRemovedUser
}

func (o *OAuth) memberNames(ctx context.Context, scope auth.Scope) (sessionOwners, error) {
	names := sessionOwners{scope.UserID: scope.UserName}
	if o.members == nil {
		return names, nil
	}

	members, err := o.members.Members(ctx, scope.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("read members: %w", err)
	}
	for _, member := range members {
		names[member.UserID] = member.Name
	}

	return names, nil
}
