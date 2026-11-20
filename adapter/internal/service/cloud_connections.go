package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/store"
)

// CloudTemplateVersion is the version of the bootstrap template this release
// ships, and the lowest version a connection's stack may report. A stack that
// reports less is Out of date.
const CloudTemplateVersion = 2

// cloudTokenTTL is how long a connect token lives. A role chained session lasts
// an hour at most, so the script's whole run fits in it.
const cloudTokenTTL = time.Hour

// What the cloud connection service refuses beyond the store's own errors.
var (
	// ErrCloudInvalid means the request cannot be a connection as it is.
	ErrCloudInvalid = errors.New("invalid cloud connection request")
	// ErrCloudUnavailable means this deployment has no control plane principal,
	// so no role in a customer's account could trust it.
	ErrCloudUnavailable = errors.New("cloud connections are not configured")
	// ErrCloudConfirm means the name typed to remove a connection is not its own.
	ErrCloudConfirm = errors.New("the typed name does not match the cloud connection")
	// ErrCloudInUse means managed environments still run through the connection.
	ErrCloudInUse = errors.New("managed environments still use the cloud connection")
)

var (
	cloudAccountID = regexp.MustCompile(`^[0-9]{12}$`)
	cloudRegion    = regexp.MustCompile(`^[a-z]{2}(-[a-z0-9]+)+$`)
	cloudAlias     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	cloudStep      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
)

// CloudStore is the state a cloud connection keeps. *store.CloudConnections is
// the one implementation, and tests fake it.
type CloudStore interface {
	CreateToken(ctx context.Context, in store.NewCloudToken) error
	RevokeToken(ctx context.Context, teamID uint, hash string, now time.Time) error
	RevokeTokenByID(ctx context.Context, teamID uint, id string, now time.Time) (store.CloudToken, error)
	TokenTeam(ctx context.Context, hash string, now time.Time) (store.CloudToken, error)
	Enroll(ctx context.Context, in store.CloudEnrollment, now time.Time) (store.CloudConnection, error)
	Get(ctx context.Context, teamID uint, id string) (store.CloudConnection, error)
	List(ctx context.Context, teamID uint) ([]store.CloudConnection, error)
	All(ctx context.Context) ([]store.CloudConnection, error)
	SaveCheck(ctx context.Context, id string, in store.CloudCheck, now time.Time) (store.CloudConnection, error)
	Cancel(ctx context.Context, teamID uint, id string, now time.Time) error
	Remove(ctx context.Context, teamID uint, id string) error
	RecordEvent(ctx context.Context, id string, in store.CloudEvent, now time.Time) error
	Events(ctx context.Context, teamID uint, id string) ([]store.CloudEvent, error)
}

// CloudInspector assumes a connection's role afresh and reads the account. The
// aws client is the production implementation; tests fake it. It returns
// aws.ErrAssumeDenied when AWS refuses the role.
type CloudInspector interface {
	Inspect(ctx context.Context, in aws.InspectRequest) (aws.Inspection, error)
}

// CloudEnvironments lists the managed environments of a Team, so a connection
// that environments run through is not removed.
type CloudEnvironments interface {
	List(ctx context.Context, teamID uint) ([]store.ManagedEnvironment, error)
}

// CloudOptions configures the service.
type CloudOptions struct {
	// PrincipalARN is the role the control plane assumes from. Customer roles
	// trust it by name, never the account root.
	PrincipalARN string
	// Environments is where managed environments are read. Without it no
	// connection counts as in use.
	Environments CloudEnvironments
	Logger       *slog.Logger
	// Now is the clock, replaced by tests.
	Now func() time.Time
}

// CloudConnections is the rules of cloud connections: who may connect an AWS
// account, what the script is told, and when a connection is Ready.
type CloudConnections struct {
	store     CloudStore
	inspector CloudInspector
	opts      CloudOptions
}

// NewCloudConnections builds the service.
func NewCloudConnections(st CloudStore, inspector CloudInspector, opts CloudOptions) *CloudConnections {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	return &CloudConnections{store: st, inspector: inspector, opts: opts}
}

// CloudToken is a connect token as it is shown once.
type CloudToken struct {
	// ID names the token to cancel it. It is the start of the token's hash, so
	// it says nothing about the token.
	ID             string
	Token          string
	ExpiresAt      time.Time
	AllowedRegions []string
}

// CloudAccount is what the script reads from the account it runs in.
type CloudAccount struct {
	ID     string
	Alias  string
	Region string
	// Regions, when set, narrow the token's allowed regions to these.
	Regions []string
}

// CloudEnrollment is what the script is told to build the stack with.
type CloudEnrollment struct {
	ConnectionID        string
	ExternalID          string
	StackName           string
	StackRegion         string
	AllowedRegions      []string
	PrincipalARN        string
	ControlPlaneRoleARN string
	TemplateVersion     int
}

