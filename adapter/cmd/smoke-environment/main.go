// Command smoke-environment bootstraps a kind cluster as a managed environment
// the way the control plane does, for the flux smoke suite: it installs the
// pinned Flux, commits the rendered pds release to the lab's Git tree, and
// writes the agent's Secret and the Flux sources. It stands in only for the
// AWS steps, which a kind cluster has no use for.
//
//	smoke-environment --endpoint URL --ca-file F --token-file F --enrollment F \
//	  --git-dir DIR --hub-url URL --image-repository R --image-tag T --chart-version V
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/managed"
)

// input is everything one bootstrap needs.
type input struct {
	access  managed.KubeAccess
	join    managed.Join
	release managed.ReleaseInput
	gitDir  string
	flux    string
}

// bootstrap runs the steps in the control plane's order: Flux, the release in
// Git, then the join, because the release reads the agent's Secret as soon as
// Flux applies it.
func bootstrap(ctx context.Context, api *managed.EnvironmentAPI, in input) error {
	if err := api.InstallFlux(ctx, in.access, in.flux); err != nil {
		return err
	}
	files, err := managed.ReleaseFiles(in.join.Environment, in.release)
	if err != nil {
		return err
	}
	for name, body := range files {
		path := filepath.Join(in.gitDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	return api.WriteJoin(ctx, in.access, in.join)
}

// readEnrollment reads the join out of the control plane's enrollment answer.
// An answer without an agent token is refused, and the error never carries the
// answer itself.
func readEnrollment(r io.Reader) (managed.Join, error) {
	var answer struct {
		Environment string `json:"environment"`
		AgentToken  string `json:"agentToken"`
		GitURL      string `json:"gitUrl"`
		Namespace   string `json:"namespace"`
	}
	if err := json.NewDecoder(r).Decode(&answer); err != nil {
		return managed.Join{}, errors.New("the enrollment answer is not JSON")
	}
	if answer.Environment == "" || answer.AgentToken == "" || answer.GitURL == "" {
		return managed.Join{}, errors.New("the enrollment answer has no environment, agent token or Git address")
	}

	return managed.Join{
		Environment: answer.Environment, AgentToken: answer.AgentToken,
		GitURL: answer.GitURL, Namespace: answer.Namespace,
	}, nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "smoke-environment:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("smoke-environment", flag.ContinueOnError)
	var (
		endpoint   = flags.String("endpoint", "", "the Kubernetes API address")
		caFile     = flags.String("ca-file", "", "the Kubernetes API's CA certificate")
		tokenFile  = flags.String("token-file", "", "a token with cluster-admin rights")
		enrollment = flags.String("enrollment", "", "the control plane's enrollment answer")
		gitDir     = flags.String("git-dir", "", "the lab's Git working tree")
		hubURL     = flags.String("hub-url", "", "the control plane's address as the environment reaches it")
		repository = flags.String("image-repository", "", "the agent image")
		tag        = flags.String("image-tag", "", "the agent image tag")
		chart      = flags.String("chart-version", "", "the pds chart version")
		chartURL   = flags.String("chart-url", config.DefaultManagedChartURL, "the OCI registry the chart is installed from")
		bucket     = flags.String("bucket", "smoke", "the backup bucket the release names; nothing is created")
		region     = flags.String("region", "us-east-1", "the backup region the release names")
	)
	if err := flags.Parse(args); err != nil {
		return err
	}

	ca, err := os.ReadFile(*caFile)
	if err != nil {
		return fmt.Errorf("read the CA: %w", err)
	}
	token, err := os.ReadFile(*tokenFile)
	if err != nil {
		return fmt.Errorf("read the token: %w", err)
	}
	file, err := os.Open(*enrollment)
	if err != nil {
		return fmt.Errorf("read the enrollment: %w", err)
	}
	defer func() { _ = file.Close() }()
	join, err := readEnrollment(file)
	if err != nil {
		return err
	}

	api := managed.NewEnvironmentAPI(
		func(access managed.KubeAccess) (managed.Remote, error) {
			return kube.NewRemote(access.Endpoint, access.CA, access.Token)
		},
		managed.NewFluxSource(&http.Client{Timeout: 2 * time.Minute}, managed.FluxReleases, managed.PinnedFluxDigests),
	)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	return bootstrap(ctx, api, input{
		access: managed.KubeAccess{Endpoint: *endpoint, CA: ca, Token: string(token)},
		join:   join,
		release: managed.ReleaseInput{
			ChartVersion: *chart, ChartURL: *chartURL, HubURL: *hubURL, EnvironmentName: join.Environment,
			ImageRepository: *repository, ImageTag: *tag, Bucket: *bucket, Region: *region,
		},
		gitDir: *gitDir,
		flux:   managed.FluxVersion,
	})
}
