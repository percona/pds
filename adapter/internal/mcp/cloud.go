package mcp

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/percona/pds/adapter/internal/auth"
)

const (
	msgInvalidConnectionID    = "That is not an AWS account id. Copy an id exactly as list_cloud_connections returns it."
	msgConnectionNotFoundHint = "Call list_cloud_connections for the current ids."
	msgNotManaged             = "That environment was connected by the user, so PDS did not build it. The user manages it in the console."
	msgNothingToRetry         = "That environment has no failed run to retry. Call get_environment to see where it stands."
	msgConnectionInUse        = "Managed environments still run through that AWS account. Delete them first, then ask again."
	msgConnectionConfirm      = "The name that was typed does not match the name of this AWS account, so nothing was changed. Ask the user to type the AWS account name to confirm, and pass exactly that."
	msgConnectionDeclined     = "The user did not confirm removing the AWS account, so nothing was changed. Do not try again unless the user asks."
	msgConnectionTypedWrong   = "The name that was typed does not match the name of this AWS account, so nothing was changed."
	msgEnvironmentTypedWrong  = "The name that was typed does not match the name of this environment, so nothing was changed."
	msgManagedConfirmMismatch = "The confirm value does not match the name of this environment, so nothing was changed. Ask the user to type the environment name to confirm, and pass exactly that."
	msgStartConnectionNote    = "Ask the user to open consoleUrl and follow the steps: they run one command in their AWS account. The command is not shown here. Then use get_cloud_connection to follow it."
	msgStartConnectionNoLink  = "Ask the user to open the console, choose to connect an AWS account and run the command it shows. The command is not shown here. Then use get_cloud_connection to follow it."

	msgManagedCreateCheck = "Call get_environment to see whether the environment was created."
	msgRetryCheck         = "Call get_environment to see whether the run started again."
	msgUpgradeCheck       = "Call get_environment to see whether the run started."
	msgRebuildCheck       = "Call get_environment to see whether the run started."
	msgManagedDeleteCheck = "Call get_environment to see whether the environment is being deleted."
	msgRemoveCheck        = "Call get_cloud_connection to see whether the AWS account still exists."
)

// connectionID is what the control plane names an AWS account: twelve hex
// digits. It comes from a model, so it is checked before it joins a path.
var connectionID = regexp.MustCompile(`^[a-f0-9]{12}$`)

func connectionPath(id string) (string, error) {
	if !connectionID.MatchString(id) {
		return "", refuse(msgInvalidConnectionID)
	}

	return url.PathEscape(id), nil
}

type cloudStep struct {
	Step   string `json:"step"`
	Status string `json:"status" jsonschema:"started, succeeded or failed."`
	At     string `json:"at,omitempty"`
}

// cloudConnection is an AWS account the Team connected. The API sends no secret
// and no role, and this type names none.
type cloudConnection struct {
	ID             string      `json:"id" jsonschema:"Pass this as connectionId or id to the tools that take one."`
	AccountID      string      `json:"accountId"`
	Alias          string      `json:"alias,omitempty"`
	State          string      `json:"state" jsonschema:"pending, ready, unreachable or out_of_date. Only a ready AWS account can host a new environment."`
	Reason         string      `json:"reason,omitempty" jsonschema:"Why the state is not ready, as a sentence for the user."`
	AllowedRegions []string    `json:"allowedRegions"`
	Environments   []string    `json:"environments" jsonschema:"The managed environments that run through this AWS account."`
	CheckedAt      string      `json:"checkedAt,omitempty"`
	ReadyAt        string      `json:"readyAt,omitempty"`
	Steps          []cloudStep `json:"steps,omitempty" jsonschema:"What the connect command reported, oldest first."`
}

type cloudConnections struct {
	CloudConnections []cloudConnection `json:"cloudConnections"`
}

type cloudConnectionInput struct {
	ID string `json:"id" jsonschema:"An AWS account id, copied exactly from list_cloud_connections."`
}

