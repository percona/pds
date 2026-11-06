package everest_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
)

func TestSpecRoundTripKeepsEveryMember(t *testing.T) {
	const recorded = `{
	  "providerRef": {"name": "percona-xtradb-cluster"},
	  "parameters": {"restoreWaitTimeout": "10m"},
	  "dataSource": {"type": "Backup", "backup": {"backupRef": {"name": "orders-dev-20260824091500"}}},
	  "components": {
	    "engine": {
	      "replicas": 1,
	      "storage": {"size": "20Gi"},
	      "parameters": {"configuration": "max_connections=200"}
	    }
	  }
	}`

	var spec everest.InstanceSpec
	require.NoError(t, json.Unmarshal([]byte(recorded), &spec))

	assert.Equal(t, everest.ProviderPXC, spec.ProviderRef.Name)
	require.Contains(t, spec.Components, "engine")
	assert.Equal(t, int32(1), spec.Components["engine"].ReplicaCount())

	encoded, err := json.Marshal(spec)
	require.NoError(t, err)

	assert.JSONEq(t, recorded, string(encoded))
}

func TestEditedFieldsWinOverTheKeptRemainder(t *testing.T) {
	var spec everest.InstanceSpec
	require.NoError(t, json.Unmarshal([]byte(
		`{"parameters":{"keep":true},"providerRef":{"name":"percona-xtradb-cluster"},"components":{"engine":{"replicas":1,"storage":{"size":"20Gi"}}}}`,
	), &spec))

	eng := spec.Components["engine"]
	eng.Replicas = everest.Ptr(int32(3))
	spec.Components["engine"] = eng

	encoded, err := json.Marshal(spec)
	require.NoError(t, err)

	assert.JSONEq(t,
		`{"parameters":{"keep":true},"providerRef":{"name":"percona-xtradb-cluster"},"components":{"engine":{"replicas":3,"storage":{"size":"20Gi"}}}}`,
		string(encoded))
}

func TestUnmodeledFieldsCanBeReadWithoutATypedField(t *testing.T) {
	var spec everest.InstanceSpec
	require.NoError(t, json.Unmarshal([]byte(
		`{"parameters":{"keep":true},"providerRef":{"name":"percona-xtradb-cluster"}}`,
	), &spec))

	require.Contains(t, spec.Unknown, "parameters")
	assert.JSONEq(t, `{"keep":true}`, string(spec.Unknown["parameters"]))
	assert.NotContains(t, spec.Unknown, "providerRef", "a modeled member is not kept twice")
}

func TestMalformedSpecIsAnError(t *testing.T) {
	var spec everest.InstanceSpec
	require.Error(t, json.Unmarshal([]byte(`{"providerRef": []}`), &spec))
}

// TestInstanceDataSourceIsModeled covers the member a restore has to write. It
// only ever arrived as part of the kept remainder read back off an existing
// object, so a manifest built here could not ask to provision from a backup.
func TestInstanceDataSourceIsModeled(t *testing.T) {
	spec := everest.InstanceSpec{
		ProviderRef: everest.ObjectRef{Name: everest.ProviderPXC},
		DataSource: &everest.DataSource{
			Type:   everest.DataSourceTypeBackup,
			Backup: &everest.DataSourceBackup{BackupRef: everest.ObjectRef{Name: "orders-dev-20260824091500"}},
		},
	}

	encoded, err := json.Marshal(spec)
	require.NoError(t, err)
	assert.JSONEq(t, `{
	  "providerRef": {"name": "percona-xtradb-cluster"},
	  "dataSource": {"type": "Backup", "backup": {"backupRef": {"name": "orders-dev-20260824091500"}}}
	}`, string(encoded))

	var back everest.InstanceSpec
	require.NoError(t, json.Unmarshal(encoded, &back))
	require.NotNil(t, back.DataSource)
	require.NotNil(t, back.DataSource.Backup)
	assert.Equal(t, "orders-dev-20260824091500", back.DataSource.Backup.BackupRef.Name)
	assert.NotContains(t, back.Unknown, "dataSource",
		"a modeled member must not also sit in the kept remainder")
}

// An instance with no dataSource must not grow an empty one, which the API
// server would read as a request to provision from nothing.
func TestInstanceWithoutADataSourceWritesNone(t *testing.T) {
	encoded, err := json.Marshal(everest.InstanceSpec{
		ProviderRef: everest.ObjectRef{Name: everest.ProviderPXC},
	})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "dataSource")
}

