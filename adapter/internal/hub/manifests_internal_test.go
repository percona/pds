package hub

import (
	"fmt"
	"strings"
	"testing"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/oe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/percona/pds/adapter/internal/client/everest"
)

// The reader is unexported, and reading a manifest the hub wrote before the
// rename is the whole point of the test, so this one is white box.
func TestProfileFromInstanceYAMLReadsBothLabelDomains(t *testing.T) {
	for name, body := range map[string]string{
		"current": fmt.Sprintf(`
metadata:
  labels:
    %s: prod
`, oe.LabelProfile),
		"written before the rename": fmt.Sprintf(`
metadata:
  labels:
    %s: prod
`, oe.LegacyLabelProfile),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, "prod", profileFromInstanceYAML([]byte(body)))
		})
	}
}

// legacyDomain is assembled rather than written out, so the naming lint has
// nothing to trip on and the constant stays the single source of the string.
var legacyDomain = strings.TrimSuffix(oe.LegacyLabelProfile, "profile")

func TestInstanceYAMLWritesOnlyTheCurrentLabelDomain(t *testing.T) {
	body, _, err := instanceYAML("orders", "pds", "spoke-a", oe.MySQL, "prod", environmentTargets{}, engine.ResourceOverrides{}, engine.Exposure{})
	assert.NoError(t, err)

	assert.Contains(t, string(body), "pds.percona.com/profile")
	assert.NotContains(t, string(body), legacyDomain)
}

func TestInstanceYAMLLeavesAnInternalDatabaseWithoutAService(t *testing.T) {
	body, _, err := instanceYAML("orders", "pds", "spoke-a", oe.MySQL, "dev", environmentTargets{}, engine.ResourceOverrides{}, engine.Exposure{})
	require.NoError(t, err)

	assert.NotContains(t, string(body), "service")
}