type cloudConnectionStarted struct {
	changed
	ConsoleURL       string            `json:"consoleUrl,omitempty" jsonschema:"Where the user connects an AWS account."`
	CloudConnections []cloudConnection `json:"cloudConnections" jsonschema:"The AWS accounts that are already connected, and their state."`
}

func cloudReads() []tool {
	return []tool{
		define(spec{
			name:  "list_cloud_connections",
			title: "List AWS accounts",
			description: "List the AWS accounts connected to this team, with their state and the managed environments that run through each. " +
				"State is pending, ready, unreachable or out_of_date. Only a ready AWS account can host a new environment. " +
				"reason says in a sentence why an account is not ready.",
			annotations: readOnly(),
		}, listCloudConnections),

		define(spec{
			name:  "get_cloud_connection",
			title: "Get an AWS account",
			description: "Get one connected AWS account with its state, the regions it allows, its managed environments and the steps the connect command reported. " +
				"id is copied from list_cloud_connections. Nothing here lets you repair an account: tell the user what reason says and link them to the console.",
			annotations: readOnly(),
		}, getCloudConnection),
	}
}

func listCloudConnections(ctx context.Context, c *call, _ noInput) (cloudConnections, error) {
	var out cloudConnections
	if err := c.get(ctx, "/api/cloud-connections", nil, &out); err != nil {
		return cloudConnections{}, err
	}
	out.CloudConnections = orEmpty(out.CloudConnections)
	for i := range out.CloudConnections {
		out.CloudConnections[i].AllowedRegions = orEmpty(out.CloudConnections[i].AllowedRegions)
		out.CloudConnections[i].Environments = orEmpty(out.CloudConnections[i].Environments)
	}

	return out, nil
}

func getCloudConnection(ctx context.Context, c *call, in cloudConnectionInput) (cloudConnection, error) {
	path, err := connectionPath(in.ID)
	if err != nil {
		return cloudConnection{}, err
	}
	var out cloudConnection
	if err := c.get(ctx, "/api/cloud-connections/"+path, nil, &out); err != nil {
		return cloudConnection{}, hintedAs(err, msgConnectionNotFoundHint)
	}
	out.AllowedRegions = orEmpty(out.AllowedRegions)
	out.Environments = orEmpty(out.Environments)

	return out, nil
}

type startCloudConnectionInput = noInput

type createManagedEnvironmentInput struct {
	ConnectionID string `json:"connectionId" jsonschema:"A ready AWS account, from list_cloud_connections."`
	Name         string `json:"name" jsonschema:"The environment name: lowercase letters, digits and dashes. It is also the name of the Kubernetes cluster that is built."`
	Region       string `json:"region" jsonschema:"An AWS region the account allows, such as eu-west-1."`
}

type managedEnvironmentBody struct {
	Name   string `json:"name"`
	Region string `json:"region"`
}

type managedEnvironmentCreated struct {
	Environment environment `json:"environment"`
}

type environmentRunInput struct {
	Environment string `json:"environment" jsonschema:"The name of a managed environment, from list_environments."`
}

type upgradeEnvironmentInput struct {
	Environment string `json:"environment" jsonschema:"The name of a managed environment, from list_environments."`
	Kind        string `json:"kind" jsonschema:"pds upgrades PDS on the environment. kubernetes upgrades the Kubernetes version and the nodes, one minor version at a time."`
}

type runBody struct {
	Kind string `json:"kind"`
}

// confirmedDeleteInput leaves confirm optional in the schema, so that a call
// with none is refused by the tool in the same words as a wrong one.
type confirmedDeleteInput struct {
	Environment string `json:"environment" jsonschema:"The name of a managed environment, from list_environments."`
	Confirm     string `json:"confirm,omitempty" jsonschema:"The environment name, typed by the user. It must match exactly."`
}