// A dataSource carries more than the two members the adapter writes. A scale
// PUTs the whole instance back, so anything here that is not kept is deleted
// from an instance the developer only asked to resize.
func TestDataSourceKeepsWhatItDoesNotModel(t *testing.T) {
	const recorded = `{
	  "providerRef": {"name": "percona-xtradb-cluster"},
	  "dataSource": {
	    "type": "Backup",
	    "pitr": {"type": "date", "date": "2026-08-25T02:00:00Z"},
	    "backup": {"backupRef": {"name": "orders-dev-20260824091500"}}
	  }
	}`

	var spec everest.InstanceSpec
	require.NoError(t, json.Unmarshal([]byte(recorded), &spec))
	require.NotNil(t, spec.DataSource)
	require.NotNil(t, spec.DataSource.Backup)
	assert.Equal(t, "orders-dev-20260824091500", spec.DataSource.Backup.BackupRef.Name)

	encoded, err := json.Marshal(spec)
	require.NoError(t, err)
	assert.JSONEq(t, recorded, string(encoded))
}

func TestComponentServiceIsModeledAndRoundTrips(t *testing.T) {
	const recorded = `{
	  "type": "haproxy",
	  "service": {
	    "serviceType": "LoadBalancer",
	    "annotations": {"service.beta.kubernetes.io/aws-load-balancer-scheme": "internet-facing"},
	    "loadBalancerService": {"sourceRanges": ["10.0.0.0/8", "203.0.113.7/32"]}
	  }
	}`

	var component everest.ComponentSpec
	require.NoError(t, json.Unmarshal([]byte(recorded), &component))

	require.NotNil(t, component.Service)
	assert.Equal(t, everest.ServiceTypeLoadBalancer, component.Service.ServiceType)
	assert.Equal(t, "internet-facing",
		component.Service.Annotations["service.beta.kubernetes.io/aws-load-balancer-scheme"])
	require.NotNil(t, component.Service.LoadBalancerService)
	assert.Equal(t, []string{"10.0.0.0/8", "203.0.113.7/32"}, component.Service.LoadBalancerService.SourceRanges)
	assert.NotContains(t, component.Unknown, "service")

	encoded, err := json.Marshal(component)
	require.NoError(t, err)
	assert.JSONEq(t, recorded, string(encoded))
}

func TestComponentServiceKeepsWhatItDoesNotModel(t *testing.T) {
	const recorded = `{
	  "serviceType": "LoadBalancer",
	  "externalTrafficPolicy": "Local",
	  "loadBalancerService": {"sourceRanges": ["10.0.0.0/8"], "loadBalancerClass": "service.k8s.aws/nlb"}
	}`

	var svc everest.ServiceSpec
	require.NoError(t, json.Unmarshal([]byte(recorded), &svc))
	assert.Contains(t, svc.Unknown, "externalTrafficPolicy")
	require.NotNil(t, svc.LoadBalancerService)
	assert.Contains(t, svc.LoadBalancerService.Unknown, "loadBalancerClass")

	svc.LoadBalancerService.SourceRanges = []string{"192.168.0.0/16"}

	encoded, err := json.Marshal(svc)
	require.NoError(t, err)
	assert.JSONEq(t, `{
	  "serviceType": "LoadBalancer",
	  "externalTrafficPolicy": "Local",
	  "loadBalancerService": {"sourceRanges": ["192.168.0.0/16"], "loadBalancerClass": "service.k8s.aws/nlb"}
	}`, string(encoded))
}

func TestScheduleRetentionIsWrittenInTheDev4Shape(t *testing.T) {
	encoded, err := json.Marshal(everest.InstanceBackupSchedule{
		Name: "daily", Enabled: true, Cron: "0 2 * * *", Retention: everest.CountRetention(7),
	})
	require.NoError(t, err)

	assert.JSONEq(t, `{"name":"daily","enabled":true,"cron":"0 2 * * *","retention":{"type":"count","count":7}}`, string(encoded))
}

func TestScheduleCopies(t *testing.T) {
	tests := []struct {
		name string
		in   everest.InstanceBackupSchedule
		want int32
	}{
		{"count retention", everest.InstanceBackupSchedule{Retention: everest.CountRetention(7)}, 7},
		{"a count with no type is a count", everest.InstanceBackupSchedule{Retention: &everest.ScheduleRetention{Count: 4}}, 4},
		{"time retention keeps no fixed number", everest.InstanceBackupSchedule{Retention: &everest.ScheduleRetention{Type: everest.RetentionTypeTime, Duration: "4w"}}, 0},
		{"no retention keeps everything", everest.InstanceBackupSchedule{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.in.Copies())
		})
	}
}
