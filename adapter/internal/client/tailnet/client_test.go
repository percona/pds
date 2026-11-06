package tailnet_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/tailnet"
	"github.com/percona/pds/adapter/internal/client/tailnet/tailnettest"
)

const (
	envTag     = "tag:pds-environment"
	serviceTag = "tag:pds-support"
)

func newClient(t *testing.T, fake *tailnettest.Server) *tailnet.Client {
	t.Helper()

	client, err := tailnet.New(tailnet.Options{
		BaseURL:             fake.URL,
		Tailnet:             tailnettest.Tailnet,
		Domain:              tailnettest.Domain,
		KeyClientID:         tailnettest.KeyClientID,
		KeyClientSecret:     tailnettest.KeyClientSecret,
		ServiceClientID:     tailnettest.ServiceClientID,
		ServiceClientSecret: tailnettest.ServiceClientSecret,
		EnvironmentTag:      envTag,
		ServiceTag:          serviceTag,
	})
	require.NoError(t, err)

	return client
}

func TestMintedKeyIsSingleUseShortLivedAndTaggedForEnvironments(t *testing.T) {
	fake := tailnettest.New(t)
	client := newClient(t, fake)

	id, secret, err := client.MintKey(t.Context(), 5*time.Minute)

	require.NoError(t, err)
	assert.NotEmpty(t, id)
	assert.NotEmpty(t, secret)
	require.Len(t, fake.Keys(), 1)
	key := fake.Keys()[0]
	assert.Equal(t, []string{envTag}, key.Tags)
	assert.False(t, key.Reusable)
	assert.True(t, key.Ephemeral)
	assert.True(t, key.Preauthorized)
	assert.Equal(t, 300, key.ExpirySeconds)
}

func TestRevokedKeyIsGoneAndRevokingAgainIsQuiet(t *testing.T) {
	fake := tailnettest.New(t)
	client := newClient(t, fake)
	id, _, err := client.MintKey(t.Context(), time.Minute)
	require.NoError(t, err)

	require.NoError(t, client.RevokeKey(t.Context(), id))
	require.NoError(t, client.RevokeKey(t.Context(), id))

	assert.True(t, fake.Keys()[0].Revoked)
}

func TestDeletingDevicesRemovesOnlyThoseNamedForTheWindow(t *testing.T) {
	fake := tailnettest.New(t)
	fake.AddDevice("d1", "win_a", envTag)
	fake.AddDevice("d2", "win_b", envTag)
	fake.AddDevice("d3", "win_a", envTag)
	client := newClient(t, fake)

	require.NoError(t, client.DeleteDevices(t.Context(), "win_a"))

	assert.Equal(t, []tailnettest.Device{{ID: "d2", Hostname: "win_b", Tags: []string{envTag}}}, fake.Devices())
}

func TestDeletingDevicesOfAWindowWithNoneSucceeds(t *testing.T) {
	fake := tailnettest.New(t)
	client := newClient(t, fake)

	require.NoError(t, client.DeleteDevices(t.Context(), "win_none"))
}

func TestCreatedAddressCarriesTheServiceTagAndThePort(t *testing.T) {
	fake := tailnettest.New(t)
	client := newClient(t, fake)

	require.NoError(t, client.CreateAddress(t.Context(), "svc:pds-abc"))

	assert.Equal(t, []tailnettest.Service{{Name: "svc:pds-abc", Tags: []string{serviceTag}, Ports: []string{"tcp:443"}}},
		fake.Services())
	require.NoError(t, client.CheckAddress(t.Context(), "svc:pds-abc"))
}

func TestAddressWithAnotherTagOrPortFailsTheCheck(t *testing.T) {
	cases := map[string]tailnettest.Service{
		"extra tag":   {Name: "svc:a", Tags: []string{serviceTag, "tag:other"}, Ports: []string{"tcp:443"}},
		"wrong tag":   {Name: "svc:a", Tags: []string{"tag:other"}, Ports: []string{"tcp:443"}},
		"no tag":      {Name: "svc:a", Ports: []string{"tcp:443"}},
		"extra port":  {Name: "svc:a", Tags: []string{serviceTag}, Ports: []string{"tcp:443", "tcp:22"}},
		"wrong port":  {Name: "svc:a", Tags: []string{serviceTag}, Ports: []string{"tcp:22"}},
		"all ports":   {Name: "svc:a", Tags: []string{serviceTag}, Ports: []string{"do-not-use-this-port"}},
		"no port set": {Name: "svc:a", Tags: []string{serviceTag}},
	}
	for name, svc := range cases {
		t.Run(name, func(t *testing.T) {
			fake := tailnettest.New(t)
			fake.SetService(svc)

			err := newClient(t, fake).CheckAddress(t.Context(), "svc:a")

			require.ErrorIs(t, err, tailnet.ErrAddressPolicy)
		})
	}
}

