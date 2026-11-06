package kube_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/client/kube/kubetest"
)

func TestRemovingAWindowLeavesAnotherWindowsWorkloadAlone(t *testing.T) {
	clientset := fake.NewClientset()
	client := kube.New(clientset)
	expires := time.Now().Add(time.Hour)
	require.NoError(t, client.InstallSupportWorkload(t.Context(), kube.SupportWorkload{
		Window: "win_new", Level: "readOnly", Address: "svc:pds-a", Key: "k", ExpiresAt: expires,
	}))

	require.NoError(t, client.RemoveSupportWorkload(t.Context(), "win_old"))

	windows, err := client.SupportWindows(t.Context())
	require.NoError(t, err)
	require.Len(t, windows, 1)
	assert.Equal(t, "win_new", windows[0].ID)
	assert.WithinDuration(t, expires, windows[0].ExpiresAt, time.Second)
	_, err = clientset.CoreV1().Secrets(kube.SupportNamespace).Get(t.Context(), "pds-support-config", metav1.GetOptions{})
	assert.NoError(t, err)
}

// The proxy writes the certificate it is issued into a Secret named after the
// address's DNS name, and may update that Secret but not create it.
func TestTheWorkloadStartsWithAnEmptySecretNamedForTheAddressHost(t *testing.T) {
	clientset := fake.NewClientset()
	client := kube.New(clientset)
	const host = "pds-a.tail1234.ts.net"

	require.NoError(t, client.InstallSupportWorkload(t.Context(), kube.SupportWorkload{
		Window: "win_a", Level: "readOnly", Address: "svc:pds-a", Host: host, Key: "k", ExpiresAt: time.Now().Add(time.Hour),
	}))

	cert, err := clientset.CoreV1().Secrets(kube.SupportNamespace).Get(t.Context(), host, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, cert.Data)
}

// serviceName only makes the proxy serve the address. The tailnet routes to a
// host that advertises the Service, so the config has to list it too.
func TestTheProxyConfigAdvertisesTheWindowsAddress(t *testing.T) {
	clientset := fake.NewClientset()
	client := kube.New(clientset)

	require.NoError(t, client.InstallSupportWorkload(t.Context(), kube.SupportWorkload{
		Window: "win_a", Level: "readOnly", Address: "svc:pds-a", Key: "k", ExpiresAt: time.Now().Add(time.Hour),
	}))

	secret, err := clientset.CoreV1().Secrets(kube.SupportNamespace).Get(t.Context(), "pds-support-config", metav1.GetOptions{})
	require.NoError(t, err)
	var config struct {
		AdvertiseServices []string `json:"advertiseServices"`
		APIServerProxy    struct {
			ServiceName string `json:"serviceName"`
		} `json:"apiServerProxy"`
	}
	require.NoError(t, json.Unmarshal(secret.Data["config.json"], &config))
	assert.Equal(t, []string{"svc:pds-a"}, config.AdvertiseServices)
	assert.Equal(t, "svc:pds-a", config.APIServerProxy.ServiceName)
}

func TestAWindowWithNoAddressAdvertisesNothing(t *testing.T) {
	clientset := fake.NewClientset()
	client := kube.New(clientset)

	require.NoError(t, client.InstallSupportWorkload(t.Context(), kube.SupportWorkload{
		Window: "win_a", Level: "readOnly", Key: "k", ExpiresAt: time.Now().Add(time.Hour),
	}))

	secret, err := clientset.CoreV1().Secrets(kube.SupportNamespace).Get(t.Context(), "pds-support-config", metav1.GetOptions{})
	require.NoError(t, err)
	var config map[string]any
	require.NoError(t, json.Unmarshal(secret.Data["config.json"], &config))
	assert.NotContains(t, config, "advertiseServices")
}

