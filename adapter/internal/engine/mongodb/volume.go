package mongodb

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/oe"
)

// serverResource is the operator's own object for a database. Its name is the
// Instance's.
var serverResource = schema.GroupVersionResource{Group: "psmdb.percona.com", Version: "v1", Resource: "perconaservermongodbs"}

// Objects is the part of the Kubernetes API the volume read uses.
type Objects interface {
	GetObject(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error)
}

var _ Objects = (*kube.Client)(nil)

// ReportVolumeSizes replaces the storage size of each MongoDB database with
// the one its operator holds. When a storage class refuses to expand a volume
// the provider accepts the new size on the Instance and stays Ready, with no
// condition or event, while the operator puts its own object back to the size
// the volumes have. The Instance spec is then not the size of the disk, and
// the operator's object is.
//
// A database whose object is not there yet, or whose size cannot be read,
// keeps the Instance's size. Reads that fail for another reason are joined in
// the returned error, and the database still keeps the Instance's size.
func ReportVolumeSizes(ctx context.Context, objects Objects, items []engine.Instance) error {
	var failures []error
	for i := range items {
		if items[i].Engine != EngineID {
			continue
		}
		obj, err := objects.GetObject(ctx, serverResource, items[i].Namespace, items[i].Name)
		if errors.Is(err, kube.ErrObjectNotFound) {
			continue
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("read the volume size of %s: %w", items[i].Name, err))

			continue
		}
		size, err := volumeSize(obj)
		if err != nil {
			failures = append(failures, fmt.Errorf("read the volume size of %s: %w", items[i].Name, err))

			continue
		}
		gb, err := oe.ParseSizeGB(size)
		if err != nil {
			failures = append(failures, fmt.Errorf("read the volume size of %s: %w", items[i].Name, err))

			continue
		}
		items[i].Sizing.StorageGB = gb
		items[i].Spec.Components.Engine.Resources.Storage = size
	}

	return errors.Join(failures...)
}

// volumeSize is the size the operator requests for the data volumes of the
// first replica set, which is the only one a PDS database has.
func volumeSize(obj *unstructured.Unstructured) (string, error) {
	sets, found, err := unstructured.NestedSlice(obj.Object, "spec", "replsets")
	if err != nil || !found || len(sets) == 0 {
		return "", errors.New("the object has no replica set")
	}
	set, ok := sets[0].(map[string]any)
	if !ok {
		return "", errors.New("the replica set is not an object")
	}
	size, found, err := unstructured.NestedString(set, "volumeSpec", "persistentVolumeClaim", "resources", "requests", "storage")
	if err != nil || !found || size == "" {
		return "", errors.New("the replica set has no volume size")
	}

	return size, nil
}