// CloudStep is one step the script reports.
type CloudStep struct {
	Step   string
	Status string
	Detail string
}

// UserSourceIdentity is the SourceIdentity of a call made for a person. The
// trust policy matches pds-user-* and pds-job-*, and AWS wants six literal
// characters before a wildcard and only letters, digits and +=,.@_- anywhere.
func UserSourceIdentity(userID uint) string {
	return "pds-user-" + strconv.FormatUint(uint64(userID), 10)
}

// JobSourceIdentity is the SourceIdentity of a call made by a background job.
func JobSourceIdentity(job string) string { return "pds-job-" + job }

// IssueToken makes a one hour token for the scope's Team. Any User of the Team
// may, the same rights as minting a join token.
func (c *CloudConnections) IssueToken(ctx context.Context, scope auth.Scope, regions []string) (CloudToken, error) {
	if c.opts.PrincipalARN == "" {
		return CloudToken{}, ErrCloudUnavailable
	}
	if len(regions) == 0 {
		return CloudToken{}, fmt.Errorf("%w: choose at least one region", ErrCloudInvalid)
	}
	for _, region := range regions {
		if !cloudRegion.MatchString(region) {
			return CloudToken{}, fmt.Errorf("%w: %q is not a region", ErrCloudInvalid, region)
		}
	}
	raw, _, err := auth.NewToken()
	if err != nil {
		return CloudToken{}, err
	}
	token := "ccn_" + raw
	expires := c.opts.Now().UTC().Add(cloudTokenTTL)
	err = c.store.CreateToken(ctx, store.NewCloudToken{
		TeamID: scope.Team.ID, Hash: auth.HashToken(token), AllowedRegions: regions, ExpiresAt: expires,
	})
	if err != nil {
		return CloudToken{}, err
	}

	return CloudToken{ID: auth.HashToken(token)[:16], Token: token, ExpiresAt: expires, AllowedRegions: regions}, nil
}

// CancelToken ends a connect token. The connection it started is dropped when
// it has never been Ready. A token that is unknown, another Team's, or ended
// already reads as invalid.
func (c *CloudConnections) CancelToken(ctx context.Context, scope auth.Scope, id string) error {
	token, err := c.store.RevokeTokenByID(ctx, scope.Team.ID, id, c.opts.Now())
	if err != nil {
		return err
	}
	if token.ConnectionID == "" {
		return nil
	}
	err = c.store.Cancel(ctx, scope.Team.ID, token.ConnectionID, c.opts.Now())
	if errors.Is(err, store.ErrCloudConnectionReady) || errors.Is(err, store.ErrCloudConnectionUnknown) {
		return nil
	}

	return err
}

// CreateTyped makes a Pending connection for an account the person typed, for
// the path that needs no script. The connection's home region is the first of
// the regions. It enrolls through a token of its own that ends at once, so the
// rules of enrolling stay in one place.
func (c *CloudConnections) CreateTyped(ctx context.Context, scope auth.Scope, accountID string, regions []string) (CloudEnrollment, error) {
	issued, err := c.IssueToken(ctx, scope, regions)
	if err != nil {
		return CloudEnrollment{}, err
	}
	defer func() {
		if err := c.store.RevokeToken(ctx, scope.Team.ID, auth.HashToken(issued.Token), c.opts.Now()); err != nil {
			c.opts.Logger.WarnContext(ctx, "cloud connection helper token was not revoked", "error", err)
		}
	}()

	return c.Enroll(ctx, issued.Token, CloudAccount{ID: accountID, Region: regions[0]})
}

// Remove forgets one of the scope's Team's connections and touches nothing in
// the customer's account. name is the alias the person typed, or the account id
// when the connection has none. A connection that managed environments run
// through stays until they are deleted.
func (c *CloudConnections) Remove(ctx context.Context, scope auth.Scope, id, name string) error {
	row, err := c.store.Get(ctx, scope.Team.ID, id)
	if err != nil {
		return err
	}
	want := row.Alias
	if want == "" {
		want = row.AccountID
	}
	if name == "" || name != want {
		return ErrCloudConfirm
	}
	if c.opts.Environments != nil {
		environments, err := c.opts.Environments.List(ctx, scope.Team.ID)
		if err != nil {
			return err
		}
		for _, environment := range environments {
			if environment.ConnectionID == id {
				return ErrCloudInUse
			}
		}
	}

	return c.store.Remove(ctx, scope.Team.ID, id)
}

