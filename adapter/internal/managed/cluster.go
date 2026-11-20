package managed

import (
	"bytes"
	"context"
	"fmt"

	"sigs.k8s.io/yaml"
)

// Remote is an environment's Kubernetes API for as long as the window is open.
// kube.Remote is the implementation.
type Remote interface {
	Apply(ctx context.Context, manifests []byte) error
	WaitDeployments(ctx context.Context, namespace string, names ...string) error
}

// EnvironmentAPI writes to a managed environment's Kubernetes API.
type EnvironmentAPI struct {
	dial func(KubeAccess) (Remote, error)
	flux FluxSource
}

// NewEnvironmentAPI builds the Cluster the create steps use. dial reaches an
// environment with what the window gives, and flux fetches a pinned manifest.
func NewEnvironmentAPI(dial func(KubeAccess) (Remote, error), flux FluxSource) *EnvironmentAPI {
	return &EnvironmentAPI{dial: dial, flux: flux}
}

// Names the join writes that the release and the repository's paths agree on.
const (
	fluxNamespace = "flux-system"
	// gitRepository is the Flux source of the environment's Git repository.
	gitRepository = "hub"
	// gitSecret holds the read-only key Flux clones with.
	gitSecret = "hub-github"
	// githubKnownHosts is GitHub's published ed25519 host key, so Flux does not
	// trust the first key it is offered.
	githubKnownHosts = "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
)

var fluxControllers = []string{"source-controller", "kustomize-controller", "helm-controller"}

type object = map[string]any

// InstallFlux applies the pinned Flux, waits for its controllers, and creates
// the accounts that apply what Git holds: the release's cluster-admin account
// and the narrower ones of its Kustomizations.
func (c *EnvironmentAPI) InstallFlux(ctx context.Context, access KubeAccess, version string) error {
	remote, err := c.dial(access)
	if err != nil {
		return err
	}
	manifest, err := c.flux(ctx, version)
	if err != nil {
		return err
	}
	if err := remote.Apply(ctx, manifest); err != nil {
		return fmt.Errorf("install Flux: %w", err)
	}
	if err := remote.WaitDeployments(ctx, fluxNamespace, fluxControllers...); err != nil {
		return fmt.Errorf("install Flux: %w", err)
	}

	accounts, err := render(
		namespace(ReleaseNamespace),
		serviceAccount(HelmServiceAccount, fluxNamespace),
		adminBinding(HelmServiceAccount),
		serviceAccount("hub-release", fluxNamespace),
		role("hub-release", fluxNamespace, rule([]string{"helm.toolkit.fluxcd.io"}, "helmreleases"), rule([]string{"source.toolkit.fluxcd.io"}, "helmrepositories")),
		roleBinding("hub-release", fluxNamespace),
		serviceAccount("hub-platform", fluxNamespace),
		role("hub-platform", ReleaseNamespace,
			rule([]string{"helm.toolkit.fluxcd.io"}, "helmreleases"), rule([]string{"source.toolkit.fluxcd.io"}, "helmrepositories"), rule([]string{""}, "configmaps")),
		roleBinding("hub-platform", ReleaseNamespace),
	)
	if err != nil {
		return err
	}
	if err := remote.Apply(ctx, accounts); err != nil {
		return fmt.Errorf("create the release's accounts: %w", err)
	}

	return nil
}

// WriteJoin writes the agent's Secret and points Flux at the environment's
// Git repository: instances, monitoring and the pds release each from their
// own path under clusters/<environment>. The Secret goes first, because the
// release reads it as soon as Flux applies it.
func (c *EnvironmentAPI) WriteJoin(ctx context.Context, access KubeAccess, join Join) error {
	remote, err := c.dial(access)
	if err != nil {
		return err
	}

	docs := []object{secret(AgentTokenSecret, ReleaseNamespace, object{"HUB_AGENT_TOKEN": join.AgentToken})}
	repo := object{"interval": "15s", "url": join.GitURL, "ref": object{"branch": "main"}}
	if join.GitIdentity != "" {
		docs = append(docs, secret(gitSecret, fluxNamespace, object{
			"identity": join.GitIdentity + "\n", "identity.pub": join.GitIdentityPub + "\n", "known_hosts": githubKnownHosts + "\n",
		}))
		repo["secretRef"] = object{"name": gitSecret}
	}
	docs = append(docs, instancesAccess(join.Namespace)...)
	docs = append(docs,
		object{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository", "metadata": meta(gitRepository, fluxNamespace), "spec": repo},
		kustomization("instances", "clusters/"+join.Environment+"/instances", "hub-instances", join.Namespace),
		kustomization("platform", "clusters/"+join.Environment+"/platform/pmm", "hub-platform", ReleaseNamespace),
		kustomization(ReleaseName, "clusters/"+join.Environment+"/platform/pds", "hub-release", ""),
	)

	manifests, err := render(docs...)
	if err != nil {
		return err
	}
	if err := remote.Apply(ctx, manifests); err != nil {
		return fmt.Errorf("write the join: %w", err)
	}

	return nil
}

// instancesAccess is the account that applies the environment's Instances, and
// the namespace they live in. It may touch nothing else.
func instancesAccess(databases string) []object {
	return []object{
		namespace(databases),
		serviceAccount("hub-instances", fluxNamespace),
		role("hub-instances", databases, object{"apiGroups": []string{"core.openeverest.io"}, "resources": []string{"instances"}, "verbs": verbs}),
		roleBinding("hub-instances", databases),
	}
}

var verbs = []string{"get", "list", "watch", "create", "update", "patch", "delete"}

func meta(name, namespace string) object {
	m := object{"name": name}
	if namespace != "" {
		m["namespace"] = namespace
	}

	return m
}

func namespace(name string) object {
	return object{"apiVersion": "v1", "kind": "Namespace", "metadata": meta(name, "")}
}

func serviceAccount(name, namespace string) object {
	return object{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": meta(name, namespace)}
}

func secret(name, namespace string, data object) object {
	return object{"apiVersion": "v1", "kind": "Secret", "metadata": meta(name, namespace), "type": "Opaque", "stringData": data}
}

func rule(groups []string, resource string) object {
	return object{"apiGroups": groups, "resources": []string{resource}, "verbs": verbs}
}

func role(name, namespace string, rules ...object) object {
	return object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": meta(name, namespace), "rules": rules}
}

func roleBinding(name, namespace string) object {
	return object{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": meta(name, namespace),
		"roleRef":  object{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": name},
		"subjects": []object{{"kind": "ServiceAccount", "name": name, "namespace": fluxNamespace}},
	}
}

func adminBinding(account string) object {
	return object{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": meta(account, ""),
		"roleRef":  object{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "cluster-admin"},
		"subjects": []object{{"kind": "ServiceAccount", "name": account, "namespace": fluxNamespace}},
	}
}

func kustomization(name, path, account, target string) object {
	spec := object{
		"interval": "15s", "prune": true, "serviceAccountName": account, "path": path,
		"sourceRef": object{"kind": "GitRepository", "name": gitRepository},
	}
	if target != "" {
		spec["targetNamespace"] = target
	}

	return object{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": meta(name, fluxNamespace), "spec": spec}
}

// render joins objects into one manifest, in the order given.
func render(docs ...object) ([]byte, error) {
	var out bytes.Buffer
	for i, doc := range docs {
		body, err := yaml.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("render a manifest: %w", err)
		}
		if i > 0 {
			out.WriteString("---\n")
		}
		out.Write(body)
	}

	return out.Bytes(), nil
}
