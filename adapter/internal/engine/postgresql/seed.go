package postgresql

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/platform"
)

// The operator's own kinds. A seed reads and writes them directly because the
// provider refuses to restore into a new Instance.
var (
	pgResource     = schema.GroupVersionResource{Group: "pgv2.percona.com", Version: "v2", Resource: "perconapgclusters"}
	backupResource = schema.GroupVersionResource{Group: "pgv2.percona.com", Version: "v2", Resource: "perconapgbackups"}
)

// Objects is the part of the Kubernetes API a seed uses.
type Objects interface {
	GetObject(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error)
	CreateObject(ctx context.Context, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error
	DeleteObject(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error
}

// cleanupTimeout bounds the removal of a cluster after a failed create. It
// runs on its own context because the caller's has usually expired.
const cleanupTimeout = 30 * time.Second

var _ Objects = (*kube.Client)(nil)

// Seed creates the PerconaPGCluster that name's Instance will adopt, with a
// dataSource that restores the source database's backup into it.
//
// A create that errors may still have reached the API server, which leaves a
// cluster restoring with no Instance to adopt it. Seed then deletes the
// cluster when it restores this backup, and reports the create's error.
//
// The provider keeps a dataSource only when the cluster already carries one,
// so the cluster has to exist before the Instance does. The new database
// reads the source's repository and archives to a repository of its own once
// the provider takes it over.
//
// A cluster that already carries the copy's name is left alone when it is the
// one an earlier run of this seed created, so an item the hub offers twice does
// not restore over a running database. Any other is refused: adding a
// dataSource to a cluster makes the operator bootstrap it again and discard
// what it holds.
func Seed(ctx context.Context, objects Objects, namespace, source, backupID, name string) error {
	backup, err := objects.GetObject(ctx, backupResource, namespace, backupID)
	if errors.Is(err, kube.ErrObjectNotFound) {
		return fmt.Errorf("%w: %s", engine.ErrBackupNotFound, backupID)
	}
	if err != nil {
		return readFailure(fmt.Sprintf("read backup %s", backupID), err)
	}

	existing, err := objects.GetObject(ctx, pgResource, namespace, name)
	if err == nil {
		return seededBefore(existing, backup)
	}
	if !errors.Is(err, kube.ErrObjectNotFound) {
		return readFailure(fmt.Sprintf("read database %s", name), err)
	}

	origin, err := objects.GetObject(ctx, pgResource, namespace, source)
	if errors.Is(err, kube.ErrObjectNotFound) {
		return fmt.Errorf("%w: %s", engine.ErrNotFound, source)
	}
	if err != nil {
		return readFailure(fmt.Sprintf("read database %s", source), err)
	}
	dataSource, err := seedDataSource(backup, origin, source)
	if err != nil {
		return err
	}

	// The copy takes the source's shape, not its monitoring, its own
	// repository settings or its load balancer. It reaches the source's S3
	// credentials only through the Secret reference in the dataSource, which
	// the restore reads.
	spec := map[string]any{
		// The operator rejects enabled backups with no repository. The provider
		// enables them when it takes the cluster over.
		"backups":    map[string]any{"enabled": false},
		"dataSource": dataSource,
	}
	for _, key := range []string{"crVersion", "image", "postgresVersion", "port", "instances", "proxy"} {
		if value, found, _ := unstructured.NestedFieldCopy(origin.Object, "spec", key); found {
			spec[key] = value
		}
	}
	unstructured.RemoveNestedField(spec, "proxy", "pgBouncer", "expose")
	if err := objects.CreateObject(ctx, pgResource, pgWith(name, namespace, spec)); err != nil {
		err = readFailure(fmt.Sprintf("create database %s", name), err)
		// A taken name means the cluster there is not the one this create made.
		if !errors.Is(err, kube.ErrObjectExists) {
			if cleanupErr := removeFailedSeed(ctx, objects, namespace, name, backup); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
		}

		return err
	}

	return nil
}

// removeFailedSeed deletes the cluster of the copy's name when it is the one
// this seed asked for: it restores this backup and is not already being
// deleted. It finds nothing to do when the create never reached the server.
func removeFailedSeed(ctx context.Context, objects Objects, namespace, name string, backup *unstructured.Unstructured) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	existing, err := objects.GetObject(ctx, pgResource, namespace, name)
	if errors.Is(err, kube.ErrObjectNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check for a leftover database %s: %w", name, err)
	}
	if seededBefore(existing, backup) != nil {
		return nil
	}
	if err := objects.DeleteObject(ctx, pgResource, namespace, name); err != nil && !errors.Is(err, kube.ErrObjectNotFound) {
		return fmt.Errorf("remove leftover database %s: %w", name, err)
	}

	return nil
}

// readFailure wraps a failed read or create, and marks it as the environment
// being unable to seed when the cause is missing access, which no retry fixes.
func readFailure(what string, err error) error {
	if errors.Is(err, kube.ErrObjectForbidden) || errors.Is(err, platform.ErrUnavailable) {
		return fmt.Errorf("%s: %w: %w", what, engine.ErrSeedUnavailable, err)
	}

	return fmt.Errorf("%s: %w", what, err)
}

// seededBefore accepts a cluster of the copy's name only when it restores this
// backup and is not being deleted, which is what an earlier run of the same
// seed leaves behind.
func seededBefore(existing, backup *unstructured.Unstructured) error {
	label, _, _ := unstructured.NestedString(backup.Object, "status", "backupName")
	options, _, _ := unstructured.NestedSlice(existing.Object, "spec", "dataSource", "pgbackrest", "options")
	if existing.GetDeletionTimestamp() == nil && label != "" && slices.Contains(options, any("--set="+label)) {
		return nil
	}

	return fmt.Errorf("%w: %s", engine.ErrExists, existing.GetName())
}

func pgWith(name, namespace string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "pgv2.percona.com/v2",
		"kind":       "PerconaPGCluster",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec":       spec,
	}}
}

