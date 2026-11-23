package oe

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
)

type sizing struct {
	Replicas  int32
	CPUCores  int
	MemoryGB  int
	StorageGB int
}

func formatCores(cores int) string {
	return strconv.Itoa(cores)
}

func formatGi(gb int) string {
	return strconv.Itoa(gb) + "Gi"
}

func instanceID(namespace, name string) string {
	return fmt.Sprintf("%s.%s", namespace, name)
}

func splitID(id string) (namespace, name string, err error) {
	namespace, name, found := strings.Cut(strings.TrimSpace(id), ".")
	if !found || namespace == "" || name == "" || strings.Contains(name, ".") {
		return "", "", fmt.Errorf("%w: %q must be namespace.name", ErrInvalidID, id)
	}

	return namespace, name, nil
}

func proxyReplicasFor(engineReplicas int32) int32 {
	if engineReplicas <= 1 {
		return 1
	}

	return engineReplicas
}

func (p *Plugin) toInstance(in *everest.Instance, fallbackNamespace string) engine.Instance {
	namespace := in.Metadata.Namespace
	if namespace == "" {
		namespace = fallbackNamespace
	}

	profile := labelValue(in.Metadata.Labels, LabelProfile, LegacyLabelProfile)
	if profile == "" {
		profile = ProfileCustom
	}

	eng := in.EngineComponent()
	out := engine.Instance{
		ID:        instanceID(namespace, in.Metadata.Name),
		Name:      in.Metadata.Name,
		Engine:    p.flavor.ID,
		Profile:   profile,
		Namespace: namespace,
		Status:    toStatus(in.Status),
	}
	if in.Metadata.CreationTimestamp != nil {
		out.CreatedAt = in.Metadata.CreationTimestamp.UTC()
	}

	out.Spec = engine.InstanceSpec{
		Components: engine.InstanceComponents{
			Engine: engine.EngineComponent{
				Replicas:  eng.ReplicaCount(),
				Resources: resourcesOf(eng),
			},
		},
	}
	out.Sizing = sizingOf(eng)
	out.BackupSchedule = ScheduleOfInstance(in.Spec, in.Metadata)
	out.Exposure = ExposureOf(in)

	return out
}

// The load balancer scheme annotations the AWS cloud provider and the AWS Load
// Balancer Controller read. The in-tree provider only knows the internal flag,
// and the controller defaults to internal when the scheme is unset, so a
// private load balancer sets both and a public one names its scheme.
const (
	AnnotationAWSLoadBalancerInternal = "service.beta.kubernetes.io/aws-load-balancer-internal"
	AnnotationAWSLoadBalancerScheme   = "service.beta.kubernetes.io/aws-load-balancer-scheme"

	awsSchemeInternal       = "internal"
	awsSchemeInternetFacing = "internet-facing"
)

// LoadBalancerServiceFor is the proxy service that puts a database behind a
// cloud load balancer: internal for a private database, internet-facing for a
// public one. A private one admits the private address ranges only, so a
// cloud that ignores the annotations still does not open it to the internet.
// A public one always names its allow list. Open to the internet is
// 0.0.0.0/0, never an omitted list.
func LoadBalancerServiceFor(exposure engine.Exposure) *everest.ServiceSpec {
	svc := &everest.ServiceSpec{ServiceType: everest.ServiceTypeLoadBalancer}
	if exposure.Private {
		svc.Annotations = map[string]string{
			AnnotationAWSLoadBalancerInternal: "true",
			AnnotationAWSLoadBalancerScheme:   awsSchemeInternal,
		}
		svc.LoadBalancerService = &everest.LoadBalancerService{
			SourceRanges: append([]string(nil), engine.PrivateSourceRanges...),
		}

		return svc
	}

	svc.Annotations = map[string]string{AnnotationAWSLoadBalancerScheme: awsSchemeInternetFacing}
	ranges := exposure.SourceRanges
	if len(ranges) == 0 {
		ranges = []string{engine.OpenInternetRange}
	}
	svc.LoadBalancerService = &everest.LoadBalancerService{
		SourceRanges: append([]string(nil), ranges...),
	}

	return svc
}

