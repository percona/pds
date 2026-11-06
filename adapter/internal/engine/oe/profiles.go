package oe

import (
	"fmt"
	"strings"

	"github.com/percona/pds/adapter/internal/engine"
)

// Profile identifiers the catalog offers. These are the wire values the UI
// sends and the canonical spelling stored on an Instance.
const (
	ProfileDev     = "dev"
	ProfileStaging = "staging"
	ProfileProd    = "prod"
)

// Backup schedules a profile can carry.
const (
	BackupOnDemand = "on-demand"
	BackupDaily    = "daily"
)

// Override bounds. Advanced overrides are validated here because the client is
// never trusted. The floors keep a cluster schedulable, the ceilings keep one
// team from claiming a whole cluster by typing an extra zero.
const (
	MinCPUCores = 1
	MaxCPUCores = 64

	MinMemoryGB = 1
	MaxMemoryGB = 512

	MinStorageGB = 10
	MaxStorageGB = 16000
)

// memberChoices is every member count a clustered engine can run. Galera
// decides quorum by majority, so a PXC cluster is one node or an odd number of
// them. PostgreSQL can run even replica counts, but the UI offers the same
// choices so the scale dialog stays one table.
var memberChoices = []int{1, 3, 5}

// MemberChoices returns the member counts Scale accepts. The copy keeps a
// caller from editing the shared list.
func MemberChoices() []int {
	out := make([]int, len(memberChoices))
	copy(out, memberChoices)

	return out
}

// catalog is the profile table, in the order the UI shows it.
var catalog = []engine.Profile{
	{
		Name:           ProfileDev,
		Members:        1,
		CPUCores:       1,
		MemoryGB:       2,
		StorageGB:      20,
		HA:             false,
		BackupSchedule: BackupOnDemand,
	},
	{
		Name:           ProfileStaging,
		Members:        3,
		CPUCores:       2,
		MemoryGB:       8,
		StorageGB:      100,
		HA:             true,
		BackupSchedule: BackupDaily,
	},
	{
		Name:           ProfileProd,
		Members:        3,
		CPUCores:       4,
		MemoryGB:       16,
		StorageGB:      250,
		HA:             true,
		BackupSchedule: BackupDaily,
	},
}

func lookupProfile(name string) (engine.Profile, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, p := range catalog {
		if p.Name == want {
			return p, nil
		}
	}

	return engine.Profile{}, fmt.Errorf("%w: %q, want dev, staging, or prod", engine.ErrUnknownProfile, name)
}

func resolveSizing(p engine.Profile, o engine.ResourceOverrides) (sizing, error) {
	s := sizing{
		Replicas:  int32(p.Members),
		CPUCores:  p.CPUCores,
		MemoryGB:  p.MemoryGB,
		StorageGB: p.StorageGB,
	}

	if o.CPUCores != nil {
		s.CPUCores = *o.CPUCores
	}
	if o.MemoryGB != nil {
		s.MemoryGB = *o.MemoryGB
	}
	if o.StorageGB != nil {
		s.StorageGB = *o.StorageGB
	}

	if err := s.validate(); err != nil {
		return sizing{}, err
	}

	return s, nil
}

func (s sizing) validate() error {
	if err := checkBounds(FieldCPU, s.CPUCores, MinCPUCores, MaxCPUCores, UnitCores); err != nil {
		return err
	}
	if err := checkBounds(FieldMemory, s.MemoryGB, MinMemoryGB, MaxMemoryGB, UnitGB); err != nil {
		return err
	}

	return checkBounds(FieldStorage, s.StorageGB, MinStorageGB, MaxStorageGB, UnitGB)
}

const (
	FieldCPU     = "CPU"
	FieldMemory  = "Memory"
	FieldStorage = "Storage"
	FieldMembers = "Members"

	UnitCores = "cores"
	UnitGB    = "GB"
)

func checkBounds(field string, value, minValue, maxValue int, unit string) error {
	if value < minValue || value > maxValue {
		return &engine.BoundsError{
			Field: field,
			Unit:  unit,
			Value: value,
			Min:   minValue,
			Max:   maxValue,
		}
	}

	return nil
}

func checkMembers(value int) error {
	for _, allowed := range memberChoices {
		if value == allowed {
			return nil
		}
	}

	return &engine.ChoiceError{Field: FieldMembers, Value: value, Allowed: MemberChoices()}
}

// StorageState is the instance's current disk size. Scale uses it to reject
// a request that would shrink storage, the same way OpenEverest does.
type StorageState struct {
	Present bool
	GB      float64
}

// CheckScale is the server-side scale gate. The OpenEverest plugin and the
// hub both call it so a GitOps scale cannot write values a spoke would reject.
func CheckScale(in engine.ScaleInput, current StorageState, backupRunning bool) error {
	if in.CPUCores == nil && in.MemoryGB == nil && in.StorageGB == nil && in.Replicas == nil {
		return fmt.Errorf("%w: set cpu, memory, storage, or replicas", engine.ErrEmptyScale)
	}
	if in.CPUCores != nil {
		if err := checkBounds(FieldCPU, *in.CPUCores, MinCPUCores, MaxCPUCores, UnitCores); err != nil {
			return err
		}
	}
	if in.MemoryGB != nil {
		if err := checkBounds(FieldMemory, *in.MemoryGB, MinMemoryGB, MaxMemoryGB, UnitGB); err != nil {
			return err
		}
	}
	if in.StorageGB != nil {
		if !current.Present {
			return fmt.Errorf("%w: engine has no storage", engine.ErrUnparsableQuantity)
		}
		if err := checkBounds(FieldStorage, *in.StorageGB, MinStorageGB, MaxStorageGB, UnitGB); err != nil {
			return err
		}
		if float64(*in.StorageGB) < current.GB {
			return fmt.Errorf("%w: %d GB is smaller than the current %g GB",
				engine.ErrStorageShrink, *in.StorageGB, current.GB)
		}
	}
	if in.Replicas != nil {
		if err := checkMembers(*in.Replicas); err != nil {
			return err
		}
	}
	if backupRunning {
		return &engine.BusyError{Operation: "backup"}
	}

	return nil
}

// ParseSizeGB reads a Kubernetes quantity as gigabytes. Hub scale uses it to
// apply the same storage-growth rule the OpenEverest path does.
func ParseSizeGB(raw string) (float64, error) {
	return parseSizeGB(raw)
}
