package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/percona/pds/adapter/internal/agent"
	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/client/kube/kubetest"
	"github.com/percona/pds/adapter/internal/hub"
)

const (
	supportNamespace = "pds-support"
	windowOne        = "win_0123456789abcdef"
	oneTimeKey       = "tskey-auth-kSecret123-NEVERLOGTHIS"
	supportAddress   = "svc:pds-7f3a"
	supportHost      = "pds-7f3a.tail1234.ts.net"
)

// scriptedHub answers each heartbeat with the next reply it was given, and the
// last one again after that. It keeps every beat it was sent.
type scriptedHub struct {
	srv     *httptest.Server
	mu      sync.Mutex
	replies []string
	beats   []hub.Heartbeat
}

func newScriptedHub(t *testing.T, replies ...string) *scriptedHub {
	t.Helper()

	h := &scriptedHub{replies: replies}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		var beat hub.Heartbeat
		assert.NoError(t, json.Unmarshal(body, &beat))

		h.mu.Lock()
		reply := `{"work":[]}`
		if n := len(h.beats); n < len(h.replies) {
			reply = h.replies[n]
		}
		h.beats = append(h.beats, beat)
		h.mu.Unlock()

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(h.srv.Close)

	return h
}

func (h *scriptedHub) beat(i int) (hub.Heartbeat, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i >= len(h.beats) {
		return hub.Heartbeat{}, false
	}

	return h.beats[i], true
}

func openWork(level string) string {
	item := hub.Work{
		ID: "wrk_open", Kind: hub.WorkSupportOpen, Window: windowOne, Level: level,
		ExpiresAt: time.Now().Add(4 * time.Hour).UTC(), Address: supportAddress, AddressHost: supportHost, Key: oneTimeKey,
	}
	raw, _ := json.Marshal(hub.HeartbeatReply{Work: []hub.Work{item}})

	return string(raw)
}

func closeWork() string {
	raw, _ := json.Marshal(hub.HeartbeatReply{Work: []hub.Work{{ID: "wrk_close", Kind: hub.WorkSupportClose, Window: windowOne}}})

	return string(raw)
}

// runAgent runs the agent against the hub until it has sent beats beats.
func runSupportAgent(t *testing.T, h *scriptedHub, k *kube.Client, beats int) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- agent.Run(ctx, agent.Options{
			HubURL: h.srv.URL, Token: "secret", Environment: "spoke-a", Namespace: "pds",
			Interval: 5 * time.Millisecond, Support: k,
		})
	}()
	require.Eventually(t, func() bool { _, ok := h.beat(beats - 1); return ok }, 5*time.Second, 5*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
}

func TestAnOpenWorkItemInstallsTheWorkloadForThatWindow(t *testing.T) {
	clientset := fake.NewClientset()
	h := newScriptedHub(t, openWork("readOnly"))

	runSupportAgent(t, h, kube.New(clientset), 3)

	sa, err := clientset.CoreV1().ServiceAccounts(supportNamespace).Get(t.Context(), "pds-support-proxy", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, windowOne, sa.Labels["pds.percona.com/support-window"])
	for _, name := range []string{"pds-support-config", "pds-support-state"} {
		secret, err := clientset.CoreV1().Secrets(supportNamespace).Get(t.Context(), name, metav1.GetOptions{})
		require.NoError(t, err, name)
		assert.Equal(t, windowOne, secret.Labels["pds.percona.com/support-window"], name)
	}
	state, _ := clientset.CoreV1().Secrets(supportNamespace).Get(t.Context(), "pds-support-state", metav1.GetOptions{})
	cert, err := clientset.CoreV1().Secrets(supportNamespace).Get(t.Context(), supportHost, metav1.GetOptions{})
	require.NoError(t, err)
	assert.NotContains(t, cert.Labels, "pds.percona.com/support-window", "the certificate belongs to the address")
	assert.Empty(t, state.Data, "the proxy writes its own state")
	assert.Empty(t, cert.Data, "the certificate Secret starts empty")

	deploy, err := clientset.AppsV1().Deployments(supportNamespace).Get(t.Context(), "pds-support-proxy", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), *deploy.Spec.Replicas)
	assert.Equal(t, "pds-support-proxy", deploy.Spec.Template.Spec.ServiceAccountName)
	assert.NotEmpty(t, deploy.Spec.Template.Spec.Containers[0].Image)
}

