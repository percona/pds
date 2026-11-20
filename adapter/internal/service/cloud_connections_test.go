package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
	"github.com/percona/pds/adapter/internal/testutil"
)

const testPrincipal = "arn:aws:iam::111122223333:role/pds-control-plane"

// fakeInspector answers every inspection with what the test set, and keeps
// the requests.
type fakeInspector struct {
	result   aws.Inspection
	err      error
	requests []aws.InspectRequest
}

func (f *fakeInspector) Inspect(_ context.Context, in aws.InspectRequest) (aws.Inspection, error) {
	f.requests = append(f.requests, in)

	return f.result, f.err
}

type cloudFixture struct {
	svc       *service.CloudConnections
	store     *testutil.CloudStore
	inspector *fakeInspector
	scope     auth.Scope
	now       time.Time
}

func newCloudFixture(t *testing.T) *cloudFixture {
	t.Helper()

	f := &cloudFixture{
		store:     testutil.NewCloudStore(),
		inspector: &fakeInspector{},
		scope:     auth.Scope{UserID: 42, Role: auth.RoleUser, Team: auth.Team{ID: 7, Name: "default"}},
		now:       time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC),
	}
	f.svc = service.NewCloudConnections(f.store, f.inspector, service.CloudOptions{
		PrincipalARN: testPrincipal, Now: func() time.Time { return f.now },
	})

	return f
}

// connect issues a token and enrolls an account with it, the way the script does.
func (f *cloudFixture) connect(t *testing.T, account string) (string, service.CloudEnrollment) {
	t.Helper()

	issued, err := f.svc.IssueToken(t.Context(), f.scope, []string{"eu-west-1"})
	require.NoError(t, err)
	enrolled, err := f.svc.Enroll(t.Context(), issued.Token, service.CloudAccount{ID: account, Alias: "acme", Region: "eu-west-1"})
	require.NoError(t, err)

	return issued.Token, enrolled
}

func (f *cloudFixture) healthy(connection service.CloudEnrollment) {
	f.inspector.result = aws.Inspection{
		AccountID: "123456789012", StackStatus: "CREATE_COMPLETE", TemplateVersion: service.CloudTemplateVersion,
	}
}

func TestAnIssuedTokenIsOneHourAndOnlyItsHashIsKept(t *testing.T) {
	f := newCloudFixture(t)

	issued, err := f.svc.IssueToken(t.Context(), f.scope, []string{"eu-west-1", "us-east-1"})
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(issued.Token, "ccn_"))
	assert.Equal(t, f.now.Add(time.Hour), issued.ExpiresAt)
	assert.Equal(t, []string{"eu-west-1", "us-east-1"}, issued.AllowedRegions)
	assert.NotContains(t, f.store.Tokens, issued.Token)
	assert.Contains(t, f.store.Tokens, auth.HashToken(issued.Token))
}

func TestATokenNeedsRegionsThatLookLikeRegions(t *testing.T) {
	f := newCloudFixture(t)

	for _, regions := range [][]string{nil, {}, {"Europe"}, {"eu-west-1", ""}, {"eu-west-1; rm -rf"}} {
		_, err := f.svc.IssueToken(t.Context(), f.scope, regions)
		assert.ErrorIs(t, err, service.ErrCloudInvalid, "%v", regions)
	}
}

func TestEnrollingNamesTheStackTheRolesAndTheSecretTheScriptNeeds(t *testing.T) {
	f := newCloudFixture(t)

	_, got := f.connect(t, "123456789012")

	assert.Len(t, got.ConnectionID, 12)
	assert.Equal(t, "pds-"+got.ConnectionID, got.StackName)
	assert.GreaterOrEqual(t, len(got.ExternalID), 32)
	assert.Equal(t, testPrincipal, got.PrincipalARN)
	assert.Equal(t, []string{"eu-west-1"}, got.AllowedRegions)
	assert.Equal(t, service.CloudTemplateVersion, got.TemplateVersion)
	assert.Equal(t, "arn:aws:iam::123456789012:role/pds/"+got.ConnectionID+"/pds-"+got.ConnectionID+"-control-plane", got.ControlPlaneRoleARN)
}

func TestEnrollingAgainReturnsTheSameSecretAndStack(t *testing.T) {
	f := newCloudFixture(t)
	token, first := f.connect(t, "123456789012")

	again, err := f.svc.Enroll(t.Context(), token, service.CloudAccount{ID: "123456789012", Alias: "acme", Region: "eu-west-1"})
	require.NoError(t, err)

	assert.Equal(t, first, again)
}

