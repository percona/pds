package service_test

import (
	"context"
	"sync"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/clientmeta"
	"github.com/percona/pds/adapter/internal/store"
)

// fakeOAuthPeople is who belongs where.
type fakeOAuthPeople struct {
	mu         sync.Mutex
	placements map[uint]store.Placement
	// also holds the memberships a person has beyond the one in placements.
	also  []store.Placement
	teams map[uint][]auth.Team
}

func (f *fakeOAuthPeople) Membership(_ context.Context, userID, organizationID uint) (store.Placement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.memberships(userID) {
		if p.OrganizationID == organizationID {
			return p, nil
		}
	}

	return store.Placement{}, store.ErrNoMembership
}

func (f *fakeOAuthPeople) memberships(userID uint) []store.Placement {
	var out []store.Placement
	if p, ok := f.placements[userID]; ok {
		out = append(out, p)
	}
	for _, p := range f.also {
		if p.UserID == userID {
			out = append(out, p)
		}
	}

	return out
}

func (f *fakeOAuthPeople) Organizations(_ context.Context, userID uint) ([]store.UserOrganization, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []store.UserOrganization
	for _, p := range f.memberships(userID) {
		out = append(out, store.UserOrganization{ID: p.OrganizationID, Name: p.OrganizationName, Role: p.Role})
	}

	return out, nil
}

func (f *fakeOAuthPeople) Teams(_ context.Context, organizationID uint) ([]auth.Team, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.teams[organizationID], nil
}

func (f *fakeOAuthPeople) remove(userID uint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.placements, userID)
}

// fakeOAuthLimits is the Organization's AI settings.
type fakeOAuthLimits struct {
	mu       sync.Mutex
	settings store.AISettings
	byOrg    map[uint]store.AISettings
}

func (f *fakeOAuthLimits) AI(_ context.Context, organizationID uint) (store.AISettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.byOrg[organizationID]; ok {
		return s, nil
	}

	return f.settings, nil
}

func (f *fakeOAuthLimits) setFor(organizationID uint, s store.AISettings) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byOrg == nil {
		f.byOrg = map[uint]store.AISettings{}
	}
	f.byOrg[organizationID] = s
}

func (f *fakeOAuthLimits) set(s store.AISettings) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settings = s
}

// fakeClientDocs answers client metadata documents without a network.
type fakeClientDocs struct {
	docs   map[string]clientmeta.Document
	err    error
	called []string
}

func (f *fakeClientDocs) Fetch(_ context.Context, id string) (clientmeta.Document, error) {
	f.called = append(f.called, id)
	if f.err != nil {
		return clientmeta.Document{}, f.err
	}
	if err := clientmeta.ValidateClientID(id); err != nil {
		return clientmeta.Document{}, err
	}
	doc, ok := f.docs[id]
	if !ok {
		return clientmeta.Document{}, clientmeta.ErrFetch
	}

	return doc, nil
}