type removeCloudConnectionInput struct {
	ID      string `json:"id" jsonschema:"An AWS account id, copied exactly from list_cloud_connections."`
	Confirm string `json:"confirm,omitempty" jsonschema:"The AWS account name, typed by the user: its alias, or its account number when it has none."`
}

type confirmBody struct {
	Confirm string `json:"confirm"`
}

func cloudChanges() []tool {
	return []tool{
		define(spec{
			name:  "start_cloud_connection",
			title: "Start connecting an AWS account",
			description: "Start connecting an AWS account so PDS can build environments in it. " +
				"This call does not connect anything: it returns consoleUrl and the AWS accounts that are already connected. " +
				"Ask the user to open consoleUrl and run the one command it shows in their AWS account. " +
				"The command is never shown here. Follow the account with get_cloud_connection.",
			annotations: changing(true, false),
			access:      auth.AccessChange,
		}, startCloudConnection),

		define(spec{
			name:  "create_managed_environment",
			title: "Create a managed environment",
			description: "Build a new environment in a connected AWS account: PDS creates the network and the Kubernetes cluster and installs PDS on it. " +
				"connectionId is a ready AWS account from list_cloud_connections and region is one it allows. Ask the user for the name and the region. " +
				"The call returns before anything is built: use wait_for_environment, which follows the four phases. " +
				"If a phase fails, tell the user what failure says, and call retry_environment_run once they have fixed it.",
			annotations: changing(false, false),
			access:      auth.AccessChange,
		}, createManagedEnvironment),

		define(spec{
			name:  "retry_environment_run",
			title: "Retry a failed run",
			description: "Retry the failed step of an environment's latest run. Only the step that failed runs again. " +
				"Use it after the user has fixed what failure says, such as raising a quota in their AWS account. " +
				"Refused when the environment has no failed run.",
			annotations: changing(false, false),
			access:      auth.AccessChange,
		}, retryEnvironmentRun),

		define(spec{
			name:  "upgrade_environment",
			title: "Upgrade a managed environment",
			description: "Start an upgrade of a managed environment now. kind pds upgrades PDS on it, and kubernetes upgrades the Kubernetes version and the nodes one minor version at a time. " +
				"Refused while the environment has a run in progress. Use wait_for_environment to follow it.",
			annotations: changing(false, false),
			access:      auth.AccessChange,
			tune: func(input, _ *jsonschema.Schema) {
				input.Properties["kind"].Enum = []any{"pds", "kubernetes"}
			},
		}, upgradeEnvironment),

		define(spec{
			name:  "delete_managed_environment",
			title: "Delete a managed environment",
			description: "Delete an environment PDS built, with its databases, its Kubernetes cluster and its network in the AWS account. This cannot be undone. " +
				"A client that can ask the user asks them to type the environment name, and the call goes on only when it matches, whatever confirm says. " +
				"For any other client, ask the user to confirm and pass the environment name as confirm. " +
				"The call returns before the environment is gone: use wait_for_environment with until deleted. " +
				"If something is left behind, the environment shows delete blocked and lists what remains.",
			annotations: changing(false, true),
			access:      auth.AccessChangeDelete,
		}, deleteManagedEnvironment),

		define(spec{
			name:  "rebuild_environment",
			title: "Rebuild a managed environment",
			description: "Rebuild a managed environment whose infrastructure is gone, so PDS builds the Kubernetes cluster again. " +
				"Backups can be restored afterwards. Ask the user first. Use wait_for_environment to follow it.",
			annotations: changing(false, true),
			access:      auth.AccessChangeDelete,
		}, rebuildEnvironment),

		define(spec{
			name:  "remove_cloud_connection",
			title: "Remove an AWS account",
			description: "Remove a connected AWS account from PDS. Nothing is deleted in the AWS account itself. " +
				"Refused while managed environments still run through it. " +
				"A client that can ask the user asks them to type the AWS account name, and the call goes on only when it matches, whatever confirm says. " +
				"For any other client, ask the user to confirm and pass the name as confirm: the alias, or the account number when there is none.",
			annotations: changing(false, true),
			access:      auth.AccessChangeDelete,
		}, removeCloudConnection),
	}
}