// Enroll answers the script's first call: it names the account, and gets the
// connection's id, secret and stack name. The same account always gets the
// same answer, so rerunning the command repairs rather than duplicates.
func (c *CloudConnections) Enroll(ctx context.Context, token string, account CloudAccount) (CloudEnrollment, error) {
	if !cloudAccountID.MatchString(account.ID) {
		return CloudEnrollment{}, fmt.Errorf("%w: the account id is not 12 digits", ErrCloudInvalid)
	}
	if !cloudRegion.MatchString(account.Region) {
		return CloudEnrollment{}, fmt.Errorf("%w: %q is not a region", ErrCloudInvalid, account.Region)
	}
	if len(account.Alias) > 64 || (account.Alias != "" && !cloudAlias.MatchString(account.Alias)) {
		return CloudEnrollment{}, fmt.Errorf("%w: the account alias is not valid", ErrCloudInvalid)
	}
	hash := auth.HashToken(token)
	regions, err := c.narrowRegions(ctx, hash, account.Regions)
	if err != nil {
		return CloudEnrollment{}, err
	}
	id, err := randomHex(6)
	if err != nil {
		return CloudEnrollment{}, err
	}
	external, err := randomHex(24)
	if err != nil {
		return CloudEnrollment{}, err
	}
	row, err := c.store.Enroll(ctx, store.CloudEnrollment{
		TokenHash: hash, AccountID: account.ID, Alias: account.Alias,
		StackRegion: account.Region, AllowedRegions: regions, NewID: id, NewExternalID: external, NewStackName: "pds-" + id,
	}, c.opts.Now())
	if err != nil {
		return CloudEnrollment{}, err
	}

	return CloudEnrollment{
		ConnectionID: row.ID, ExternalID: row.ExternalID, StackName: row.StackName, StackRegion: row.StackRegion,
		AllowedRegions: row.AllowedRegions, PrincipalARN: c.opts.PrincipalARN,
		ControlPlaneRoleARN: aws.RoleARN(row.AccountID, row.ID, "control-plane"), TemplateVersion: CloudTemplateVersion,
	}, nil
}

// narrowRegions checks that the regions a script asked for are among the
// token's, so a script can limit a connection and never widen it.
func (c *CloudConnections) narrowRegions(ctx context.Context, hash string, asked []string) ([]string, error) {
	if len(asked) == 0 {
		return nil, nil
	}
	token, err := c.store.TokenTeam(ctx, hash, c.opts.Now())
	if err != nil {
		return nil, err
	}
	for _, region := range asked {
		if !slices.Contains(token.AllowedRegions, region) {
			return nil, fmt.Errorf("%w: %q is not one of the regions chosen for this command", ErrCloudInvalid, region)
		}
	}

	return asked, nil
}

// RecordEvent keeps a step the script reported. The script's word is a hint:
// only a check makes a connection Ready.
func (c *CloudConnections) RecordEvent(ctx context.Context, token string, step CloudStep) error {
	if !cloudStep.MatchString(step.Step) {
		return fmt.Errorf("%w: the step name is not valid", ErrCloudInvalid)
	}
	switch step.Status {
	case store.CloudStepStarted, store.CloudStepSucceeded, store.CloudStepFailed, store.CloudStepHeartbeat:
	default:
		return fmt.Errorf("%w: the step status is not valid", ErrCloudInvalid)
	}
	live, err := c.liveToken(ctx, token)
	if err != nil {
		return err
	}
	if utf8.RuneCountInString(step.Detail) > 512 {
		step.Detail = string([]rune(step.Detail)[:512])
	}

	return c.store.RecordEvent(ctx, live.ConnectionID, store.CloudEvent{Step: step.Step, Status: step.Status, Detail: step.Detail}, c.opts.Now())
}

// VerifyWithToken runs the connection check for the connection a token
// enrolled. The script calls it last.
func (c *CloudConnections) VerifyWithToken(ctx context.Context, token string) (store.CloudConnection, error) {
	live, err := c.liveToken(ctx, token)
	if err != nil {
		return store.CloudConnection{}, err
	}
	row, err := c.store.Get(ctx, live.TeamID, live.ConnectionID)
	if err != nil {
		return store.CloudConnection{}, err
	}

	return c.check(ctx, row, JobSourceIdentity("connect"))
}

// Verify runs the connection check for one of the scope's Team's connections.
func (c *CloudConnections) Verify(ctx context.Context, scope auth.Scope, id string) (store.CloudConnection, error) {
	row, err := c.store.Get(ctx, scope.Team.ID, id)
	if err != nil {
		return store.CloudConnection{}, err
	}

	return c.check(ctx, row, UserSourceIdentity(scope.UserID))
}

// Get returns one of the scope's Team's connections.
func (c *CloudConnections) Get(ctx context.Context, scope auth.Scope, id string) (store.CloudConnection, error) {
	return c.store.Get(ctx, scope.Team.ID, id)
}

// List returns the scope's Team's connections.
func (c *CloudConnections) List(ctx context.Context, scope auth.Scope) ([]store.CloudConnection, error) {
	return c.store.List(ctx, scope.Team.ID)
}