// ExposureOf reads the service the providers act on: the proxy's when it sets
// one, the engine's otherwise. The address is not in the spec; the plugin
// fills it once the published connection names one.
//
// A load balancer with no scheme annotation reads as public. Every one written
// before private access existed was asked for as public and carries none, and
// the in-tree AWS provider builds those internet-facing. A private one reports
// no ranges: they are the safety net above, not an allow list anyone set.
func ExposureOf(in *everest.Instance) engine.Exposure {
	svc := in.ProxyComponent().Service
	if svc == nil {
		svc = in.EngineComponent().Service
	}
	if svc == nil || svc.ServiceType != everest.ServiceTypeLoadBalancer {
		return engine.Exposure{}
	}

	out := engine.Exposure{External: true, Private: privateLoadBalancer(svc.Annotations)}
	if !out.Private && svc.LoadBalancerService != nil && len(svc.LoadBalancerService.SourceRanges) > 0 {
		out.SourceRanges = append([]string(nil), svc.LoadBalancerService.SourceRanges...)
	}

	return out
}

func privateLoadBalancer(annotations map[string]string) bool {
	if strings.EqualFold(strings.TrimSpace(annotations[AnnotationAWSLoadBalancerScheme]), awsSchemeInternal) {
		return true
	}
	internal, err := strconv.ParseBool(strings.TrimSpace(annotations[AnnotationAWSLoadBalancerInternal]))

	return err == nil && internal
}

// inClusterHost reports a published host only the cluster can resolve. Until a
// load balancer has an ingress, the provider publishes a Service name in the
// namespace, and handing that to a client outside the cluster as its address
// would be a connection string that cannot work.
//
// A single label with no colon is a bare Service name, which resolves only
// through the cluster's search domains. An IPv6 address has colons and no dots.
func inClusterHost(host, namespace string) bool {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if host == "" || serviceNameFromHost(host, namespace) != "" {
		return true
	}
	if !strings.ContainsAny(host, ".:") {
		return true
	}

	return strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local")
}

func resourcesOf(e everest.ComponentSpec) engine.EngineResources {
	return engine.EngineResources{
		CPU:     e.CPU(),
		Memory:  e.Memory(),
		Storage: e.StorageSize(),
	}
}

func sizingOf(e everest.ComponentSpec) engine.Sizing {
	out := engine.Sizing{Members: int(e.ReplicaCount())}

	res := resourcesOf(e)
	if cores, err := parseCores(res.CPU); err == nil {
		out.CPUCores = cores
	}
	if memory, err := parseSizeGB(res.Memory); err == nil {
		out.MemoryGB = memory
	}
	if storage, err := parseSizeGB(res.Storage); err == nil {
		out.StorageGB = storage
	}

	return out
}

func toStatus(in everest.InstanceStatus) engine.Status {
	message := ""
	if in.Phase == everest.PhaseProvisioning || in.Phase == everest.PhaseInitializing {
		message = startBlockerMessage(in, time.Now())
	}
	if message == "" {
		message = strings.TrimSpace(in.Message)
	}
	if message == "" {
		message = stageMessage(in)
	}
	// A provider that refuses an Instance reports Failed with no message, and
	// its reason is only in its own log.
	if message == "" && in.Phase == everest.PhaseFailed {
		message = failedFallbackMessage
	}

	return engine.Status{Phase: toPhase(in), Message: message}
}

const failedFallbackMessage = "The database could not be started. Contact Percona support."

func stageMessage(in everest.InstanceStatus) string {
	ready, total := in.ReadyCounts()

	switch in.Phase {
	case "", everest.PhasePending:
		return "Waiting to start"
	case everest.PhaseProvisioning:
		if ready <= 0 {
			return "Creating volumes"
		}

		if total <= 0 {
			total = ready
		}

		return fmt.Sprintf("Starting member %d of %d", ready, total)
	case everest.PhaseInitializing:
		if in.ConnectionSecretName() == "" {
			return "Starting database"
		}

		return "Publishing connection"
	case everest.PhaseReady:
		if in.ConnectionSecretName() == "" {
			return "Publishing connection"
		}

		return ""
	default:
		return ""
	}
}

func toPhase(in everest.InstanceStatus) string {
	switch in.Phase {
	case "", everest.PhasePending:
		return engine.PhasePending
	case everest.PhaseProvisioning:
		ready, _ := in.ReadyCounts()
		if ready > 0 {
			return engine.PhaseInitializing
		}

		return engine.PhaseProvisioning
	case everest.PhaseInitializing:
		return engine.PhaseInitializing
	case everest.PhaseReady:
		return engine.PhaseReady
	case everest.PhaseUpdating:
		return engine.PhaseUpdating
	case everest.PhaseTerminating:
		return engine.PhaseDeleting
	case everest.PhaseFailed:
		return engine.PhaseFailed
	case everest.PhaseRestoring:
		return engine.PhaseRestoring
	case everest.PhaseSuspending:
		return engine.PhaseSuspending
	case everest.PhaseSuspended:
		return engine.PhaseSuspended
	case everest.PhaseResuming:
		return engine.PhaseResuming
	default:
		return engine.PhaseUnknown
	}
}