// A certificate is issued for the address, not the window, and the issuer
// limits how often one name gets one, so the next window reuses it.
func TestTheNextWindowFindsTheCertificateTheLastOneWasIssued(t *testing.T) {
	clientset := fake.NewClientset()
	client := kube.New(clientset)
	const host = "pds-a.tail1234.ts.net"
	window := func(id string) kube.SupportWorkload {
		return kube.SupportWorkload{
			Window: id, Level: "readOnly", Address: "svc:pds-a", Host: host, Key: "k", ExpiresAt: time.Now().Add(time.Hour),
		}
	}
	require.NoError(t, client.InstallSupportWorkload(t.Context(), window("win_a")))
	secrets := clientset.CoreV1().Secrets(kube.SupportNamespace)
	issued, err := secrets.Get(t.Context(), host, metav1.GetOptions{})
	require.NoError(t, err)
	issued.Data = map[string][]byte{"tls.crt": []byte("cert"), "tls.key": []byte("key")}
	_, err = secrets.Update(t.Context(), issued, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.NoError(t, client.RemoveSupportWorkload(t.Context(), "win_a"))
	require.NoError(t, client.InstallSupportWorkload(t.Context(), window("win_b")))

	cert, err := secrets.Get(t.Context(), host, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []byte("cert"), cert.Data["tls.crt"])
}

// installedWindow installs a window for host and returns it as the agent lists
// it.
func installedWindow(t *testing.T, client *kube.Client, host string) kube.SupportWindow {
	t.Helper()

	require.NoError(t, client.InstallSupportWorkload(t.Context(), kube.SupportWorkload{
		Window: "win_a", Level: "readOnly", Address: "svc:pds-a", Host: host, Key: "k", ExpiresAt: time.Now().Add(time.Hour),
	}))
	windows, err := client.SupportWindows(t.Context())
	require.NoError(t, err)
	require.Len(t, windows, 1)

	return windows[0]
}

// issue plays the proxy writing the certificate it was issued.
func issue(t *testing.T, clientset *fake.Clientset, host string, notAfter time.Time) {
	t.Helper()

	cert, key := kubetest.Certificate(t, host, notAfter)
	secrets := clientset.CoreV1().Secrets(kube.SupportNamespace)
	issued, err := secrets.Get(t.Context(), host, metav1.GetOptions{})
	require.NoError(t, err)
	issued.Data = map[string][]byte{corev1.TLSCertKey: cert, corev1.TLSPrivateKeyKey: key}
	_, err = secrets.Update(t.Context(), issued, metav1.UpdateOptions{})
	require.NoError(t, err)
}

// The address answers no one until the proxy has written the certificate it
// was issued, and issuing can fail.
func TestAWindowServesOnceItsProxyHoldsTheAddressCertificate(t *testing.T) {
	clientset := fake.NewClientset()
	client := kube.New(clientset)
	const host = "pds-a.tail1234.ts.net"
	window := installedWindow(t, client, host)

	serves, err := client.SupportServes(t.Context(), window)
	require.NoError(t, err)
	assert.False(t, serves, "an empty certificate Secret is a certificate not yet issued")

	issue(t, clientset, host, time.Now().Add(60*24*time.Hour))

	serves, err = client.SupportServes(t.Context(), window)
	require.NoError(t, err)
	assert.True(t, serves)
}

// The certificate outlives windows, so the next one can find it expired. The
// proxy then asks for a new one, which can fail like the first.
func TestAWindowWhoseKeptCertificateHasExpiredDoesNotServe(t *testing.T) {
	clientset := fake.NewClientset()
	client := kube.New(clientset)
	const host = "pds-a.tail1234.ts.net"
	window := installedWindow(t, client, host)
	issue(t, clientset, host, time.Now().Add(-time.Hour))

	serves, err := client.SupportServes(t.Context(), window)

	require.NoError(t, err)
	assert.False(t, serves)
}

func TestAWindowWhoseCertificateSecretIsGoneDoesNotServe(t *testing.T) {
	clientset := fake.NewClientset()
	client := kube.New(clientset)
	const host = "pds-a.tail1234.ts.net"
	window := installedWindow(t, client, host)
	require.NoError(t, clientset.CoreV1().Secrets(kube.SupportNamespace).Delete(t.Context(), host, metav1.DeleteOptions{}))

	serves, err := client.SupportServes(t.Context(), window)

	require.NoError(t, err)
	assert.False(t, serves)
}

// With no private network there is no address and so no certificate to wait
// for: the workload is all there is.
func TestAWindowWithNoAddressServesOnceItsWorkloadIsThere(t *testing.T) {
	client := kube.New(fake.NewClientset())
	require.NoError(t, client.InstallSupportWorkload(t.Context(), kube.SupportWorkload{
		Window: "win_a", Level: "readOnly", Key: "k", ExpiresAt: time.Now().Add(time.Hour),
	}))
	windows, err := client.SupportWindows(t.Context())
	require.NoError(t, err)

	serves, err := client.SupportServes(t.Context(), windows[0])

	require.NoError(t, err)
	assert.True(t, serves)
}

// Kubernetes authorizes the identity the proxy impersonates, which is the group
// the tailnet maps engineers to. A binding that names the proxy's own account
// grants an engineer nothing.
func TestAWindowBindsItsRoleToTheGroupEngineersArriveAs(t *testing.T) {
	for level, role := range map[string]string{"readOnly": "pds-support-read", "admin": "pds-support-admin"} {
		t.Run(level, func(t *testing.T) {
			clientset := fake.NewClientset()
			client := kube.New(clientset)

			require.NoError(t, client.InstallSupportWorkload(t.Context(), kube.SupportWorkload{
				Window: "win_a", Level: level, Key: "k", ExpiresAt: time.Now().Add(time.Hour),
			}))

			binding, err := clientset.RbacV1().ClusterRoleBindings().Get(t.Context(), "pds-support-a", metav1.GetOptions{})
			require.NoError(t, err)
			assert.Equal(t, role, binding.RoleRef.Name)
			require.Len(t, binding.Subjects, 1)
			assert.Equal(t, "Group", binding.Subjects[0].Kind)
			assert.Equal(t, "pds.percona.com:support", binding.Subjects[0].Name)
		})
	}
}

func TestAClientWithNoKubernetesAccessCannotRunAWindow(t *testing.T) {
	var client *kube.Client

	assert.Error(t, client.InstallSupportWorkload(t.Context(), kube.SupportWorkload{Window: "win_a", Level: "admin"}))
	assert.Error(t, client.RemoveSupportWorkload(t.Context(), "win_a"))
	_, err := client.SupportWindows(t.Context())
	assert.Error(t, err)
}