func TestTheKeyAndAddressAreInTheConfigSecretAndNowhereElse(t *testing.T) {
	clientset := fake.NewClientset()
	h := newScriptedHub(t, openWork("readOnly"))

	runSupportAgent(t, h, kube.New(clientset), 3)

	config, err := clientset.CoreV1().Secrets(supportNamespace).Get(t.Context(), "pds-support-config", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, string(config.Data["config.json"]), oneTimeKey)
	assert.Contains(t, string(config.Data["config.json"]), supportAddress)

	everything := dumpObjects(t, clientset)
	assert.Equal(t, 1, strings.Count(everything, oneTimeKey), "only the config Secret holds the key")
	for i := range 3 {
		beat, ok := h.beat(i)
		require.True(t, ok)
		raw, _ := json.Marshal(beat)
		assert.NotContains(t, string(raw), oneTimeKey, "the key never goes back to the hub")
	}
}

func dumpObjects(t *testing.T, clientset *fake.Clientset) string {
	t.Helper()

	var sb strings.Builder
	secrets, _ := clientset.CoreV1().Secrets(supportNamespace).List(t.Context(), metav1.ListOptions{})
	deployments, _ := clientset.AppsV1().Deployments(supportNamespace).List(t.Context(), metav1.ListOptions{})
	accounts, _ := clientset.CoreV1().ServiceAccounts(supportNamespace).List(t.Context(), metav1.ListOptions{})
	bindings, _ := clientset.RbacV1().ClusterRoleBindings().List(t.Context(), metav1.ListOptions{})
	for _, list := range []any{secrets, deployments, accounts, bindings} {
		raw, err := json.Marshal(list)
		require.NoError(t, err)
		sb.Write(raw)
	}
	// A Secret's data is base64 in JSON, so the plain text is added for the
	// match.
	for _, secret := range secrets.Items {
		for _, value := range secret.Data {
			sb.Write(value)
		}
	}

	return sb.String()
}

func bindingFor(t *testing.T, clientset *fake.Clientset) *rbacv1.ClusterRoleBinding {
	t.Helper()

	list, err := clientset.RbacV1().ClusterRoleBindings().List(t.Context(), metav1.ListOptions{
		LabelSelector: "pds.percona.com/support-window=" + windowOne,
	})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)

	return &list.Items[0]
}

func TestEachWindowGetsItsOwnBindingToTheRoleOfItsLevel(t *testing.T) {
	for level, role := range map[string]string{"readOnly": "pds-support-read", "admin": "pds-support-admin"} {
		t.Run(level, func(t *testing.T) {
			clientset := fake.NewClientset()
			h := newScriptedHub(t, openWork(level))

			runSupportAgent(t, h, kube.New(clientset), 2)

			binding := bindingFor(t, clientset)
			assert.Equal(t, rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role}, binding.RoleRef)
			assert.Contains(t, binding.Name, "pds-support-")
			assert.NotContains(t, binding.Name, "_", "the name is a valid object name")
		})
	}
}

func TestALevelTheAgentDoesNotKnowBindsNothingAndFails(t *testing.T) {
	clientset := fake.NewClientset()
	h := newScriptedHub(t, openWork("superuser"))

	runSupportAgent(t, h, kube.New(clientset), 2)

	bindings, err := clientset.RbacV1().ClusterRoleBindings().List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, bindings.Items)
	second, _ := h.beat(1)
	require.NotNil(t, second.Support)
	assert.Equal(t, hub.SupportFailed, second.Support.Confirmations[0].Event)
}

func TestTheNextBeatConfirmsTheInstallAndListsTheWindow(t *testing.T) {
	h := newScriptedHub(t, openWork("readOnly"))

	runSupportAgent(t, h, kube.New(fake.NewClientset()), 3)

	first, _ := h.beat(0)
	require.NotNil(t, first.Support, "an agent that can do support access says what it holds")
	assert.Empty(t, first.Support.Held)
	second, _ := h.beat(1)
	assert.Equal(t, []string{windowOne}, second.Support.Held)
	assert.Equal(t, []hub.WorkResult{{ID: "wrk_open"}}, second.Results)
	assert.Equal(t, []hub.SupportConfirmation{{Window: windowOne, Event: hub.SupportInstalled}}, second.Support.Confirmations)
	third, _ := h.beat(2)
	assert.Empty(t, third.Support.Confirmations, "a confirmation the hub took is not sent again")
	assert.Equal(t, []string{windowOne}, third.Support.Held, "the window is listed on every beat")
}

func TestAWindowWhoseProxyHasNoCertificateIsHeldButNotServing(t *testing.T) {
	h := newScriptedHub(t, openWork("readOnly"))

	runSupportAgent(t, h, kube.New(fake.NewClientset()), 3)

	third, _ := h.beat(2)
	assert.Equal(t, []string{windowOne}, third.Support.Held)
	assert.Empty(t, third.Support.Serving, "the address answers no one until its certificate is issued")
}