func (p *Plugin) toBackup(in everest.Backup) engine.Backup {
	out := engine.Backup{
		ID:        in.Metadata.Name,
		Status:    toBackupState(in.Status.State),
		Scheduled: in.Spec.ScheduleName != "",
		Message:   in.Status.Message,
		Instance:  in.Spec.InstanceName(),

		FinishOnly: p.flavor.FinishOnly,
	}
	if in.Status.CompletedAt != nil {
		out.FinishedAt = in.Status.CompletedAt.UTC()
	}

	switch {
	case in.Status.StartedAt != nil:
		out.CreatedAt = in.Status.StartedAt.UTC()
	case in.Metadata.CreationTimestamp != nil:
		out.CreatedAt = in.Metadata.CreationTimestamp.UTC()
	}

	if in.Status.Size != nil {
		if size, err := parseBytes(*in.Status.Size); err == nil {
			out.SizeBytes = int64(size)
		}
	}

	return out
}

func newestSucceededAt(backups []everest.Backup) *time.Time {
	var newest time.Time
	found := false
	for _, backup := range backups {
		if toBackupState(backup.Status.State) != engine.BackupSucceeded {
			continue
		}
		at := backupFinishedAt(backup)
		if at.IsZero() {
			continue
		}
		if !found || at.After(newest) {
			newest = at
			found = true
		}
	}
	if !found {
		return nil
	}
	out := newest.UTC()

	return &out
}

func backupFinishedAt(in everest.Backup) time.Time {
	switch {
	case in.Status.CompletedAt != nil:
		return in.Status.CompletedAt.UTC()
	case in.Status.StartedAt != nil:
		return in.Status.StartedAt.UTC()
	case in.Metadata.CreationTimestamp != nil:
		return in.Metadata.CreationTimestamp.UTC()
	default:
		return time.Time{}
	}
}

func toBackupState(state string) string {
	switch state {
	case "", everest.BackupStatePending:
		return engine.BackupPending
	case everest.BackupStateRunning:
		return engine.BackupRunning
	case everest.BackupStateSucceeded:
		return engine.BackupSucceeded
	case everest.BackupStateFailed, everest.BackupStateError:
		return engine.BackupFailed
	case everest.BackupStateDeleting:
		return engine.BackupDeleting
	default:
		return engine.BackupUnknown
	}
}

func (p *Plugin) toConnection(conn *everest.Connection, namespace, secretName string) *engine.Connection {
	if conn == nil || conn.Host == "" {
		return nil
	}

	out := &engine.Connection{
		Host:        conn.Host,
		Port:        int(conn.Port),
		Username:    conn.Username,
		SecretName:  secretName,
		ServiceName: serviceNameFromHost(conn.Host, namespace),
	}

	if conn.URI != "" {
		out.URI = conn.URI

		return out
	}

	// JoinHostPort brackets an IPv6 host, which a load balancer can hand out
	// and which a URI only parses in brackets.
	host := conn.Host
	if conn.Port > 0 {
		host = net.JoinHostPort(conn.Host, strconv.Itoa(int(conn.Port)))
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	uri := url.URL{
		Scheme: p.flavor.URIScheme,
		User:   url.UserPassword(conn.Username, conn.Password),
		Host:   host,
	}
	out.URI = uri.String()

	return out
}

var byteSuffixes = []struct {
	suffix string
	factor float64
}{
	{"Ki", 1 << 10},
	{"Mi", 1 << 20},
	{"Gi", 1 << 30},
	{"Ti", 1 << 40},
	{"Pi", 1 << 50},
	{"k", 1e3},
	{"M", 1e6},
	{"G", 1e9},
	{"T", 1e12},
	{"P", 1e15},
}

var sizeSuffixes = []struct {
	suffix string
	gb     float64
}{
	{"Ki", 1.0 / (1 << 20)},
	{"Mi", 1.0 / (1 << 10)},
	{"Gi", 1},
	{"Ti", 1 << 10},
	{"Pi", 1 << 20},
	{"k", 1.0 / (1 << 20)},
	{"M", 1.0 / (1 << 10)},
	{"G", 1},
	{"T", 1 << 10},
	{"P", 1 << 20},
}

func parseSizeGB(raw string) (float64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("%w: quantity is blank", ErrUnparsableQuantity)
	}

	for _, s := range sizeSuffixes {
		digits, found := strings.CutSuffix(value, s.suffix)
		if !found {
			continue
		}

		parsed, err := strconv.ParseFloat(strings.TrimSpace(digits), 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %q", ErrUnparsableQuantity, raw)
		}

		return round2(parsed * s.gb), nil
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrUnparsableQuantity, raw)
	}

	return round2(parsed / (1 << 30)), nil
}

