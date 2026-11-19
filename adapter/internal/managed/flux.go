package managed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// FluxSource returns the install manifest of a Flux version.
type FluxSource func(ctx context.Context, version string) ([]byte, error)

// FluxReleases is where Flux publishes its install manifests.
const FluxReleases = "https://github.com/fluxcd/flux2/releases/download"

// PinnedFluxDigests are the SHA-256 digests of the install manifests the
// control plane applies with cluster-admin rights. A release asset can be
// replaced after it was published, so the digest is what says the bytes are the
// ones that were reviewed. A version with no entry is refused.
var PinnedFluxDigests = map[string]string{
	"2.5.1": "1ab601012b35e0a84c817f839edb2a52f3fabab2a33d5498b29cf96f9feb57b2",
}

// maxFluxManifest bounds what is read from the release, which is about 1 MiB.
const maxFluxManifest = 16 << 20

// NewFluxSource downloads a version's manifest from base and returns it only
// when its digest is the pinned one.
func NewFluxSource(client *http.Client, base string, digests map[string]string) FluxSource {
	return func(ctx context.Context, version string) ([]byte, error) {
		want, ok := digests[version]
		if !ok {
			return nil, fmt.Errorf("no digest is pinned for Flux %s", version)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v%s/install.yaml", base, version), nil)
		if err != nil {
			return nil, fmt.Errorf("build the Flux request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("download Flux %s: %w", version, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("download Flux %s: the release answered %d", version, resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxFluxManifest+1))
		if err != nil {
			return nil, fmt.Errorf("download Flux %s: %w", version, err)
		}
		if len(body) > maxFluxManifest {
			return nil, errors.New("the Flux manifest is larger than any release")
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != want {
			return nil, fmt.Errorf("the Flux %s manifest does not match its pinned digest", version)
		}

		return body, nil
	}
}