func TestAWindowWhoseProxyHoldsItsCertificateIsServing(t *testing.T) {
	cert, key := kubetest.Certificate(t, supportHost, time.Now().Add(60*24*time.Hour))
	issued := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: supportHost, Namespace: supportNamespace},
		Data:       map[string][]byte{corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key},
	}
	h := newScriptedHub(t, openWork("readOnly"))

	runSupportAgent(t, h, kube.New(fake.NewClientset(issued)), 3)

	second, _ := h.beat(1)
	assert.Equal(t, []string{windowOne}, second.Support.Serving)
}

func TestAnInstallThatFailsIsReportedWithItsReasonAndNeverTheKey(t *testing.T) {
	clientset := fake.NewClientset()
	clientset.PrependReactor("create", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("admission webhook refused " + oneTimeKey)
	})
	h := newScriptedHub(t, openWork("readOnly"))

	runSupportAgent(t, h, kube.New(clientset), 2)

	second, _ := h.beat(1)
	require.Len(t, second.Support.Confirmations, 1)
	assert.Equal(t, hub.SupportFailed, second.Support.Confirmations[0].Event)
	assert.Contains(t, second.Support.Confirmations[0].Reason, "admission webhook refused")
	raw, _ := json.Marshal(second)
	assert.NotContains(t, string(raw), oneTimeKey)
	assert.NotEmpty(t, second.Results[0].ID, "the hub is told the item is done with, so it can mint again")
}

func TestInstallingTheSameWindowAgainChangesNothingButTheKey(t *testing.T) {
	clientset := fake.NewClientset()
	k := kube.New(clientset)
	h := newScriptedHub(t, openWork("readOnly"), openWork("readOnly"))

	runSupportAgent(t, h, k, 4)

	bindingFor(t, clientset)
	deployments, err := clientset.AppsV1().Deployments(supportNamespace).List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, deployments.Items, 1)
}

func TestACloseWorkItemRemovesEverythingTheWindowHad(t *testing.T) {
	clientset := fake.NewClientset()
	h := newScriptedHub(t, openWork("readOnly"), closeWork())

	runSupportAgent(t, h, kube.New(clientset), 4)

	assert.NotContains(t, dumpObjects(t, clientset), windowOne, "nothing of the window is left")
	for _, name := range []string{"pds-support-config", "pds-support-state"} {
		_, err := clientset.CoreV1().Secrets(supportNamespace).Get(t.Context(), name, metav1.GetOptions{})
		assert.Error(t, err, name)
	}
	_, err := clientset.CoreV1().ServiceAccounts(supportNamespace).Get(t.Context(), "pds-support-proxy", metav1.GetOptions{})
	assert.Error(t, err)
	_, err = clientset.AppsV1().Deployments(supportNamespace).Get(t.Context(), "pds-support-proxy", metav1.GetOptions{})
	assert.Error(t, err)
	bindings, _ := clientset.RbacV1().ClusterRoleBindings().List(t.Context(), metav1.ListOptions{})
	assert.Empty(t, bindings.Items)

	last, _ := h.beat(3)
	assert.Empty(t, last.Support.Held)
	assert.Contains(t, confirmedEvents(t, h, 4), hub.SupportRemoved)
}

func confirmedEvents(t *testing.T, h *scriptedHub, beats int) []string {
	t.Helper()

	var events []string
	for i := range beats {
		beat, ok := h.beat(i)
		if !ok || beat.Support == nil {
			continue
		}
		for _, c := range beat.Support.Confirmations {
			events = append(events, c.Event)
		}
	}

	return events
}

func TestClosingAWindowThatIsNotThereStillConfirmsTheRemoval(t *testing.T) {
	h := newScriptedHub(t, closeWork())

	runSupportAgent(t, h, kube.New(fake.NewClientset()), 2)

	second, _ := h.beat(1)
	assert.Equal(t, []hub.SupportConfirmation{{Window: windowOne, Event: hub.SupportRemoved}}, second.Support.Confirmations)
}

func TestAnExpiredWorkloadIsRemovedWhileTheHubCannotBeReached(t *testing.T) {
	clientset := fake.NewClientset()
	k := kube.New(clientset)
	require.NoError(t, k.InstallSupportWorkload(t.Context(), kube.SupportWorkload{
		Window: windowOne, Level: "readOnly", Address: supportAddress, Key: oneTimeKey,
		ExpiresAt: time.Now().Add(-time.Minute),
	}))
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(dead.Close)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- agent.Run(ctx, agent.Options{
			HubURL: dead.URL, Token: "secret", Environment: "spoke-a", Namespace: "pds",
			Interval: 5 * time.Millisecond, Support: k,
		})
	}()
	require.Eventually(t, func() bool {
		windows, err := k.SupportWindows(t.Context())
		return err == nil && len(windows) == 0
	}, 5*time.Second, 5*time.Millisecond, "the workload outlived its window")
	cancel()
	require.NoError(t, <-done)
}