func startCloudConnection(ctx context.Context, c *call, _ startCloudConnectionInput) (cloudConnectionStarted, error) {
	listed, err := listCloudConnections(ctx, c, noInput{})
	if err != nil {
		return cloudConnectionStarted{}, err
	}

	out := cloudConnectionStarted{changed: accepted("start_cloud_connection", "cloud-connection"), CloudConnections: listed.CloudConnections}
	out.Note = msgStartConnectionNoLink
	if c.registry.consoleURL != "" {
		out.ConsoleURL, out.Note = c.registry.consoleURL+"/environments/aws-accounts/new", msgStartConnectionNote
	}

	return out, nil
}

func createManagedEnvironment(ctx context.Context, c *call, in createManagedEnvironmentInput) (changed, error) {
	path, err := connectionPath(in.ConnectionID)
	if err != nil {
		return changed{}, err
	}
	var created managedEnvironmentCreated
	err = c.post(ctx, "/api/cloud-connections/"+path+"/environments", managedEnvironmentBody{Name: in.Name, Region: in.Region}, msgManagedCreateCheck, &created)
	if err != nil {
		return changed{}, hintedAs(err, msgConnectionNotFoundHint)
	}

	out := accepted("create_managed_environment", in.Name)
	out.Status = created.Environment.State
	out.Provisioning = created.Environment.Provisioning
	out.Next = waitForEnvironmentCall(in.Name)

	return out, nil
}

func retryEnvironmentRun(ctx context.Context, c *call, in environmentRunInput) (changed, error) {
	path, err := environmentPath(in.Environment)
	if err != nil {
		return changed{}, err
	}
	var current environment
	if err := c.get(ctx, "/api/environments/"+path, nil, &current); err != nil {
		return changed{}, hintedAs(err, msgEnvironmentNotFoundHint)
	}
	if current.Provisioning == nil || current.Provisioning.Failure == nil {
		return changed{}, refuse(msgNothingToRetry)
	}
	runPath := "/api/environments/" + path + "/runs/" + strconv.FormatUint(uint64(current.Provisioning.Run.ID), 10) + "/retry"
	var started runStarted
	if err := c.post(ctx, runPath, nil, msgRetryCheck, &started); err != nil {
		return changed{}, hintedAs(err, msgEnvironmentNotFoundHint)
	}

	out := accepted("retry_environment_run", in.Environment)
	out.Provisioning = &started.Provisioning
	out.Next = waitForEnvironmentCall(in.Environment)

	return out, nil
}

func upgradeEnvironment(ctx context.Context, c *call, in upgradeEnvironmentInput) (changed, error) {
	return startRun(ctx, c, "upgrade_environment", in.Environment, "upgrade_"+in.Kind, msgUpgradeCheck)
}

func rebuildEnvironment(ctx context.Context, c *call, in environmentRunInput) (changed, error) {
	return startRun(ctx, c, "rebuild_environment", in.Environment, "rebuild", msgRebuildCheck)
}

func startRun(ctx context.Context, c *call, operation, name, kind, check string) (changed, error) {
	path, err := environmentPath(name)
	if err != nil {
		return changed{}, err
	}
	var started runStarted
	if err := c.post(ctx, "/api/environments/"+path+"/runs", runBody{Kind: kind}, check, &started); err != nil {
		return changed{}, hintedAs(err, msgEnvironmentNotFoundHint)
	}

	out := accepted(operation, name)
	out.Provisioning = &started.Provisioning
	out.Next = waitForEnvironmentCall(name)

	return out, nil
}