func parseBytes(raw string) (float64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("%w: quantity is blank", ErrUnparsableQuantity)
	}

	for _, s := range byteSuffixes {
		digits, found := strings.CutSuffix(value, s.suffix)
		if !found {
			continue
		}

		parsed, err := strconv.ParseFloat(strings.TrimSpace(digits), 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %q", ErrUnparsableQuantity, raw)
		}

		return parsed * s.factor, nil
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrUnparsableQuantity, raw)
	}

	return parsed, nil
}

func parseCores(raw string) (float64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("%w: quantity is blank", ErrUnparsableQuantity)
	}

	if millis, found := strings.CutSuffix(value, "m"); found {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(millis), 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %q", ErrUnparsableQuantity, raw)
		}

		return round2(parsed / 1000), nil
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrUnparsableQuantity, raw)
	}

	return round2(parsed), nil
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// serviceNameFromHost reads the Service out of a published connection host.
//
// A provider publishes an in-cluster address, so the first label is the
// Service and the second is the namespace it lives in. Requiring the second
// label to match means an external DNS name, a bare IP or a host from another
// namespace yields nothing at all. That is deliberate: a port-forward command
// built on a wrong Service name fails with a kubectl error that reads like the
// database is broken, so no command beats a plausible one.
func serviceNameFromHost(host, namespace string) string {
	if host == "" || namespace == "" {
		return ""
	}

	labels := strings.Split(host, ".")
	if len(labels) < 2 || labels[1] != namespace {
		return ""
	}

	return labels[0]
}

// schedulingGrace is how long PodsScheduled may stay False before it is shown.
// A healthy start sets it for the seconds a volume takes to bind or a node to
// join, so a shorter wait would flash a capacity warning at databases that
// start fine.
const schedulingGrace = 2 * time.Minute

// startBlockerMessage says why pods cannot be placed, as a sentence the console
// can show, once the start is stuck. It is empty for a healthy start, whose
// PodsReady stays False until the last member is up. The provider's own reason
// and message name its internals, so they are read only to pick a fixed
// sentence and are never returned.
func startBlockerMessage(in everest.InstanceStatus, now time.Time) string {
	blocker, ok := in.SchedulingBlocker(schedulingGrace, now)
	if !ok {
		return ""
	}

	if blocker.Reason == "Unschedulable" {
		return unschedulableMessage(blocker.Message)
	}

	return "Pods are waiting for a node. Check that the Kubernetes cluster has capacity."
}

// unschedulableMessage tells the causes of an Unschedulable pod apart by the
// scheduler's text, which the provider passes through. Storage is checked
// first because a claim that cannot bind also makes every node look
// unsuitable, and capacity before placement because a mix of full and tainted
// nodes is fixed by adding capacity. A message that matches nothing gets the
// generic sentence.
func unschedulableMessage(scheduler string) string {
	text := strings.ToLower(scheduler)
	has := func(parts ...string) bool {
		for _, part := range parts {
			if strings.Contains(text, part) {
				return true
			}
		}

		return false
	}

	switch {
	case has("persistentvolumeclaim", "persistent volume", "volume node affinity", "storageclass", "storage class"):
		return "Pods cannot be scheduled. Storage cannot be provisioned. Check that the Kubernetes cluster has a working storage class."
	case has("insufficient cpu", "insufficient memory"):
		return "Pods cannot be scheduled. Check that the Kubernetes cluster has enough CPU and memory."
	case has("taint", "affinity", "selector"):
		return "Pods cannot be scheduled. No node in the Kubernetes cluster can run the members. Check node taints and labels, or add nodes."
	default:
		return "Pods cannot be scheduled. Check the nodes and storage of the Kubernetes cluster."
	}
}