// Events returns the steps the script reported for a connection.
func (c *CloudConnections) Events(ctx context.Context, scope auth.Scope, id string) ([]store.CloudEvent, error) {
	return c.store.Events(ctx, scope.Team.ID, id)
}

// Cancel drops a connection that has never been Ready.
func (c *CloudConnections) Cancel(ctx context.Context, scope auth.Scope, id string) error {
	return c.store.Cancel(ctx, scope.Team.ID, id, c.opts.Now())
}

// CheckAll runs the connection check on every connection. One that fails does
// not stop the rest.
func (c *CloudConnections) CheckAll(ctx context.Context) error {
	rows, err := c.store.All(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := c.check(ctx, row, JobSourceIdentity("hourly")); err != nil {
			c.opts.Logger.WarnContext(ctx, "cloud connection check did not finish", "connection", row.ID, "error", err)
		}
	}

	return nil
}

// Run checks every connection each interval until the context ends.
func (c *CloudConnections) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.CheckAll(ctx); err != nil {
				c.opts.Logger.WarnContext(ctx, "cloud connection check could not list connections", "error", err)
			}
		}
	}
}

func (c *CloudConnections) liveToken(ctx context.Context, token string) (store.CloudToken, error) {
	live, err := c.store.TokenTeam(ctx, auth.HashToken(token), c.opts.Now())
	if err != nil {
		return store.CloudToken{}, err
	}
	if live.ConnectionID == "" {
		return store.CloudToken{}, store.ErrCloudTokenInvalid
	}

	return live, nil
}

// check assumes the connection's role afresh and records what it finds. It
// never simulates policies, since a simulation misses service control policies.
func (c *CloudConnections) check(ctx context.Context, row store.CloudConnection, sourceIdentity string) (store.CloudConnection, error) {
	inspection, err := c.inspector.Inspect(ctx, aws.InspectRequest{
		RoleARN: aws.RoleARN(row.AccountID, row.ID, "control-plane"), ExternalID: row.ExternalID,
		SourceIdentity: sourceIdentity, Region: row.StackRegion, StackName: row.StackName,
		RoleNames: []string{aws.RoleName(row.ID, "cloudformation"), aws.RoleName(row.ID, "eks-cluster"), aws.RoleName(row.ID, "node"), aws.RoleName(row.ID, "ebs-csi")},
	})
	result := judgeCloudCheck(row, inspection, err)
	if err != nil && !errors.Is(err, aws.ErrAssumeDenied) {
		c.opts.Logger.WarnContext(ctx, "cloud connection check could not reach the account", "connection", row.ID, "error", err)
	}

	return c.store.SaveCheck(ctx, row.ID, result, c.opts.Now())
}

// judgeCloudCheck turns what the account answered into a state and a sentence
// for the customer. A connection that has never been Ready stays Pending
// while it is being set up, and one that was Ready becomes Unreachable.
func judgeCloudCheck(row store.CloudConnection, in aws.Inspection, err error) store.CloudCheck {
	notReady := store.CloudPending
	if row.ReadyAt != nil {
		notReady = store.CloudUnreachable
	}
	refuse := func(reason string) store.CloudCheck { return store.CloudCheck{State: notReady, Reason: reason} }

	switch {
	case errors.Is(err, aws.ErrAssumeDenied):
		return refuse("The control plane cannot assume the role in your AWS account. Rerun the connect command, or check that the stack still exists.")
	case err != nil:
		return refuse("The control plane could not check your AWS account. Try Verify again in a few minutes.")
	case in.AccountID != row.AccountID:
		return refuse("The role belongs to a different AWS account than the one you connected. Rerun the connect command in the right account.")
	}
	switch in.StackStatus {
	case "CREATE_COMPLETE", "UPDATE_COMPLETE", "UPDATE_ROLLBACK_COMPLETE":
	default:
		if strings.HasSuffix(in.StackStatus, "_IN_PROGRESS") {
			return refuse("The stack in your AWS account is still being created. Verify again when it finishes.")
		}

		return refuse("The stack in your AWS account is not usable (" + in.StackStatus + "). Delete it, then rerun the connect command.")
	}
	if len(in.MissingRoles) > 0 {
		return refuse("The stack is missing these roles: " + strings.Join(in.MissingRoles, ", ") + ". Rerun the connect command to repair it.")
	}
	if in.TemplateVersion < CloudTemplateVersion {
		return store.CloudCheck{State: store.CloudOutOfDate, TemplateVersion: in.TemplateVersion, Reason: "The stack uses an older template. Rerun the connect command to update it."}
	}

	return store.CloudCheck{State: store.CloudReady, TemplateVersion: in.TemplateVersion}
}

func randomHex(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}

	return hex.EncodeToString(raw), nil
}