// deleteManagedEnvironment never trusts the model's claim of the name: the name
// is read from the control plane and compared, and a mismatch sends nothing.
// Only an environment PDS built is deleted here, and never with Remove from
// PDS only, which is for a person in the console.
func deleteManagedEnvironment(ctx context.Context, c *call, in confirmedDeleteInput) (changed, error) {
	path, err := environmentPath(in.Environment)
	if err != nil {
		return changed{}, err
	}
	var stored environment
	if err := c.get(ctx, "/api/environments/"+path, nil, &stored); err != nil {
		return changed{}, hintedAs(err, msgEnvironmentNotFoundHint)
	}
	if stored.Origin != "managed" {
		return changed{}, refuse(msgNotManaged)
	}
	if err := c.confirmTyped(
		form(
			"Delete the environment "+stored.Name+" with its databases and everything PDS built for it in the AWS account? This cannot be undone. Type the environment name to confirm.",
			"name", map[string]any{"type": "string", "title": "Environment name", "description": "Type " + stored.Name + " to delete it."},
		),
		[]string{"delete-environment", stored.Name}, stored.Name, in.Confirm, msgManagedConfirmMismatch, msgDeleteDeclined, msgEnvironmentTypedWrong,
	); err != nil {
		return changed{}, err
	}
	var started runStarted
	if err := c.send(ctx, http.MethodDelete, "/api/environments/"+path, confirmBody{Confirm: stored.Name}, msgManagedDeleteCheck, &started); err != nil {
		return changed{}, err
	}

	out := accepted("delete_managed_environment", in.Environment)
	out.Provisioning = &started.Provisioning
	out.Next = &nextCall{Tool: "wait_for_environment", Arguments: map[string]any{"name": in.Environment, "until": untilDeleted}}

	return out, nil
}

// removeCloudConnection reads the AWS account first, so that the name compared
// is the control plane's and a connection with environments is refused before
// the person is asked.
func removeCloudConnection(ctx context.Context, c *call, in removeCloudConnectionInput) (changed, error) {
	path, err := connectionPath(in.ID)
	if err != nil {
		return changed{}, err
	}
	var stored cloudConnection
	if err := c.get(ctx, "/api/cloud-connections/"+path, nil, &stored); err != nil {
		return changed{}, hintedAs(err, msgConnectionNotFoundHint)
	}
	if len(stored.Environments) > 0 {
		return changed{}, &toolError{Kind: kindConflict, Message: msgConnectionInUse}
	}
	name := stored.Alias
	if name == "" {
		name = stored.AccountID
	}
	if err := c.confirmTyped(
		form(
			"Remove the AWS account "+name+" from PDS? Nothing is deleted in the AWS account itself. Type the AWS account name to confirm.",
			"name", map[string]any{"type": "string", "title": "AWS account name", "description": "Type " + name + " to remove it."},
		),
		[]string{"remove-cloud-connection", stored.ID, name}, name, in.Confirm, msgConnectionConfirm, msgConnectionDeclined, msgConnectionTypedWrong,
	); err != nil {
		return changed{}, err
	}
	if err := c.send(ctx, http.MethodDelete, "/api/cloud-connections/"+path, confirmBody{Confirm: name}, msgRemoveCheck, nil); err != nil {
		return changed{}, err
	}

	return accepted("remove_cloud_connection", in.ID), nil
}

// confirmTyped is the user's word on a stored name. A client that can ask the
// person asks them to type it, and the model's confirm counts for nothing there.
// Any other client falls back to confirm, which the model fills after asking.
func (c *call) confirmTyped(question *sdk.ElicitParams, shown []string, stored, confirm, mismatch, declined, typedWrong string) error {
	if !c.canAsk {
		if confirm == "" || confirm != stored {
			return refuse(mismatch)
		}

		return nil
	}
	answer, err := c.ask(question, shown...)
	if err != nil {
		return err
	}
	if !saidYes(answer) {
		return &toolError{Kind: kindConfirmationRequired, Message: declined}
	}
	if typed, _ := answer.Content["name"].(string); typed == "" || typed != stored {
		return refuse(typedWrong)
	}

	return nil
}