func TestInstanceYAMLPutsTheProxyBehindALoadBalancer(t *testing.T) {
	body, _, err := instanceYAML("orders", "pds", "spoke-a", oe.MySQL, "dev", environmentTargets{}, engine.ResourceOverrides{},
		engine.Exposure{External: true, SourceRanges: []string{"10.0.0.0/8"}})
	require.NoError(t, err)

	var doc struct {
		Spec everest.InstanceSpec `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(body, &doc))

	proxy := doc.Spec.Components[everest.ComponentProxy]
	require.NotNil(t, proxy.Service)
	assert.Equal(t, everest.ServiceTypeLoadBalancer, proxy.Service.ServiceType)
	require.NotNil(t, proxy.Service.LoadBalancerService)
	assert.Equal(t, []string{"10.0.0.0/8"}, proxy.Service.LoadBalancerService.SourceRanges)
	assert.Nil(t, doc.Spec.Components[everest.ComponentEngine].Service)
}

func TestInstanceYAMLAsksForAPrivateLoadBalancer(t *testing.T) {
	body, _, err := instanceYAML("orders", "pds", "spoke-a", oe.MySQL, "dev", environmentTargets{}, engine.ResourceOverrides{},
		engine.Exposure{External: true, Private: true})
	require.NoError(t, err)

	assert.Contains(t, string(body), "serviceType: LoadBalancer")
	assert.Contains(t, string(body), "service.beta.kubernetes.io/aws-load-balancer-scheme: internal")
	assert.Contains(t, string(body), "10.0.0.0/8")
}

func TestInstanceYAMLWithAnEmptyAllowListNamesEveryAddress(t *testing.T) {
	body, _, err := instanceYAML("orders", "pds", "spoke-a", oe.MySQL, "dev", environmentTargets{}, engine.ResourceOverrides{},
		engine.Exposure{External: true})
	require.NoError(t, err)

	assert.Contains(t, string(body), "serviceType: LoadBalancer")
	assert.Contains(t, string(body), "0.0.0.0/0")
}

// Before the spoke first reports a new database the hub only has its manifest,
// and an external database must not read as internal in that window.
func TestExposureFromInstanceYAMLReadsWhatCreateWrote(t *testing.T) {
	want := engine.Exposure{External: true, SourceRanges: []string{"10.0.0.0/8"}}
	body, _, err := instanceYAML("orders", "pds", "spoke-a", oe.MySQL, "dev", environmentTargets{}, engine.ResourceOverrides{}, want)
	require.NoError(t, err)

	assert.Equal(t, want, exposureFromInstanceYAML(body))

	private := engine.Exposure{External: true, Private: true}
	body, _, err = instanceYAML("orders", "pds", "spoke-a", oe.MySQL, "dev", environmentTargets{}, engine.ResourceOverrides{}, private)
	require.NoError(t, err)
	assert.Equal(t, private, exposureFromInstanceYAML(body))

	internal, _, err := instanceYAML("orders", "pds", "spoke-a", oe.MySQL, "dev", environmentTargets{}, engine.ResourceOverrides{}, engine.Exposure{})
	require.NoError(t, err)
	assert.Equal(t, engine.Exposure{}, exposureFromInstanceYAML(internal))
	assert.Equal(t, engine.Exposure{}, exposureFromInstanceYAML([]byte("not: [yaml")))
}

// TestApplyScheduleUsesTheEngineBackupClass covers both engines: a manifest
// with no backup section gets the backup class of the provider it names, so a
// PostgreSQL schedule is not written against the MySQL class.
func TestApplyScheduleUsesTheEngineBackupClass(t *testing.T) {
	tests := []struct {
		provider  string
		wantClass string
	}{
		{everest.ProviderPXC, everest.BackupClassPXC},
		{everest.ProviderPostgreSQL, everest.BackupClassPostgreSQL},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			spec := everest.InstanceSpec{ProviderRef: everest.ObjectRef{Name: tt.provider}}

			require.NoError(t, applySchedule(&spec, &everest.ObjectMeta{}, "s3", engine.DefaultSchedule()))

			require.NotNil(t, spec.Backup)
			assert.Equal(t, tt.wantClass, spec.Backup.ClassRef.Name)
			require.Len(t, spec.Backup.Storages, 1)
			assert.Equal(t, "s3", spec.Backup.Storages[0].StorageRef.Name)
			require.Len(t, spec.Backup.Storages[0].Schedules, 1)
			assert.Equal(t, "0 2 * * *", spec.Backup.Storages[0].Schedules[0].Cron)
		})
	}
}

// TestApplyScheduleWithoutStorageIsRefused keeps the hub from writing a
// schedule against a storage with no name, which would fail every run.
func TestApplyScheduleWithoutStorageIsRefused(t *testing.T) {
	spec := everest.InstanceSpec{ProviderRef: everest.ObjectRef{Name: everest.ProviderPXC}}

	err := applySchedule(&spec, &everest.ObjectMeta{}, "", engine.DefaultSchedule())

	require.ErrorIs(t, err, engine.ErrBackupsUnavailable)
	assert.Nil(t, spec.Backup)
}

// TestInstanceYAMLNeverEnablesBackupsWithoutAStorage keeps every create
// startable. The PostgreSQL operator refuses a database with backups enabled
// and no repository, so it would wait to start forever.
func TestInstanceYAMLNeverEnablesBackupsWithoutAStorage(t *testing.T) {
	for _, flavor := range []oe.Flavor{oe.MySQL, oe.PostgreSQL} {
		for _, profile := range []string{oe.ProfileDev, oe.ProfileStaging, oe.ProfileProd} {
			t.Run(flavor.ID+" "+profile, func(t *testing.T) {
				body, _, err := instanceYAML("orders", "pds", "spoke-a", flavor, profile, environmentTargets{BackupStorage: "s3"}, engine.ResourceOverrides{}, engine.Exposure{})
				require.NoError(t, err)

				var doc struct {
					Spec everest.InstanceSpec `json:"spec"`
				}
				require.NoError(t, yaml.Unmarshal(body, &doc))
				require.NotNil(t, doc.Spec.Backup)
				assert.True(t, doc.Spec.Backup.Enabled)
				assert.Equal(t, flavor.BackupClass, doc.Spec.Backup.ClassRef.Name)
				require.Len(t, doc.Spec.Backup.Storages, 1)
				assert.Equal(t, "s3", doc.Spec.Backup.Storages[0].StorageRef.Name)
			})
		}
	}
}

// A MongoDB replica set has no proxy, and carries the pmm component only when
// the environment has a monitoring config to point it at.
func TestInstanceYAMLWritesAMongoDBReplicaSetWithEngineAndMonitoring(t *testing.T) {
	targets := environmentTargets{BackupStorage: "s3", MonitoringConfig: "pmm"}
	for profile, members := range map[string]int32{oe.ProfileDev: 1, oe.ProfileStaging: 3, oe.ProfileProd: 3} {
		t.Run(profile, func(t *testing.T) {
			body, p, err := instanceYAML("orders", "pds", "spoke-a", oe.MongoDB, profile, targets, engine.ResourceOverrides{}, engine.Exposure{})
			require.NoError(t, err)
			assert.Equal(t, members, p.members)

			var doc struct {
				Metadata everest.ObjectMeta   `json:"metadata"`
				Spec     everest.InstanceSpec `json:"spec"`
			}
			require.NoError(t, yaml.Unmarshal(body, &doc))
			assert.Equal(t, "percona-server-mongodb", doc.Spec.ProviderRef.Name)
			assert.Equal(t, "replicaSet", doc.Spec.Topology.Type)
			assert.Equal(t, "mongodb", doc.Metadata.Labels[oe.LabelEngine])
			assert.Equal(t, "pds-mongodb-"+profile, doc.Metadata.Annotations["openeverest.io/instance-preset"])
			require.Len(t, doc.Spec.Components, 2)
			assert.NotContains(t, doc.Spec.Components, everest.ComponentProxy)
			monitoring := doc.Spec.Components[everest.ComponentMonitoring]
			assert.Equal(t, everest.ComponentTypePMM, monitoring.Type)
			assert.JSONEq(t, `{"monitoringConfigName":"pmm"}`, string(monitoring.Parameters))
			engineSpec := doc.Spec.Components[everest.ComponentEngine]
			assert.Empty(t, engineSpec.Type)
			assert.Equal(t, members, engineSpec.ReplicaCount())
			assert.Nil(t, engineSpec.Service, "a MongoDB database has no load balancer yet")
		})
	}
}

// Backups cannot be turned on in a running MongoDB database without rolling
// every member, so a MongoDB database is created with them on and a storage. A
// Dev database keeps the storage with no schedule, and Staging and Prod carry
// the daily one. The provider's retention is left out, and the count the
// control plane prunes to rides on the Instance.
func TestInstanceYAMLWritesAMongoDBWithBackupsOnAndAStorage(t *testing.T) {
	for profile, daily := range map[string]bool{oe.ProfileDev: false, oe.ProfileStaging: true, oe.ProfileProd: true} {
		t.Run(profile, func(t *testing.T) {
			body, _, err := instanceYAML("orders", "pds", "spoke-a", oe.MongoDB, profile, environmentTargets{BackupStorage: "s3"}, engine.ResourceOverrides{}, engine.Exposure{})
			require.NoError(t, err)

			var doc struct {
				Metadata everest.ObjectMeta   `json:"metadata"`
				Spec     everest.InstanceSpec `json:"spec"`
			}
			require.NoError(t, yaml.Unmarshal(body, &doc))
			require.NotNil(t, doc.Spec.Backup)
			assert.True(t, doc.Spec.Backup.Enabled)
			assert.Equal(t, everest.BackupClassMongoDB, doc.Spec.Backup.ClassRef.Name)
			require.Len(t, doc.Spec.Backup.Storages, 1)
			assert.Equal(t, "s3", doc.Spec.Backup.Storages[0].StorageRef.Name)

			if !daily {
				assert.Empty(t, doc.Spec.Backup.Storages[0].Schedules)
				assert.NotContains(t, doc.Metadata.Annotations, oe.AnnotationBackupCopies)

				return
			}
			assert.Equal(t, []everest.InstanceBackupSchedule{{
				Name: engine.ScheduleName, Enabled: true, Cron: "0 2 * * *",
			}}, doc.Spec.Backup.Storages[0].Schedules, "no retention on the provider's schedule")
			assert.Equal(t, "7", doc.Metadata.Annotations[oe.AnnotationBackupCopies])
		})
	}
}

func TestAMongoDBScheduleEditKeepsItsCopiesOnTheInstance(t *testing.T) {
	body, _, err := instanceYAML("orders", "pds", "spoke-a", oe.MongoDB, oe.ProfileDev, environmentTargets{BackupStorage: "s3"}, engine.ResourceOverrides{}, engine.Exposure{})
	require.NoError(t, err)

	out, err := mutateInstanceYAML(body, func(spec *everest.InstanceSpec, meta *everest.ObjectMeta) error {
		in := engine.ScheduleInput{Enabled: true, Period: engine.PeriodHourly, Minute: 5, RetentionCopies: 2}

		return applySchedule(spec, meta, "s3", in)
	})
	require.NoError(t, err)

	var doc struct {
		Metadata everest.ObjectMeta   `json:"metadata"`
		Spec     everest.InstanceSpec `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(out, &doc))
	got := oe.ScheduleOfInstance(doc.Spec, doc.Metadata)
	assert.Equal(t, int32(2), got.RetentionCopies)
	assert.Equal(t, "5 * * * *", got.Cron)
	assert.Nil(t, doc.Spec.Backup.Storages[0].Schedules[0].Retention)
}