func TestEnrollingRefusesWhatCannotBeAnAccount(t *testing.T) {
	f := newCloudFixture(t)
	issued, err := f.svc.IssueToken(t.Context(), f.scope, []string{"eu-west-1"})
	require.NoError(t, err)

	for _, account := range []service.CloudAccount{
		{ID: "1234", Region: "eu-west-1"},
		{ID: "12345678901x", Region: "eu-west-1"},
		{ID: "123456789012", Region: "mars"},
		{ID: "123456789012", Region: "eu-west-1", Alias: strings.Repeat("a", 65)},
		{ID: "123456789012", Region: "eu-west-1", Alias: "a\nb"},
	} {
		_, err := f.svc.Enroll(t.Context(), issued.Token, account)
		assert.ErrorIs(t, err, service.ErrCloudInvalid, "%+v", account)
	}
}

func TestEnrollingWithAWrongTokenIsRefused(t *testing.T) {
	f := newCloudFixture(t)

	_, err := f.svc.Enroll(t.Context(), "ccn_nope", service.CloudAccount{ID: "123456789012", Region: "eu-west-1"})

	assert.ErrorIs(t, err, store.ErrCloudTokenInvalid)
}

func TestWithoutAControlPlanePrincipalNothingIsIssued(t *testing.T) {
	svc := service.NewCloudConnections(testutil.NewCloudStore(), &fakeInspector{}, service.CloudOptions{})

	_, err := svc.IssueToken(t.Context(), auth.Scope{Team: auth.Team{ID: 1}}, []string{"eu-west-1"})

	assert.ErrorIs(t, err, service.ErrCloudUnavailable)
}

func TestAnAccountThatPassesTheRoleCheckIsReadyAndTheTokenStopsWorking(t *testing.T) {
	f := newCloudFixture(t)
	token, got := f.connect(t, "123456789012")
	f.healthy(got)

	row, err := f.svc.VerifyWithToken(t.Context(), token)
	require.NoError(t, err)

	assert.Equal(t, store.CloudReady, row.State)
	_, err = f.svc.VerifyWithToken(t.Context(), token)
	assert.ErrorIs(t, err, store.ErrCloudTokenInvalid)
	require.Len(t, f.inspector.requests, 1)
	request := f.inspector.requests[0]
	assert.Equal(t, got.ControlPlaneRoleARN, request.RoleARN)
	assert.Equal(t, got.ExternalID, request.ExternalID)
	assert.Equal(t, got.StackName, request.StackName)
	assert.Equal(t, "eu-west-1", request.Region)
	assert.Equal(t, []string{
		"pds-" + got.ConnectionID + "-cloudformation",
		"pds-" + got.ConnectionID + "-eks-cluster",
		"pds-" + got.ConnectionID + "-node",
		"pds-" + got.ConnectionID + "-ebs-csi",
	}, request.RoleNames)
}

func TestVerifyActsAsTheUserAndTheJobActsAsAJob(t *testing.T) {
	f := newCloudFixture(t)
	_, got := f.connect(t, "123456789012")
	f.healthy(got)

	_, err := f.svc.Verify(t.Context(), f.scope, got.ConnectionID)
	require.NoError(t, err)
	require.NoError(t, f.svc.CheckAll(t.Context()))

	require.Len(t, f.inspector.requests, 2)
	assert.Equal(t, "pds-user-42", f.inspector.requests[0].SourceIdentity)
	assert.Equal(t, "pds-job-hourly", f.inspector.requests[1].SourceIdentity)
}

func TestEverySourceIdentityMatchesTheTrustPolicyPatterns(t *testing.T) {
	// The trust policy allows pds-user-* and pds-job-*, and AWS needs six
	// literal characters before a wildcard and only [\w+=,.@-] anywhere.
	for _, id := range []string{service.UserSourceIdentity(42), service.JobSourceIdentity("hourly")} {
		assert.Regexp(t, `^pds-(user|job)-[\w+=,.@-]+$`, id)
		assert.LessOrEqual(t, len(id), 64)
	}
}

