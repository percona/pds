package mcp

import (
	"context"

	"github.com/percona/pds/adapter/internal/auth"
)

type environment struct {
	Name      string `json:"name" jsonschema:"Pass this as environment to the tools that take one."`
	Namespace string `json:"namespace,omitempty"`
	Status    string `json:"status"`
	LastSeen  string `json:"lastSeen,omitempty" jsonschema:"When the environment last reported in."`
	Origin    string `json:"origin" jsonschema:"connected when the user connected the Kubernetes cluster, managed when PDS built it."`
	State     string `json:"state" jsonschema:"creating, ready, degraded, failed, deleting or lost. lost means the Kubernetes cluster is gone from the AWS account: the user rebuilds it or removes it from PDS."`
	// ConnectionID, Region, Provisioning and Lifecycle are set on a managed environment.
	ConnectionID string        `json:"connectionId,omitempty" jsonschema:"The AWS account a managed environment runs in."`
	Region       string        `json:"region,omitempty"`
	Provisioning *provisioning `json:"provisioning,omitempty" jsonschema:"How the latest run stands: its phases and, when it failed, what to do."`
	Lifecycle    *lifecycle    `json:"lifecycle,omitempty" jsonschema:"Upgrades, changes made outside PDS and loss of a managed environment."`
}

type maintenanceWindow struct {
	Day       int    `json:"day" jsonschema:"0 for Sunday to 6 for Saturday."`
	Hour      int    `json:"hour" jsonschema:"0 to 23, in timeZone."`
	TimeZone  string `json:"timeZone"`
	NextStart string `json:"nextStart" jsonschema:"The window that is open now, or the next one."`
	NextEnd   string `json:"nextEnd"`
}

type driftItem struct {
	Resource string `json:"resource" jsonschema:"The resource in the AWS account that was changed."`
	Detail   string `json:"detail" jsonschema:"Changed outside PDS or Deleted outside PDS."`
}

// lifecycle is what the control plane knows of a managed environment's upgrades,
// drift and loss, as the hourly check last read it.
type lifecycle struct {
	ChartVersion               string            `json:"chartVersion,omitempty" jsonschema:"The PDS version on the environment."`
	PendingChartVersion        string            `json:"pendingChartVersion,omitempty" jsonschema:"A newer PDS version that upgrades in the maintenance window, or now with upgrade_environment."`
	SkippedChartVersion        string            `json:"skippedChartVersion,omitempty" jsonschema:"A version that failed here and was rolled back. It is not tried again until the user retries."`
	Deferred                   bool              `json:"deferred" jsonschema:"The user put pendingChartVersion off until deferredUntil."`
	DeferredUntil              string            `json:"deferredUntil,omitempty"`
	MaintenanceWindow          maintenanceWindow `json:"maintenanceWindow"`
	TemplateRevision           int               `json:"templateRevision"`
	LatestTemplateRevision     int               `json:"latestTemplateRevision"`
	TemplateOutOfDate          bool              `json:"templateOutOfDate" jsonschema:"The infrastructure template is older than the one this PDS ships. upgrade_environment with kind kubernetes and no kubernetesVersion updates it."`
	TemplateSummary            string            `json:"templateSummary,omitempty" jsonschema:"Tell the user in these words, for example Template revision 1, latest 2."`
	KubernetesVersion          string            `json:"kubernetesVersion,omitempty"`
	KubernetesUpgradeAvailable bool              `json:"kubernetesUpgradeAvailable"`
	Drift                      []driftItem       `json:"drift" jsonschema:"What was changed outside PDS, by resource. PDS only reports it and never overwrites it."`
	DriftCheckedAt             string            `json:"driftCheckedAt,omitempty"`
	Lost                       bool              `json:"lost"`
	LostReason                 string            `json:"lostReason,omitempty" jsonschema:"Infrastructure missing."`
	LostAt                     string            `json:"lostAt,omitempty"`
}

