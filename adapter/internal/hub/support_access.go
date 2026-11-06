package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/percona/pds/adapter/internal/client/tailnet"
)

// Who may be let in, and how much they may do.
const (
	SupportLevelReadOnly = "readOnly"
	SupportLevelAdmin    = "admin"
)

// The states a window moves through. A closed window carries why.
const (
	SupportStateOpening = "opening"
	SupportStateOpen    = "open"
	SupportStateClosed  = "closed"
)

// Why a window closed.
const (
	SupportClosedEnded   = "ended"
	SupportClosedExpired = "expired"
	// SupportClosedInstallFailed means the window did not become reachable in
	// time: the agent did not start the workload, or the network never routed
	// the address to it.
	SupportClosedInstallFailed = "failed"
)

// MaxSupportCaseReference bounds the case reference, in characters, so a stray
// paste cannot break the page or the note on the case.
const MaxSupportCaseReference = 64

// supportKeyTTL is how long a minted key may be used to join. It is the time a
// window has to start, so a key never outlives the wait for it.
const supportKeyTTL = 5 * time.Minute

// supportInstallWithin is how long an opening window has to be confirmed
// installed before it closes as failed.
const supportInstallWithin = 5 * time.Minute

// supportAddressPrefix starts the name of every private address the hub makes,
// so the stray sweep touches nothing else on the network.
const supportAddressPrefix = "svc:pds-"

// supportAddressMu orders creating an address against sweeping for strays. It
// is process wide because the tailnet is shared by every Team's hub service.
var supportAddressMu sync.Mutex

// MaxSupportHours is the furthest from now a window may be made to end, however
// often it is extended.
const MaxSupportHours = 72

// supportHours are the durations a window may be opened for.
var supportHours = []int{1, 4, 24, 72}

var (
	// ErrSupportAccessActive means the environment already has a window that is
	// opening or open.
	ErrSupportAccessActive = errors.New("support access is already active on this environment")
	// ErrInvalidSupportLevel, ErrInvalidSupportHours and
	// ErrInvalidSupportCaseReference mean one value of an open is outside what
	// the hub will accept.
	ErrInvalidSupportLevel         = errors.New("support access level is not readOnly or admin")
	ErrInvalidSupportHours         = errors.New("support access hours are not 1, 4, 24 or 72")
	ErrInvalidSupportCaseReference = errors.New("support access case reference is too long")
	// ErrSupportNetwork means the private network could not give or take back
	// access: it failed, or the environment's address does not match the
	// policy. The cause says which, for the log, and never reaches a customer.
	ErrSupportNetwork = errors.New("support access network is not available")
	// ErrSupportNoWindow means there is no active window to act on.
	ErrSupportNoWindow = errors.New("there is no active support access window")
	// ErrSupportNotOpen means the window has not started, so there is nothing
	// running to extend.
	ErrSupportNotOpen = errors.New("the support access window has not started")
	// ErrSupportExtendRange means the hours of an extension are not between 1
	// and MaxSupportHours.
	ErrSupportExtendRange = errors.New("support access extension hours are out of range")
	// ErrSupportExtendNotLater means the extension would end the window no later
	// than it ends now.
	ErrSupportExtendNotLater = errors.New("support access extension does not end later than the window does")
)

// Tailnet is the private network Percona reaches environments through. The hub
// is the only holder of the credentials behind it.
type Tailnet interface {
	// CreateAddress makes a private address with the tag and port the policy
	// is written for.
	CreateAddress(ctx context.Context, name string) error
	// CheckAddress fails unless the address carries exactly that tag and port.
	CheckAddress(ctx context.Context, name string) error
	// AddressApproved reports whether the device a window joined as hostname is
	// approved to host the address, so the network may route the address to it.
	// An error means the network could not say, which is not a no.
	AddressApproved(ctx context.Context, name, hostname string) (bool, error)
	// AddressHost is the DNS name a private address is served under.
	AddressHost(name string) string
	// MintKey makes a single-use key that expires after ttl.
	MintKey(ctx context.Context, ttl time.Duration) (id, secret string, err error)
	// RevokeKey revokes a key by id.
	RevokeKey(ctx context.Context, id string) error
	// DeleteDevices deletes the devices joined under a window's id, which ends
	// their access at once.
	DeleteDevices(ctx context.Context, window string) error
	// ListDevices returns every device an environment joined with a minted key.
	ListDevices(ctx context.Context) ([]tailnet.Device, error)
	// DeleteDevice deletes one device by id.
	DeleteDevice(ctx context.Context, id string) error
	// ListAddresses returns the name of every private address of the kind the
	// hub creates.
	ListAddresses(ctx context.Context) ([]string, error)
	// DeleteAddress deletes a private address by name.
	DeleteAddress(ctx context.Context, name string) error
}