func TestACheckExplainsEachWayAnAccountIsNotReady(t *testing.T) {
	tests := []struct {
		name       string
		inspection aws.Inspection
		err        error
		wantState  string
		wantReason string
	}{
		{"role refused", aws.Inspection{}, aws.ErrAssumeDenied, store.CloudPending, "role"},
		{"aws unreachable", aws.Inspection{}, errors.New("dial tcp: i/o timeout"), store.CloudPending, "could not check"},
		{"other account", aws.Inspection{AccountID: "999999999999", StackStatus: "CREATE_COMPLETE", TemplateVersion: service.CloudTemplateVersion}, nil, store.CloudPending, "different AWS account"},
		{"stack still creating", aws.Inspection{AccountID: "123456789012", StackStatus: "CREATE_IN_PROGRESS", TemplateVersion: service.CloudTemplateVersion}, nil, store.CloudPending, "still being created"},
		{"stack rolled back", aws.Inspection{AccountID: "123456789012", StackStatus: "ROLLBACK_COMPLETE", TemplateVersion: service.CloudTemplateVersion}, nil, store.CloudPending, "stack"},
		{"role missing", aws.Inspection{AccountID: "123456789012", StackStatus: "UPDATE_COMPLETE", TemplateVersion: service.CloudTemplateVersion, MissingRoles: []string{"pds-x-node"}}, nil, store.CloudPending, "pds-x-node"},
		{"old template", aws.Inspection{AccountID: "123456789012", StackStatus: "UPDATE_COMPLETE", TemplateVersion: service.CloudTemplateVersion - 1}, nil, store.CloudOutOfDate, "Rerun"},
		{"healthy after rollback of an update", aws.Inspection{AccountID: "123456789012", StackStatus: "UPDATE_ROLLBACK_COMPLETE", TemplateVersion: service.CloudTemplateVersion}, nil, store.CloudReady, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCloudFixture(t)
			_, got := f.connect(t, "123456789012")
			f.inspector.result, f.inspector.err = tt.inspection, tt.err

			row, err := f.svc.Verify(t.Context(), f.scope, got.ConnectionID)
			require.NoError(t, err)

			assert.Equal(t, tt.wantState, row.State)
			assert.Contains(t, row.Reason, tt.wantReason)
			assert.NotContains(t, row.Reason, "dial tcp", "an AWS error never reaches the customer")
		})
	}
}

func TestAReadyConnectionThatStopsAnsweringIsUnreachableNotPending(t *testing.T) {
	f := newCloudFixture(t)
	_, got := f.connect(t, "123456789012")
	f.healthy(got)
	_, err := f.svc.Verify(t.Context(), f.scope, got.ConnectionID)
	require.NoError(t, err)

	f.inspector.result, f.inspector.err = aws.Inspection{}, aws.ErrAssumeDenied
	row, err := f.svc.Verify(t.Context(), f.scope, got.ConnectionID)
	require.NoError(t, err)

	assert.Equal(t, store.CloudUnreachable, row.State)
}

func TestAnotherTeamCannotVerifyOrCancelAConnection(t *testing.T) {
	f := newCloudFixture(t)
	_, got := f.connect(t, "123456789012")
	other := auth.Scope{UserID: 9, Role: auth.RoleUser, Team: auth.Team{ID: 8}}

	_, err := f.svc.Verify(t.Context(), other, got.ConnectionID)
	assert.ErrorIs(t, err, store.ErrCloudConnectionUnknown)
	assert.ErrorIs(t, f.svc.Cancel(t.Context(), other, got.ConnectionID), store.ErrCloudConnectionUnknown)
	assert.Empty(t, f.inspector.requests)
}

func TestScriptEventsAreRecordedAgainstTheTokensConnection(t *testing.T) {
	f := newCloudFixture(t)
	token, _ := f.connect(t, "123456789012")

	require.NoError(t, f.svc.RecordEvent(t.Context(), token, service.CloudStep{Step: "stack", Status: store.CloudStepStarted}))
	require.NoError(t, f.svc.RecordEvent(t.Context(), token, service.CloudStep{Step: "stack", Status: store.CloudStepFailed, Detail: strings.Repeat("x", 600)}))

	require.Len(t, f.store.Reported, 2)
	assert.Len(t, f.store.Reported[1].Detail, 512, "a long detail is cut, not refused")

	for _, bad := range []service.CloudStep{{Step: "", Status: "started"}, {Step: "stack", Status: "done"}, {Step: "st ack", Status: "started"}} {
		assert.ErrorIs(t, f.svc.RecordEvent(t.Context(), token, bad), service.ErrCloudInvalid, "%+v", bad)
	}
	assert.ErrorIs(t, f.svc.RecordEvent(t.Context(), "ccn_nope", service.CloudStep{Step: "stack", Status: "started"}), store.ErrCloudTokenInvalid)
}