func TestAWorkloadStillInsideItsWindowIsLeftAlone(t *testing.T) {
	clientset := fake.NewClientset()
	k := kube.New(clientset)
	require.NoError(t, k.InstallSupportWorkload(t.Context(), kube.SupportWorkload{
		Window: windowOne, Level: "readOnly", Address: supportAddress, Key: oneTimeKey,
		ExpiresAt: time.Now().Add(time.Hour),
	}))
	h := newScriptedHub(t)

	runSupportAgent(t, h, k, 3)

	windows, err := k.SupportWindows(t.Context())
	require.NoError(t, err)
	require.Len(t, windows, 1)
	assert.Equal(t, windowOne, windows[0].ID)
	last, _ := h.beat(2)
	assert.Equal(t, []string{windowOne}, last.Support.Held)
}

func TestAnAgentWithNoKubernetesAccessSaysNothingAboutSupportAccess(t *testing.T) {
	h := newScriptedHub(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- agent.Run(ctx, agent.Options{
			HubURL: h.srv.URL, Token: "secret", Environment: "spoke-a", Namespace: "pds", Interval: 5 * time.Millisecond,
		})
	}()
	require.Eventually(t, func() bool { _, ok := h.beat(0); return ok }, 5*time.Second, 5*time.Millisecond)
	cancel()
	require.NoError(t, <-done)

	first, _ := h.beat(0)
	assert.Nil(t, first.Support, "the hub reads no report as nothing known, not as no windows")
}

func extendWork(until time.Time) string {
	raw, _ := json.Marshal(hub.HeartbeatReply{Work: []hub.Work{
		{ID: "wrk_extend", Kind: hub.WorkSupportExtend, Window: windowOne, ExpiresAt: until},
	}})

	return string(raw)
}

func TestAnExtendWorkItemMovesTheWorkloadsExpiryAndTheNextBeatConfirmsIt(t *testing.T) {
	clientset := fake.NewClientset()
	k := kube.New(clientset)
	require.NoError(t, k.InstallSupportWorkload(t.Context(), kube.SupportWorkload{
		Window: windowOne, Level: "readOnly", Address: supportAddress, Key: oneTimeKey,
		ExpiresAt: time.Now().Add(time.Hour),
	}))
	until := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	h := newScriptedHub(t, extendWork(until))

	runSupportAgent(t, h, k, 3)

	windows, err := k.SupportWindows(t.Context())
	require.NoError(t, err)
	require.Len(t, windows, 1)
	assert.True(t, until.Equal(windows[0].ExpiresAt), "the workload now ends at %s, not %s", until, windows[0].ExpiresAt)
	second, _ := h.beat(1)
	assert.Equal(t, []hub.WorkResult{{ID: "wrk_extend"}}, second.Results)
	assert.Equal(t,
		[]hub.SupportConfirmation{{Window: windowOne, Event: hub.SupportExtended, ExpiresAt: until}},
		second.Support.Confirmations)
	third, _ := h.beat(2)
	assert.Empty(t, third.Support.Confirmations, "a confirmation the hub took is not sent again")
}

func TestAnExtendedWorkloadIsNotRemovedAtItsOldExpiry(t *testing.T) {
	k := kube.New(fake.NewClientset())
	require.NoError(t, k.InstallSupportWorkload(t.Context(), kube.SupportWorkload{
		Window: windowOne, Level: "readOnly", Address: supportAddress, Key: oneTimeKey,
		ExpiresAt: time.Now().Add(-time.Minute),
	}))
	require.NoError(t, k.ExtendSupportWorkload(t.Context(), windowOne, time.Now().Add(time.Hour)))

	windows, err := k.SupportWindows(t.Context())

	require.NoError(t, err)
	require.Len(t, windows, 1)
	assert.True(t, windows[0].ExpiresAt.After(time.Now()))
}

func TestExtendingAWorkloadThatIsNotThereFailsAndIsReportedWithoutTheKey(t *testing.T) {
	h := newScriptedHub(t, extendWork(time.Now().Add(24*time.Hour)))

	runSupportAgent(t, h, kube.New(fake.NewClientset()), 2)

	second, _ := h.beat(1)
	require.Len(t, second.Support.Confirmations, 1)
	assert.Equal(t, hub.SupportFailed, second.Support.Confirmations[0].Event)
	assert.NotEmpty(t, second.Results[0].Error)
	assert.NotEmpty(t, second.Results[0].ID, "the hub is told the item is done with")
}