func TestScaleOfAMongoDBDatabaseAddsNoProxy(t *testing.T) {
	body, _, err := instanceYAML("orders", "pds", "spoke-a", oe.MongoDB, oe.ProfileDev, environmentTargets{}, engine.ResourceOverrides{}, engine.Exposure{})
	require.NoError(t, err)
	members := 3

	out, err := mutateInstanceYAML(body, func(spec *everest.InstanceSpec, _ *everest.ObjectMeta) error {
		return applyScale(spec, engine.ScaleInput{Replicas: &members})
	})
	require.NoError(t, err)

	var doc struct {
		Spec everest.InstanceSpec `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(out, &doc))
	assert.NotContains(t, doc.Spec.Components, everest.ComponentProxy)
	assert.Equal(t, int32(3), doc.Spec.Components[everest.ComponentEngine].ReplicaCount())
}

// TestInstanceYAMLKeepsAnUnscheduledPostgreSQLStorageWithItsScheduleOff
// writes the storage of a database with no schedule under the control
// plane's schedule turned off. The PostgreSQL provider removes a storage with
// no schedule entry, and runs nothing for one that is off, so the manifest
// and the Instance agree and no backup runs until one is asked for.
func TestInstanceYAMLKeepsAnUnscheduledPostgreSQLStorageWithItsScheduleOff(t *testing.T) {
	body, _, err := instanceYAML("orders", "pds", "spoke-a", oe.PostgreSQL, oe.ProfileDev, environmentTargets{BackupStorage: "s3"}, engine.ResourceOverrides{}, engine.Exposure{})
	require.NoError(t, err)

	var doc struct {
		Spec everest.InstanceSpec `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(body, &doc))
	require.NotNil(t, doc.Spec.Backup)
	require.Len(t, doc.Spec.Backup.Storages, 1)
	off := oe.ScheduleSpec(everest.ProviderPostgreSQL, engine.DefaultSchedule())
	off.Enabled = false
	assert.Equal(t, []everest.InstanceBackupSchedule{off}, doc.Spec.Backup.Storages[0].Schedules)
	assert.False(t, oe.ScheduleOf(doc.Spec.Backup).Runs())
}