// SupportWindow is a window as the customer reads it. It holds nothing the hub
// uses to reach the environment.
type SupportWindow struct {
	State         string    `json:"state"`
	Level         string    `json:"level"`
	ExpiresAt     time.Time `json:"expiresAt"`
	CaseReference string    `json:"caseReference,omitempty"`
	ClosedReason  string    `json:"closedReason,omitempty"`
	// ExtendingTo is where an extension the environment has not confirmed yet
	// would move ExpiresAt. ExpiresAt stays the time the environment is known to
	// hold until it is confirmed.
	ExtendingTo time.Time `json:"extendingTo,omitzero"`
}

// SupportSummary is what the environments list says about an active window.
type SupportSummary struct {
	State     string    `json:"state"`
	Level     string    `json:"level"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// SupportOpen is what a customer chooses when they open a window.
type SupportOpen struct {
	Level         string
	Hours         int
	CaseReference string
}

// SupportRecord is everything the hub keeps for one environment's support
// access: the address it is reached at for as long as the environment exists,
// and the current window. Nothing here reaches a customer.
type SupportRecord struct {
	Address string               `json:"address,omitempty"`
	Window  *SupportWindowRecord `json:"window,omitempty"`
}

// SupportWindowRecord is the current window and what the hub holds against it.
// A new window replaces it: history is the activity log's job.
type SupportWindowRecord struct {
	ID string `json:"id"`
	// KeyID names the single-use key minted for the window, never the key.
	KeyID string `json:"keyId,omitempty"`
	// OpenedAt is when the hub started the window, which the install deadline
	// counts from.
	OpenedAt time.Time `json:"openedAt,omitzero"`
	// Confirmed lists the agent confirmations already written up, so a beat that
	// repeats one adds nothing.
	Confirmed []string `json:"confirmed,omitempty"`
	SupportWindow
}

// SupportPersist keeps the support access record of each environment.
type SupportPersist interface {
	Load(ctx context.Context) (map[string]SupportRecord, error)
	Save(ctx context.Context, environment string, rec SupportRecord) error
	// Remove drops an environment's record. Removing one that is not there is
	// not an error.
	Remove(ctx context.Context, environment string) error
}

// MemorySupportPersist holds records in process memory. It is what a hub with
// no database has, and what tests seed and read back.
type MemorySupportPersist struct {
	mu   sync.Mutex
	rows map[string]SupportRecord
}

// NewMemorySupportPersist returns an empty store.
func NewMemorySupportPersist() *MemorySupportPersist {
	return &MemorySupportPersist{rows: map[string]SupportRecord{}}
}

// Load returns a copy of every record.
func (m *MemorySupportPersist) Load(context.Context) (map[string]SupportRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make(map[string]SupportRecord, len(m.rows))
	for environment, rec := range m.rows {
		out[environment] = rec.clone()
	}

	return out, nil
}

// Save replaces one environment's record.
func (m *MemorySupportPersist) Save(_ context.Context, environment string, rec SupportRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.rows[environment] = rec.clone()

	return nil
}

// Remove drops one environment's record.
func (m *MemorySupportPersist) Remove(_ context.Context, environment string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.rows, environment)

	return nil
}

func (r SupportRecord) clone() SupportRecord {
	if r.Window != nil {
		window := *r.Window
		r.Window = &window
	}

	return r
}

func (o SupportOpen) validate() error {
	if o.Level != SupportLevelReadOnly && o.Level != SupportLevelAdmin {
		return fmt.Errorf("%w: %q", ErrInvalidSupportLevel, o.Level)
	}
	valid := false
	for _, hours := range supportHours {
		valid = valid || o.Hours == hours
	}
	if !valid {
		return fmt.Errorf("%w: %d", ErrInvalidSupportHours, o.Hours)
	}
	if utf8.RuneCountInString(o.CaseReference) > MaxSupportCaseReference {
		return ErrInvalidSupportCaseReference
	}

	return nil
}

// current is the window as of now. An open or opening window past its expiry
// reads as closed, whether or not anything has swept it yet.
func (r SupportRecord) current(now time.Time) *SupportWindowRecord {
	if r.Window == nil {
		return nil
	}
	window := *r.Window
	if window.State != SupportStateClosed && !now.Before(window.ExpiresAt) {
		window.State = SupportStateClosed
		window.ClosedReason = SupportClosedExpired
	}
	if window.State == SupportStateClosed {
		window.ExtendingTo = time.Time{}
	}

	return &window
}

func (w SupportWindowRecord) active() bool { return w.State != SupportStateClosed }

// OpenSupportAccess starts a window on an environment that is connected and has
// none active.
func (s *Service) OpenSupportAccess(ctx context.Context, environment string, in SupportOpen) (SupportWindow, error) {
	environment = strings.TrimSpace(environment)
	if err := s.requireInventory(environment); err != nil {
		return SupportWindow{}, err
	}
	in.CaseReference = strings.TrimSpace(in.CaseReference)
	if err := in.validate(); err != nil {
		return SupportWindow{}, err
	}

	unlock := s.spokes.lock(environment)
	defer unlock()
	now := s.now()
	if env, _ := s.inv.Get(environment, now); env.Status != StatusConnected {
		return SupportWindow{}, fmt.Errorf("%w: %q", ErrUnreachable, environment)
	}
	rec, err := s.supportRecord(ctx, environment)
	if err != nil {
		return SupportWindow{}, err
	}
	if window := rec.current(now); window != nil && window.active() {
		return SupportWindow{}, ErrSupportAccessActive
	}
	if err := s.ensureSupportAddress(ctx, environment, &rec); err != nil {
		return SupportWindow{}, err
	}

	id, err := mintID("win_")
	if err != nil {
		return SupportWindow{}, fmt.Errorf("mint a support access window id: %w", err)
	}
	rec.Window = &SupportWindowRecord{
		ID:       id,
		OpenedAt: now.UTC(),
		SupportWindow: SupportWindow{
			State:         SupportStateOpening,
			Level:         in.Level,
			ExpiresAt:     now.Add(time.Duration(in.Hours) * time.Hour).UTC(),
			CaseReference: in.CaseReference,
		},
	}
	if err := s.support.Save(ctx, environment, rec); err != nil {
		return SupportWindow{}, wrapPersist(err)
	}

	return rec.Window.SupportWindow, nil
}

// ExtendSupportAccess asks for an open window to end hours from now. The window
// keeps its expiry and carries ExtendingTo until the environment's agent
// confirms, so what the customer reads is never more time than the environment
// has. Asking again before then replaces the pending extension.
func (s *Service) ExtendSupportAccess(ctx context.Context, environment string, hours int) (SupportWindow, error) {
	environment = strings.TrimSpace(environment)
	if err := s.requireInventory(environment); err != nil {
		return SupportWindow{}, err
	}
	if hours < 1 || hours > MaxSupportHours {
		return SupportWindow{}, fmt.Errorf("%w: %d", ErrSupportExtendRange, hours)
	}

	unlock := s.spokes.lock(environment)
	defer unlock()
	rec, err := s.supportRecord(ctx, environment)
	if err != nil {
		return SupportWindow{}, err
	}
	now := s.now()
	window := rec.current(now)
	switch {
	case window == nil || !window.active():
		return SupportWindow{}, ErrSupportNoWindow
	case window.State != SupportStateOpen:
		return SupportWindow{}, ErrSupportNotOpen
	}
	until := now.Add(time.Duration(hours) * time.Hour).UTC()
	if !until.After(window.ExpiresAt) {
		return SupportWindow{}, ErrSupportExtendNotLater
	}

	rec.Window.ExtendingTo = until
	if err := s.support.Save(ctx, environment, rec); err != nil {
		return SupportWindow{}, wrapPersist(err)
	}
	// An extension the agent has not confirmed is replaced by this one.
	if err := s.work.Cancel(environment, WorkSupportExtend, ""); err != nil {
		slog.Warn("hub could not drop a support access extension",
			slog.String("environment", environment), slog.String("error", err.Error()))
	}

	return rec.Window.SupportWindow, nil
}

// SupportAccess reads an environment's current window, or nil when it has never
// had one.
func (s *Service) SupportAccess(ctx context.Context, environment string) (*SupportWindow, error) {
	environment = strings.TrimSpace(environment)
	if err := s.requireInventory(environment); err != nil {
		return nil, err
	}
	rec, err := s.supportRecord(ctx, environment)
	if err != nil {
		return nil, err
	}
	window := rec.current(s.now())
	if window == nil {
		return nil, nil //nolint:nilnil // no window is an answer, not a failure
	}

	return &window.SupportWindow, nil
}

// SupportAddressHost is the DNS name an environment's support address is served
// under, or empty when it has no address yet.
func (s *Service) SupportAddressHost(ctx context.Context, environment string) (string, error) {
	rec, err := s.supportRecord(ctx, strings.TrimSpace(environment))
	if err != nil {
		return "", err
	}
	if rec.Address == "" || s.tailnet == nil {
		return "", nil
	}

	return s.tailnet.AddressHost(rec.Address), nil
}

// EndSupportAccess closes an environment's active window and returns it as it
// was, or nil when there was none to close. A window already closed, or never
// opened, is not an error and is not reported as ended, so the caller writes one
// entry per end.
func (s *Service) EndSupportAccess(ctx context.Context, environment string) (*SupportWindow, error) {
	environment = strings.TrimSpace(environment)
	if err := s.requireInventory(environment); err != nil {
		return nil, err
	}

	unlock := s.spokes.lock(environment)
	defer unlock()
	rec, err := s.supportRecord(ctx, environment)
	if err != nil {
		return nil, err
	}
	window := rec.current(s.now())
	if window == nil || !window.active() {
		return nil, nil //nolint:nilnil // no window to end is an answer, not a failure
	}
	// Access is taken away before the window reads closed: a window that says
	// it ended while its device is still joined would be a lie.
	if err := s.revokeSupportAccess(ctx, window); err != nil {
		return nil, err
	}
	ended := window.SupportWindow
	rec.Window.State = SupportStateClosed
	rec.Window.ClosedReason = SupportClosedEnded
	rec.Window.KeyID = ""
	rec.Window.ExtendingTo = time.Time{}
	if err := s.support.Save(ctx, environment, rec); err != nil {
		return nil, wrapPersist(err)
	}
	// An open or extension the agent has not run yet is no longer wanted. An
	// open that slips through gets no key, because the window is closed.
	for _, kind := range []string{WorkSupportOpen, WorkSupportExtend} {
		if err := s.work.Cancel(environment, kind, ""); err != nil {
			slog.Warn("hub could not drop an unrun support access item",
				slog.String("environment", environment), slog.String("kind", kind), slog.String("error", err.Error()))
		}
	}

	return &ended, nil
}

// MintSupportKey mints the single-use key an environment's agent joins the
// network with, for its active window. The secret is returned once: the hub
// keeps only the key's id, and revokes it when the window ends or when it mints
// the window's next key. Asking again before the window has started replaces
// the key, so one lost on the way does not fail a window that could still start.
func (s *Service) MintSupportKey(ctx context.Context, environment string) (string, error) {
	environment = strings.TrimSpace(environment)
	if err := s.requireInventory(environment); err != nil {
		return "", err
	}

	unlock := s.spokes.lock(environment)
	defer unlock()

	return s.mintSupportKeyLocked(ctx, environment, "")
}

// mintSupportKeyLocked is MintSupportKey for a caller that holds the
// environment's lock. A window other than the active one has no key to mint.
func (s *Service) mintSupportKeyLocked(ctx context.Context, environment, windowID string) (string, error) {
	if s.tailnet == nil {
		return "", fmt.Errorf("%w: no credentials are configured", ErrSupportNetwork)
	}
	rec, err := s.supportRecord(ctx, environment)
	if err != nil {
		return "", err
	}
	window := rec.current(s.now())
	if window == nil || !window.active() || (windowID != "" && window.ID != windowID) {
		return "", ErrSupportNoWindow
	}
	if window.KeyID != "" {
		if err := s.tailnet.RevokeKey(ctx, window.KeyID); err != nil {
			return "", fmt.Errorf("%w: revoke the previous key: %w", ErrSupportNetwork, err)
		}
	}
	id, secret, err := s.tailnet.MintKey(ctx, supportKeyTTL)
	if err != nil {
		return "", fmt.Errorf("%w: mint a key: %w", ErrSupportNetwork, err)
	}
	rec.Window.KeyID = id
	// A key whose id was not kept could never be revoked, so it is recorded or
	// revoked before this returns. Each step gets its own budget: the calls
	// above may have used up ctx, and the caller may have gone.
	saveCtx, cancelSave := context.WithTimeout(s.lifetime, inventoryPersistTimeout)
	defer cancelSave()
	if err := s.support.Save(saveCtx, environment, rec); err != nil {
		revokeCtx, cancelRevoke := context.WithTimeout(s.lifetime, inventoryPersistTimeout)
		defer cancelRevoke()
		if revokeErr := s.tailnet.RevokeKey(revokeCtx, id); revokeErr != nil {
			slog.Warn("hub could not revoke a support access key it failed to record",
				slog.String("environment", environment), slog.String("error", revokeErr.Error()))
		}

		return "", wrapPersist(err)
	}

	return secret, nil
}

// ensureSupportAddress gives the record an address, creating it on the
// environment's first window, and checks it against the policy. The address is
// kept for as long as the environment exists and is never reused for another.
func (s *Service) ensureSupportAddress(ctx context.Context, environment string, rec *SupportRecord) error {
	if s.tailnet == nil {
		return nil
	}
	if rec.Address == "" {
		id, err := mintID("")
		if err != nil {
			return fmt.Errorf("mint a support address name: %w", err)
		}
		if err := s.createSupportAddress(ctx, environment, supportAddressPrefix+id, rec); err != nil {
			return err
		}
	}
	if err := s.tailnet.CheckAddress(ctx, rec.Address); err != nil {
		return fmt.Errorf("%w: check the address: %w", ErrSupportNetwork, err)
	}

	return nil
}

// createSupportAddress creates an address and keeps it in the record at once, so
// a refused check does not leave an address the next open would create a second
// time. It holds supportAddressMu across both, so the stray sweep never sees an
// address that no record holds yet.
func (s *Service) createSupportAddress(ctx context.Context, environment, name string, rec *SupportRecord) error {
	supportAddressMu.Lock()
	defer supportAddressMu.Unlock()

	if err := s.tailnet.CreateAddress(ctx, name); err != nil {
		return fmt.Errorf("%w: create the address: %w", ErrSupportNetwork, err)
	}
	rec.Address = name
	if err := s.support.Save(ctx, environment, *rec); err != nil {
		return wrapPersist(err)
	}

	return nil
}

// revokeSupportAccess ends a window's reach: its devices go, then its key.
func (s *Service) revokeSupportAccess(ctx context.Context, window *SupportWindowRecord) error {
	if s.tailnet == nil {
		return nil
	}
	if err := s.tailnet.DeleteDevices(ctx, window.ID); err != nil {
		return fmt.Errorf("%w: delete the devices: %w", ErrSupportNetwork, err)
	}
	if window.KeyID != "" {
		if err := s.tailnet.RevokeKey(ctx, window.KeyID); err != nil {
			return fmt.Errorf("%w: revoke the key: %w", ErrSupportNetwork, err)
		}
	}

	return nil
}

// removeSupportAccess takes an environment's support access away with the
// environment: its active window's devices and key, then its address, then the
// record. The address is never reused, so the record is dropped with it. It
// reports whether the environment held anything.
//
// A network that refuses is logged and does not stop the removal. The record is
// dropped anyway, which leaves the device and the address to the stray sweep:
// nothing holds them any more.
func (s *Service) removeSupportAccess(ctx context.Context, environment string) (bool, error) {
	rec, err := s.supportRecord(ctx, environment)
	if err != nil {
		return false, err
	}
	if rec.Address == "" && rec.Window == nil {
		return false, nil
	}

	if window := rec.current(s.now()); window != nil && window.active() {
		if err := s.revokeSupportAccess(ctx, window); err != nil {
			slog.WarnContext(ctx, "the hub could not take back access for the window of a removed environment",
				slog.String("environment", environment), slog.String("error", err.Error()))
		}
	}
	if s.tailnet != nil && rec.Address != "" {
		if err := s.tailnet.DeleteAddress(ctx, rec.Address); err != nil {
			slog.WarnContext(ctx, "the hub could not delete the support address of a removed environment",
				slog.String("environment", environment), slog.String("error", err.Error()))
		}
	}
	if err := s.support.Remove(ctx, environment); err != nil {
		return false, wrapPersist(err)
	}

	return true, nil
}

// SupportSummaries says which environments have an active window.
func (s *Service) SupportSummaries(ctx context.Context) (map[string]SupportSummary, error) {
	rows, err := s.support.Load(ctx)
	if err != nil {
		return nil, wrapPersist(err)
	}
	now := s.now()
	out := map[string]SupportSummary{}
	for environment, rec := range rows {
		if window := rec.current(now); window != nil && window.active() {
			out[environment] = SupportSummary{State: window.State, Level: window.Level, ExpiresAt: window.ExpiresAt}
		}
	}

	return out, nil
}

func (s *Service) supportRecord(ctx context.Context, environment string) (SupportRecord, error) {
	rows, err := s.support.Load(ctx)
	if err != nil {
		return SupportRecord{}, wrapPersist(err)
	}

	return rows[environment], nil
}
