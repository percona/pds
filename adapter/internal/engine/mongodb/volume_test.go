package mongodb_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/percona/pds/adapter/internal/client/kube"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/mongodb"
)

// operatorObjects answers reads of the operator's PerconaServerMongoDB objects.
type operatorObjects struct {
	sizes map[string]string
	err   error
	reads []schema.GroupVersionResource
}

func (o *operatorObjects) GetObject(_ context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	o.reads = append(o.reads, gvr)
	if o.err != nil {
		return nil, o.err
	}
	size, ok := o.sizes[namespace+"/"+name]
	if !ok {
		return nil, kube.ErrObjectNotFound
	}

	return &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"replsets": []any{map[string]any{
			"name": "rs0",
			"volumeSpec": map[string]any{"persistentVolumeClaim": map[string]any{
				"resources": map[string]any{"requests": map[string]any{"storage": size}},
			}},
		}}},
	}}, nil
}

func sizedDatabase(name, storage string, storageGB float64) engine.Instance {
	return engine.Instance{
		Name: name, Namespace: "pds", Engine: mongodb.EngineID,
		Sizing: engine.Sizing{Members: 3, StorageGB: storageGB},
		Spec: engine.InstanceSpec{Components: engine.InstanceComponents{Engine: engine.EngineComponent{
			Replicas: 3, Resources: engine.EngineResources{CPU: "2", Memory: "8Gi", Storage: storage},
		}}},
		Status: engine.Status{Phase: engine.PhaseReady},
	}
}

// A storage class without volume expansion leaves the Instance spec at the
// requested size and Ready, while the operator puts its own object back to the
// size the claims have. The operator's object is the size that is true.
func TestReportVolumeSizesShowsTheSizeTheOperatorKeptNotTheOneRequested(t *testing.T) {
	items := []engine.Instance{sizedDatabase("orders", "10Gi", 10)}
	objects := &operatorObjects{sizes: map[string]string{"pds/orders": "5Gi"}}

	require.NoError(t, mongodb.ReportVolumeSizes(context.Background(), objects, items))

	assert.Equal(t, 5.0, items[0].Sizing.StorageGB)
	assert.Equal(t, "5Gi", items[0].Spec.Components.Engine.Resources.Storage)
	assert.Equal(t, engine.PhaseReady, items[0].Status.Phase)
	assert.Equal(t, schema.GroupVersionResource{Group: "psmdb.percona.com", Version: "v1", Resource: "perconaservermongodbs"}, objects.reads[0])
}

func TestReportVolumeSizesShowsAnIncreaseTheOperatorAccepted(t *testing.T) {
	items := []engine.Instance{sizedDatabase("orders", "5Gi", 5)}
	objects := &operatorObjects{sizes: map[string]string{"pds/orders": "10Gi"}}

	require.NoError(t, mongodb.ReportVolumeSizes(context.Background(), objects, items))

	assert.Equal(t, 10.0, items[0].Sizing.StorageGB)
	assert.Equal(t, "10Gi", items[0].Spec.Components.Engine.Resources.Storage)
}

func TestReportVolumeSizesLeavesOtherEnginesAndUnreadableObjectsAlone(t *testing.T) {
	mysqlDB := sizedDatabase("billing", "20Gi", 20)
	mysqlDB.Engine = "mysql"
	missing := sizedDatabase("fresh", "10Gi", 10)
	items := []engine.Instance{mysqlDB, missing}
	objects := &operatorObjects{sizes: map[string]string{"pds/billing": "1Gi"}}

	require.NoError(t, mongodb.ReportVolumeSizes(context.Background(), objects, items))

	assert.Equal(t, 20.0, items[0].Sizing.StorageGB, "another engine's size is not read from this operator")
	assert.Equal(t, 10.0, items[1].Sizing.StorageGB, "a database with no operator object yet keeps the Instance size")
	assert.Len(t, objects.reads, 1)
}

func TestReportVolumeSizesKeepsTheInstanceSizeAndReportsAFailedRead(t *testing.T) {
	items := []engine.Instance{sizedDatabase("orders", "10Gi", 10)}
	objects := &operatorObjects{err: errors.New("forbidden")}

	err := mongodb.ReportVolumeSizes(context.Background(), objects, items)

	require.ErrorContains(t, err, "orders")
	assert.Equal(t, 10.0, items[0].Sizing.StorageGB)
}