func TestMissingAddressFailsTheCheck(t *testing.T) {
	fake := tailnettest.New(t)

	err := newClient(t, fake).CheckAddress(t.Context(), "svc:gone")

	require.ErrorIs(t, err, tailnet.ErrAddressPolicy)
}

func TestTailnetFailureIsAnErrorThatNamesNoCredential(t *testing.T) {
	fake := tailnettest.New(t)
	client := newClient(t, fake)
	fake.Fail()

	_, secret, err := client.MintKey(t.Context(), time.Minute)

	require.Error(t, err)
	assert.Empty(t, secret)
	assert.NotContains(t, err.Error(), tailnettest.KeyClientSecret)
	assert.NotContains(t, err.Error(), tailnettest.ServiceClientSecret)
}

func TestBadCredentialsAreRefusedAtTheTokenEndpoint(t *testing.T) {
	fake := tailnettest.New(t)
	client, err := tailnet.New(tailnet.Options{
		BaseURL: fake.URL, Tailnet: tailnettest.Tailnet, Domain: tailnettest.Domain,
		KeyClientID: tailnettest.KeyClientID, KeyClientSecret: "wrong",
		ServiceClientID: tailnettest.ServiceClientID, ServiceClientSecret: tailnettest.ServiceClientSecret,
		EnvironmentTag: envTag, ServiceTag: serviceTag,
	})
	require.NoError(t, err)

	_, _, err = client.MintKey(t.Context(), time.Minute)

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "wrong")
}

func TestNewRefusesBlankSettings(t *testing.T) {
	valid := tailnet.Options{
		BaseURL: "https://api.example", Tailnet: "example.com", Domain: "tail1234.ts.net",
		KeyClientID: "a", KeyClientSecret: "b", ServiceClientID: "c", ServiceClientSecret: "d",
		EnvironmentTag: envTag, ServiceTag: serviceTag,
	}
	_, err := tailnet.New(valid)
	require.NoError(t, err)

	blanks := map[string]func(*tailnet.Options){
		"tailnet":               func(o *tailnet.Options) { o.Tailnet = " " },
		"key client id":         func(o *tailnet.Options) { o.KeyClientID = "" },
		"key client secret":     func(o *tailnet.Options) { o.KeyClientSecret = "" },
		"service client id":     func(o *tailnet.Options) { o.ServiceClientID = "" },
		"service client secret": func(o *tailnet.Options) { o.ServiceClientSecret = "" },
		"domain":                func(o *tailnet.Options) { o.Domain = "" },
		"domain with a scheme":  func(o *tailnet.Options) { o.Domain = "https://tail1234.ts.net" },
		"domain with a path":    func(o *tailnet.Options) { o.Domain = "tail1234.ts.net/x" },
		"domain of only dots":   func(o *tailnet.Options) { o.Domain = "." },
		"environment tag":       func(o *tailnet.Options) { o.EnvironmentTag = "" },
		"service tag":           func(o *tailnet.Options) { o.ServiceTag = "" },
		"same tag for both":     func(o *tailnet.Options) { o.ServiceTag = o.EnvironmentTag },
		"base url scheme":       func(o *tailnet.Options) { o.BaseURL = "ftp://x" },
	}
	for name, mutate := range blanks {
		t.Run(name, func(t *testing.T) {
			opts := valid
			mutate(&opts)

			_, err := tailnet.New(opts)

			require.Error(t, err)
		})
	}
}

func TestAddressHostIsTheAddressNameUnderTheTailnetDomain(t *testing.T) {
	client := newClient(t, tailnettest.New(t))

	assert.Equal(t, "pds-abc.tail1234.ts.net", client.AddressHost("svc:pds-abc"))
}

func TestAddressWithTheRightTagAndPortInAnyOrderPassesTheCheck(t *testing.T) {
	fake := tailnettest.New(t)
	fake.SetService(tailnettest.Service{Name: "svc:a", Tags: []string{serviceTag}, Ports: []string{"tcp:443"}})

	require.NoError(t, newClient(t, fake).CheckAddress(t.Context(), "svc:a"))
}

func TestListedDevicesAreOnlyThoseCarryingTheEnvironmentTag(t *testing.T) {
	fake := tailnettest.New(t)
	fake.AddDevice("d1", "win_a", envTag)
	fake.AddDevice("d2", "laptop", "tag:other")
	fake.AddDevice("d3", "untagged")
	client := newClient(t, fake)

	devices, err := client.ListDevices(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []tailnet.Device{{ID: "d1", Hostname: "win_a"}}, devices)
}

