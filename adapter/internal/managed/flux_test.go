package managed_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/percona/pds/adapter/internal/managed"
)

func digest(body string) string {
	sum := sha256.Sum256([]byte(body))

	return hex.EncodeToString(sum[:])
}

func fluxServer(t *testing.T, body string, hits *atomic.Int32) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/v2.5.1/install.yaml" {
			http.NotFound(w, r)

			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestFluxSourceReturnsTheManifestWhoseDigestIsPinned(t *testing.T) {
	var hits atomic.Int32
	srv := fluxServer(t, "kind: Namespace\n", &hits)
	source := managed.NewFluxSource(srv.Client(), srv.URL, map[string]string{"2.5.1": digest("kind: Namespace\n")})

	got, err := source(t.Context(), "2.5.1")

	require.NoError(t, err)
	assert.Equal(t, "kind: Namespace\n", string(got))
}

func TestFluxSourceRefusesAManifestThatDoesNotMatchItsDigest(t *testing.T) {
	var hits atomic.Int32
	srv := fluxServer(t, "kind: Replaced\n", &hits)
	source := managed.NewFluxSource(srv.Client(), srv.URL, map[string]string{"2.5.1": digest("kind: Namespace\n")})

	got, err := source(t.Context(), "2.5.1")

	require.Error(t, err)
	assert.Nil(t, got, "a release asset can be replaced after it was published")
	assert.NotContains(t, err.Error(), "Replaced")
}

func TestFluxSourceRefusesAVersionNobodyPinnedWithoutAskingForIt(t *testing.T) {
	var hits atomic.Int32
	srv := fluxServer(t, "x", &hits)
	source := managed.NewFluxSource(srv.Client(), srv.URL, map[string]string{"2.5.1": digest("x")})

	_, err := source(t.Context(), "2.6.0")

	require.Error(t, err)
	assert.Zero(t, hits.Load())
}

func TestFluxSourceFailsOnAnErrorStatus(t *testing.T) {
	var hits atomic.Int32
	srv := fluxServer(t, "x", &hits)
	source := managed.NewFluxSource(srv.Client(), srv.URL, map[string]string{"9.9.9": digest("x")})

	_, err := source(t.Context(), "9.9.9")

	require.Error(t, err)
}

func TestThePinnedFluxVersionHasADigestTheJoinScriptAgreesWith(t *testing.T) {
	raw, err := os.ReadFile("../../../charts/pds/compatibility.yaml")
	require.NoError(t, err)
	var pin struct {
		Flux struct {
			Version string `json:"version"`
		} `json:"flux"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &pin))
	script, err := os.ReadFile("../api/connect_bootstrap.sh")
	require.NoError(t, err)
	match := regexp.MustCompile(`FLUX_PINNED_SHA256="([0-9a-f]{64})"`).FindSubmatch(script)
	require.NotNil(t, match)

	assert.Equal(t, managed.FluxVersion, pin.Flux.Version, "bump FluxVersion with the pin")
	assert.Equal(t, string(match[1]), managed.PinnedFluxDigests[pin.Flux.Version],
		"the control plane and the join script install the same Flux manifest")
}
