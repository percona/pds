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
			description: "List the environments connected to this control plane, with their status and when each was last seen. " +
				"Use an environment name when creating a database.",
			annotations: readOnly(),
		}, listEnvironments),
	}
}

func listEnvironments(ctx context.Context, c *call, _ noInput) (environments, error) {
	var out environments
	if err := c.get(ctx, "/api/environments", nil, &out); err != nil {
		return environments{}, err
	}
	out.Environments = orEmpty(out.Environments)

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