func TestATokenThatEnrolledNothingCannotReportOrVerify(t *testing.T) {
	f := newCloudFixture(t)
	issued, err := f.svc.IssueToken(t.Context(), f.scope, []string{"eu-west-1"})
	require.NoError(t, err)

	assert.ErrorIs(t, f.svc.RecordEvent(t.Context(), issued.Token, service.CloudStep{Step: "stack", Status: "started"}), store.ErrCloudTokenInvalid)
	_, err = f.svc.VerifyWithToken(t.Context(), issued.Token)
	assert.ErrorIs(t, err, store.ErrCloudTokenInvalid)
}

func TestTheHourlyCheckVisitsEveryConnectionAndOneFailureDoesNotStopTheRest(t *testing.T) {
	f := newCloudFixture(t)
	f.connect(t, "123456789012")
	f.connect(t, "210987654321")
	f.inspector.err = aws.ErrAssumeDenied

	require.NoError(t, f.svc.CheckAll(t.Context()))

	assert.Len(t, f.inspector.requests, 2)
}

func TestAScriptMayNarrowTheTokensRegionsButNeverWidenThem(t *testing.T) {
	f := newCloudFixture(t)
	issued, err := f.svc.IssueToken(t.Context(), f.scope, []string{"eu-west-1", "us-east-1"})
	require.NoError(t, err)

	narrowed, err := f.svc.Enroll(t.Context(), issued.Token, service.CloudAccount{ID: "123456789012", Region: "eu-west-1", Regions: []string{"us-east-1"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"us-east-1"}, narrowed.AllowedRegions)

	_, err = f.svc.Enroll(t.Context(), issued.Token, service.CloudAccount{ID: "123456789012", Region: "eu-west-1", Regions: []string{"eu-central-1"}})
	assert.ErrorIs(t, err, service.ErrCloudInvalid)
}

type environmentsOf []store.ManagedEnvironment

func (e environmentsOf) List(context.Context, uint) ([]store.ManagedEnvironment, error) {
	return e, nil
}

func TestAnIssuedTokenHasAPublicIDThatIsNotTheToken(t *testing.T) {
	f := newCloudFixture(t)

	issued, err := f.svc.IssueToken(t.Context(), f.scope, []string{"eu-west-1"})
	require.NoError(t, err)

	assert.Equal(t, auth.HashToken(issued.Token)[:16], issued.ID)
	assert.NotContains(t, issued.Token, issued.ID)
}

func TestCancellingATokenRevokesItAndDropsTheConnectionItStarted(t *testing.T) {
	f := newCloudFixture(t)
	issued, err := f.svc.IssueToken(t.Context(), f.scope, []string{"eu-west-1"})
	require.NoError(t, err)
	enrolled, err := f.svc.Enroll(t.Context(), issued.Token, service.CloudAccount{ID: "123456789012", Region: "eu-west-1"})
	require.NoError(t, err)

	require.NoError(t, f.svc.CancelToken(t.Context(), f.scope, issued.ID))

	_, err = f.svc.Get(t.Context(), f.scope, enrolled.ConnectionID)
	require.ErrorIs(t, err, store.ErrCloudConnectionUnknown)
	_, err = f.svc.Enroll(t.Context(), issued.Token, service.CloudAccount{ID: "123456789012", Region: "eu-west-1"})
	require.ErrorIs(t, err, store.ErrCloudTokenInvalid)
}

func TestCancellingATokenOfAnotherTeamIsRefusedAsUnknown(t *testing.T) {
	f := newCloudFixture(t)
	issued, err := f.svc.IssueToken(t.Context(), f.scope, []string{"eu-west-1"})
	require.NoError(t, err)

	other := f.scope
	other.Team = auth.Team{ID: 8}

	require.ErrorIs(t, f.svc.CancelToken(t.Context(), other, issued.ID), store.ErrCloudTokenInvalid)
}

func TestCancellingATokenWhoseConnectionIsReadyKeepsTheConnection(t *testing.T) {
	f := newCloudFixture(t)
	issued, err := f.svc.IssueToken(t.Context(), f.scope, []string{"eu-west-1"})
	require.NoError(t, err)
	enrolled, err := f.svc.Enroll(t.Context(), issued.Token, service.CloudAccount{ID: "123456789012", Region: "eu-west-1"})
	require.NoError(t, err)
	f.healthy(enrolled)
	_, err = f.svc.Verify(t.Context(), f.scope, enrolled.ConnectionID)
	require.NoError(t, err)

	err = f.svc.CancelToken(t.Context(), f.scope, issued.ID)

	require.ErrorIs(t, err, store.ErrCloudTokenInvalid, "the connection's tokens ended when it first became ready")
	_, err = f.svc.Get(t.Context(), f.scope, enrolled.ConnectionID)
	require.NoError(t, err)
}

func TestTheTypedAccountPathMakesAPendingConnectionAndLeavesNoLiveToken(t *testing.T) {
	f := newCloudFixture(t)

	first, err := f.svc.CreateTyped(t.Context(), f.scope, "123456789012", []string{"eu-west-1", "us-east-1"})
	require.NoError(t, err)
	again, err := f.svc.CreateTyped(t.Context(), f.scope, "123456789012", []string{"eu-west-1"})
	require.NoError(t, err)

	assert.Equal(t, first.ConnectionID, again.ConnectionID, "the same account in the same Team is the same connection")
	assert.Equal(t, first.ExternalID, again.ExternalID)
	row, err := f.svc.Get(t.Context(), f.scope, first.ConnectionID)
	require.NoError(t, err)
	assert.Equal(t, store.CloudPending, row.State)
	assert.Equal(t, "eu-west-1", row.StackRegion)
	for hash := range f.store.Tokens {
		_, err := f.store.TokenTeam(t.Context(), hash, f.now)
		assert.ErrorIs(t, err, store.ErrCloudTokenInvalid, "the helper token is revoked at once")
	}
}

func TestTheTypedAccountPathRefusesWhatCannotBeAnAccount(t *testing.T) {
	f := newCloudFixture(t)

	_, err := f.svc.CreateTyped(t.Context(), f.scope, "not-an-account", []string{"eu-west-1"})

	require.ErrorIs(t, err, service.ErrCloudInvalid)
}

func TestRemovingAConnectionNeedsItsTypedNameAndNoManagedEnvironments(t *testing.T) {
	f := newCloudFixture(t)
	_, enrolled := f.connect(t, "123456789012")
	f.svc = service.NewCloudConnections(f.store, f.inspector, service.CloudOptions{
		PrincipalARN: testPrincipal, Now: func() time.Time { return f.now },
		Environments: environmentsOf{{Name: "orders", ConnectionID: enrolled.ConnectionID}},
	})

	require.ErrorIs(t, f.svc.Remove(t.Context(), f.scope, enrolled.ConnectionID, "wrong"), service.ErrCloudConfirm)
	require.ErrorIs(t, f.svc.Remove(t.Context(), f.scope, enrolled.ConnectionID, "acme"), service.ErrCloudInUse)

	f.svc = service.NewCloudConnections(f.store, f.inspector, service.CloudOptions{
		PrincipalARN: testPrincipal, Now: func() time.Time { return f.now }, Environments: environmentsOf{},
	})
	require.NoError(t, f.svc.Remove(t.Context(), f.scope, enrolled.ConnectionID, "acme"))
	_, err := f.svc.Get(t.Context(), f.scope, enrolled.ConnectionID)
	require.ErrorIs(t, err, store.ErrCloudConnectionUnknown)
}

func TestAConnectionWithNoAliasIsRemovedByTypingItsAccountID(t *testing.T) {
	f := newCloudFixture(t)
	issued, err := f.svc.IssueToken(t.Context(), f.scope, []string{"eu-west-1"})
	require.NoError(t, err)
	enrolled, err := f.svc.Enroll(t.Context(), issued.Token, service.CloudAccount{ID: "123456789012", Region: "eu-west-1"})
	require.NoError(t, err)

	require.ErrorIs(t, f.svc.Remove(t.Context(), f.scope, enrolled.ConnectionID, "acme"), service.ErrCloudConfirm)
	require.NoError(t, f.svc.Remove(t.Context(), f.scope, enrolled.ConnectionID, "123456789012"))
}