// tidy makes the lists a tool answers with arrays, never null.
func (e *environment) tidy() {
	if e.Provisioning != nil {
		e.Provisioning.Phases = orEmpty(e.Provisioning.Phases)
	}
	if e.Lifecycle != nil {
		e.Lifecycle.Drift = orEmpty(e.Lifecycle.Drift)
	}
}

type refusal struct {
	Reason  string `json:"reason" jsonschema:"quota, permission or region_not_enabled."`
	Service string `json:"service,omitempty"`
	Item    string `json:"item,omitempty" jsonschema:"The quota or the action AWS refused."`
	Region  string `json:"region,omitempty"`
}

type runFailure struct {
	Sentence string   `json:"sentence" jsonschema:"What happened and what the user does next. Tell the user in these words."`
	Action   string   `json:"action" jsonschema:"retry: call retry_environment_run once the user has fixed what the sentence says."`
	Refusal  *refusal `json:"refusal,omitempty" jsonschema:"Set when AWS refused a step and the user fixes it in their own AWS account."`
}

type phase struct {
	Name   string `json:"name"`
	Status string `json:"status" jsonschema:"pending, running, waiting, succeeded or failed."`
}

type runView struct {
	ID         uint   `json:"id"`
	Kind       string `json:"kind" jsonschema:"create, upgrade_pds, upgrade_kubernetes, rebuild or delete."`
	Status     string `json:"status"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

// provisioning is how the latest run of a managed environment stands.
type provisioning struct {
	Run      runView     `json:"run"`
	Progress string      `json:"progress" jsonschema:"in_progress, waiting, failed, blocked or ready. waiting is a run paused until the AWS account answers again. blocked is a delete that left something behind."`
	Message  string      `json:"message,omitempty"`
	Phases   []phase     `json:"phases" jsonschema:"The four phases of a create, in order."`
	Failure  *runFailure `json:"failure,omitempty"`
}

// runStarted is what the API answers when it accepts a run.
type runStarted struct {
	Run          runView      `json:"run"`
	Provisioning provisioning `json:"provisioning"`
}

type environments struct {
	Environments []environment `json:"environments"`
}

// noInput is the arguments of a tool that takes none.
type noInput struct{}

func environmentReads() []tool {
	return []tool{
		define(spec{
			name:  "list_environments",
			title: "List environments",
			description: "List the environments of this control plane, with their status and when each was last seen. " +
				"origin says whether the user connected the Kubernetes cluster or PDS built the environment, and state says how it is. " +
				"A managed environment also carries provisioning: the four phases of its latest run and, when a run failed, what to do, " +
				"and lifecycle: a newer PDS version, the maintenance window, changes made outside PDS and whether its infrastructure is lost. " +
				"Use an environment name when creating a database.",
			annotations: readOnly(),
		}, listEnvironments),

		define(spec{
			name:  "get_environment",
			title: "Get an environment",
			description: "Get one environment with its status, origin and state. " +
				"A managed environment carries provisioning: its latest run, the four phases (Creating network and Kubernetes cluster, Connecting to the environment, Installing PDS, Finishing) and, when a run failed, " +
				"failure with the sentence to tell the user and the one action. When AWS refused a step, failure.refusal says which quota or permission and where. " +
				"lifecycle names a pending PDS version, its deferral, the maintenance window, templateSummary when the infrastructure template is old, kubernetesUpgradeAvailable, " +
				"drift by resource (changed outside PDS: report it, PDS never overwrites it) and lostReason when the Kubernetes cluster is gone. " +
				"When state is lost, ask the user whether to rebuild it with rebuild_environment. Removing it from PDS only is for the user in the console.",
			annotations: readOnly(),
		}, getEnvironment),
	}
}

type environmentInput struct {
	Name string `json:"name" jsonschema:"The environment name, copied exactly from list_environments."`
}

func getEnvironment(ctx context.Context, c *call, in environmentInput) (environment, error) {
	path, err := environmentPath(in.Name)
	if err != nil {
		return environment{}, err
	}
	var out environment
	if err := c.get(ctx, "/api/environments/"+path, nil, &out); err != nil {
		return environment{}, hintedAs(err, msgEnvironmentNotFoundHint)
	}
	out.tidy()

	return out, nil
}

func waitForEnvironmentCall(name string) *nextCall {
	return &nextCall{Tool: "wait_for_environment", Arguments: map[string]any{"name": name}}
}

func listEnvironments(ctx context.Context, c *call, _ noInput) (environments, error) {
	var out environments
	if err := c.get(ctx, "/api/environments", nil, &out); err != nil {
		return environments{}, err
	}
	out.Environments = orEmpty(out.Environments)
	for i := range out.Environments {
		out.Environments[i].tidy()
	}

	return out, nil
}

const (
	msgEnvironmentExists        = "An environment with that name already exists. Choose another name, or use list_environments to see the existing ones."
	msgEnvironmentNote          = "Ask the user to open consoleUrl, enter the same name and run the command it creates. The command is not shown here."
	msgEnvironmentNoteNoConsole = "Ask the user to add the environment in the console, enter the same name and run the command it creates. The command is not shown here."
	msgEnvironmentCreateCheck   = "Call list_environments to see whether the environment was registered."
)

type createEnvironmentInput struct {
	Name string `json:"name" jsonschema:"The environment name, which is also the EKS cluster name."`
}

// environmentChanged is the envelope of create_environment, with where the
// user goes next.
type environmentChanged struct {
	changed
	ExpiresAt  string `json:"expiresAt,omitempty" jsonschema:"When the command the console creates stops working."`
	ConsoleURL string `json:"consoleUrl,omitempty" jsonschema:"Where the user adds the environment."`
}

// registeredEnvironment is the part of the answer to a join request that a tool
// may read. The answer carries the join token and the command that holds it,
// and neither is a field here, so neither can leave this package.
type registeredEnvironment struct {
	ExpiresAt string `json:"expiresAt"`
}

func environmentChanges() []tool {
	return []tool{
		define(spec{
			name:  "create_environment",
			title: "Create an environment",
			description: "Register a new environment. name is also the name of the EKS cluster the user creates, so ask the user for it. " +
				"Refused when an environment with that name already exists. " +
				"This call does not connect anything: it returns consoleUrl. " +
				"Ask the user to open it, enter the same name and run the command it creates. " +
				"The command is never shown here. Then use wait_for_environment.",
			annotations: changing(false, false),
			access:      auth.AccessChange,
		}, createEnvironment),
	}
}

func createEnvironment(ctx context.Context, c *call, in createEnvironmentInput) (environmentChanged, error) {
	var listed environments
	if err := c.get(ctx, "/api/environments", nil, &listed); err != nil {
		return environmentChanged{}, err
	}
	for _, e := range listed.Environments {
		if e.Name == in.Name {
			return environmentChanged{}, &toolError{Kind: kindConflict, Message: msgEnvironmentExists}
		}
	}

	var registered registeredEnvironment
	err := c.post(ctx, "/api/connect/tokens", struct {
		Name string `json:"name"`
	}{in.Name}, msgEnvironmentCreateCheck, &registered)
	if err != nil {
		return environmentChanged{}, err
	}

	out := environmentChanged{changed: accepted("create_environment", in.Name), ExpiresAt: registered.ExpiresAt}
	out.Note = msgEnvironmentNoteNoConsole
	if c.registry.consoleURL != "" {
		out.ConsoleURL, out.Note = c.registry.consoleURL+"/environments/new", msgEnvironmentNote
	}
	out.Next = &nextCall{Tool: "wait_for_environment", Arguments: map[string]any{"name": in.Name}}

	return out, nil
}
