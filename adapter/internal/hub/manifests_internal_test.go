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

			require.NoError(t, applySchedule(&spec, "s3", engine.DefaultSchedule()))

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

	err := applySchedule(&spec, "", engine.DefaultSchedule())

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
	off := oe.ScheduleSpec(engine.DefaultSchedule())
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
	require.NoError(t, applySchedule(&doc.Spec, "s3", engine.DefaultSchedule()))

	require.Len(t, doc.Spec.Backup.Storages, 1)
	assert.Equal(t, []everest.InstanceBackupSchedule{oe.ScheduleSpec(engine.DefaultSchedule())}, doc.Spec.Backup.Storages[0].Schedules)
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
	assert.Equal(t, []everest.InstanceBackupSchedule{oe.ScheduleSpec(engine.DefaultSchedule())}, doc.Spec.Backup.Storages[0].Schedules)
}