func TestADeletedDeviceIsGoneAndDeletingItAgainIsQuiet(t *testing.T) {
	fake := tailnettest.New(t)
	fake.AddDevice("d1", "win_a", envTag)
	client := newClient(t, fake)

	require.NoError(t, client.DeleteDevice(t.Context(), "d1"))
	require.NoError(t, client.DeleteDevice(t.Context(), "d1"))

	assert.Empty(t, fake.Devices())
}

func TestListedAddressesAreOnlyThoseCarryingTheServiceTag(t *testing.T) {
	fake := tailnettest.New(t)
	fake.SetService(tailnettest.Service{Name: "svc:pds-a", Tags: []string{serviceTag}, Ports: []string{"tcp:443"}})
	fake.SetService(tailnettest.Service{Name: "svc:someone-elses", Tags: []string{"tag:other"}})
	client := newClient(t, fake)

	names, err := client.ListAddresses(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []string{"svc:pds-a"}, names)
}

func TestADeletedAddressIsGoneAndDeletingItAgainIsQuiet(t *testing.T) {
	fake := tailnettest.New(t)
	fake.SetService(tailnettest.Service{Name: "svc:pds-a", Tags: []string{serviceTag}, Ports: []string{"tcp:443"}})
	client := newClient(t, fake)

	require.NoError(t, client.DeleteAddress(t.Context(), "svc:pds-a"))
	require.NoError(t, client.DeleteAddress(t.Context(), "svc:pds-a"))

	assert.Empty(t, fake.Services())
}

func addressWithDevice(t *testing.T) (*tailnettest.Server, *tailnet.Client) {
	t.Helper()

	fake := tailnettest.New(t)
	fake.SetService(tailnettest.Service{Name: "svc:a", Tags: []string{serviceTag}, Ports: []string{"tcp:443"}})
	fake.AddDevice("d1", "win_a", envTag)

	return fake, newClient(t, fake)
}

func TestAnAddressIsApprovedWhenTheWindowsDeviceMayHostIt(t *testing.T) {
	approvals := map[string]tailnettest.Approval{
		"by the policy": {AutoApproved: true},
		"by a person":   {Approved: true},
	}
	for name, approval := range approvals {
		t.Run(name, func(t *testing.T) {
			fake, client := addressWithDevice(t)
			fake.Approve("svc:a", "d1", approval)

			approved, err := client.AddressApproved(t.Context(), "svc:a", "win_a")

			require.NoError(t, err)
			assert.True(t, approved)
		})
	}
}

func TestAnAddressIsNotApprovedForADeviceNobodyHasApproved(t *testing.T) {
	_, client := addressWithDevice(t)

	approved, err := client.AddressApproved(t.Context(), "svc:a", "win_a")

	require.NoError(t, err)
	assert.False(t, approved)
}

func TestAnAddressIsNotApprovedForAnotherWindowsDevice(t *testing.T) {
	fake, client := addressWithDevice(t)
	fake.AddDevice("d2", "win_b", envTag)
	fake.Approve("svc:a", "d2", tailnettest.Approval{AutoApproved: true})

	approved, err := client.AddressApproved(t.Context(), "svc:a", "win_a")

	require.NoError(t, err)
	assert.False(t, approved)
}

func TestAnAddressIsNotApprovedOnceTheWindowsDeviceIsDeleted(t *testing.T) {
	fake, client := addressWithDevice(t)
	fake.Approve("svc:a", "d1", tailnettest.Approval{AutoApproved: true})
	require.NoError(t, client.DeleteDevices(t.Context(), "win_a"))

	approved, err := client.AddressApproved(t.Context(), "svc:a", "win_a")

	require.NoError(t, err)
	assert.False(t, approved)
}

func TestAnAddressThatIsGoneIsNotApproved(t *testing.T) {
	fake := tailnettest.New(t)
	fake.AddDevice("d1", "win_a", envTag)

	approved, err := newClient(t, fake).AddressApproved(t.Context(), "svc:gone", "win_a")

	require.NoError(t, err)
	assert.False(t, approved)
}

func TestAnApprovalCheckAgainstADownTailnetIsAnErrorNotAnAnswer(t *testing.T) {
	fake, client := addressWithDevice(t)
	fake.Approve("svc:a", "d1", tailnettest.Approval{AutoApproved: true})
	fake.Fail()

	_, err := client.AddressApproved(t.Context(), "svc:a", "win_a")

	require.Error(t, err)
}