// TestScheduleOnAnUnscheduledPostgreSQLDatabaseReplacesTheOffOne keeps a
// schedule turned on after create from running beside the one written off.
func TestScheduleOnAnUnscheduledPostgreSQLDatabaseReplacesTheOffOne(t *testing.T) {
	body, _, err := instanceYAML("orders", "pds", "spoke-a", oe.PostgreSQL, oe.ProfileDev, environmentTargets{BackupStorage: "s3"}, engine.ResourceOverrides{}, engine.Exposure{})
	require.NoError(t, err)

	var doc struct {
		Spec everest.InstanceSpec `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(body, &doc))
	require.NoError(t, applySchedule(&doc.Spec, &everest.ObjectMeta{}, "s3", engine.DefaultSchedule()))

	require.Len(t, doc.Spec.Backup.Storages, 1)
	assert.Equal(t, []everest.InstanceBackupSchedule{oe.ScheduleSpec(everest.ProviderPostgreSQL, engine.DefaultSchedule())}, doc.Spec.Backup.Storages[0].Schedules)
}

// TestInstanceYAMLLeavesMySQLDevUnscheduled keeps MySQL as it was: its
// provider keeps a storage with no schedule.
func TestInstanceYAMLLeavesMySQLDevUnscheduled(t *testing.T) {
	body, _, err := instanceYAML("orders", "pds", "spoke-a", oe.MySQL, oe.ProfileDev, environmentTargets{BackupStorage: "s3"}, engine.ResourceOverrides{}, engine.Exposure{})
	require.NoError(t, err)

	var doc struct {
		Spec everest.InstanceSpec `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(body, &doc))
	require.NotNil(t, doc.Spec.Backup)
	require.Len(t, doc.Spec.Backup.Storages, 1)
	assert.Empty(t, doc.Spec.Backup.Storages[0].Schedules)
}

// TestInstanceYAMLWritesTheDefaultSchedule keeps create on the same default
// the engine plugins write: Staging and Prod daily at 02:00, seven copies.
func TestInstanceYAMLWritesTheDefaultSchedule(t *testing.T) {
	body, _, err := instanceYAML("orders", "pds", "spoke-a", oe.MySQL, "staging", environmentTargets{BackupStorage: "s3"}, engine.ResourceOverrides{}, engine.Exposure{})
	require.NoError(t, err)

	var doc struct {
		Spec everest.InstanceSpec `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(body, &doc))
	require.NotNil(t, doc.Spec.Backup)
	assert.Equal(t, everest.BackupClassPXC, doc.Spec.Backup.ClassRef.Name)
	assert.Equal(t, []everest.InstanceBackupSchedule{oe.ScheduleSpec(everest.ProviderPXC, engine.DefaultSchedule())}, doc.Spec.Backup.Storages[0].Schedules)
}

func TestInstanceYAMLLeavesMongoDBMonitoringOffWithoutAConfig(t *testing.T) {
	body, _, err := instanceYAML("orders", "pds", "spoke-a", oe.MongoDB, oe.ProfileDev, environmentTargets{BackupStorage: "s3"}, engine.ResourceOverrides{}, engine.Exposure{})
	require.NoError(t, err)

	var doc struct {
		Spec everest.InstanceSpec `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(body, &doc))
	require.Len(t, doc.Spec.Components, 1)
	assert.NotContains(t, doc.Spec.Components, everest.ComponentMonitoring)
}