// seedDataSource is the dataSource that restores one finished backup of
// source. The credentials come from the Secret the source's own configuration
// projects, and the repository path from where the backup says it was written.
func seedDataSource(backup, origin *unstructured.Unstructured, source string) (map[string]any, error) {
	if owner, _, _ := unstructured.NestedString(backup.Object, "spec", "pgCluster"); owner != source {
		return nil, fmt.Errorf("%w: %s is not a backup of %s", engine.ErrBackupNotFound, backup.GetName(), source)
	}
	if state, _, _ := unstructured.NestedString(backup.Object, "status", "state"); state != "Succeeded" {
		return nil, fmt.Errorf("%w: %s is %q", engine.ErrBackupUnusable, backup.GetName(), state)
	}
	label, _, _ := unstructured.NestedString(backup.Object, "status", "backupName")
	if label == "" {
		return nil, fmt.Errorf("%w: %s has no backup label", engine.ErrBackupUnusable, backup.GetName())
	}
	repo, found, _ := unstructured.NestedMap(backup.Object, "status", "repo")
	repoName, _ := repo["name"].(string)
	if !found || repoName == "" {
		return nil, fmt.Errorf("%w: %s names no repository", engine.ErrBackupUnusable, backup.GetName())
	}

	pgbackrest := map[string]any{
		"stanza":  "db",
		"repo":    repo,
		"options": []any{"--set=" + label, "--type=immediate"},
	}
	if configuration, found, _ := unstructured.NestedFieldCopy(origin.Object, "spec", "backups", "pgbackrest", "configuration"); found {
		pgbackrest["configuration"] = configuration
	}
	global := map[string]any{}
	if copied, found, _ := unstructured.NestedMap(origin.Object, "spec", "backups", "pgbackrest", "global"); found {
		global = copied
	}
	destination, _, _ := unstructured.NestedString(backup.Object, "status", "destination")
	if parsed, err := url.Parse(destination); err == nil && parsed.Path != "" {
		global[repoName+"-path"] = parsed.Path
	}
	pgbackrest["global"] = global

	return map[string]any{"pgbackrest": pgbackrest}, nil
}
