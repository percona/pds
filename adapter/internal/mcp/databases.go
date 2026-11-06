package mcp

import (
	"context"
	"net/url"
)

type connection struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	MaskedURI   string `json:"maskedUri,omitempty" jsonschema:"The connection string with the password hidden. Absent when that password stays on the Kubernetes cluster."`
	SecretName  string `json:"secretName,omitempty"`
	ServiceName string `json:"serviceName,omitempty"`
}

type access struct {
	External     bool     `json:"external" jsonschema:"True when the database has an address outside the Kubernetes cluster."`
	Visibility   string   `json:"visibility" jsonschema:"Who can reach the database: private, public, or a value that means only from inside the Kubernetes cluster."`
	SourceRanges []string `json:"sourceRanges,omitempty"`
	Address      string   `json:"address,omitempty"`
}

type backupSchedule struct {
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	Cron            string `json:"cron"`
	RetentionCopies int    `json:"retentionCopies,omitempty"`
	Period          string `json:"period,omitempty"`
	Minute          *int   `json:"minute,omitempty"`
	Hour            *int   `json:"hour,omitempty"`
	Weekday         *int   `json:"weekday,omitempty"`
}

// database is one database as get_database returns it. The API's team field is
// not here: a tool acts in one Team.
type database struct {
	ID             string          `json:"id" jsonschema:"Pass this exactly to the tools that take an id."`
	Name           string          `json:"name"`
	Engine         string          `json:"engine"`
	Profile        string          `json:"profile" jsonschema:"One of dev, staging, prod or custom."`
	Namespace      string          `json:"namespace"`
	Environment    string          `json:"environment,omitempty"`
	Status         string          `json:"status"`
	Message        string          `json:"message,omitempty" jsonschema:"The reason when something blocks the database."`
	Members        int             `json:"members"`
	CPU            float64         `json:"cpu"`
	MemoryGB       float64         `json:"memoryGb"`
	StorageGB      float64         `json:"storageGb"`
	CreatedAt      string          `json:"createdAt,omitempty"`
	LastBackupAt   string          `json:"lastBackupAt,omitempty"`
	BackupSchedule *backupSchedule `json:"backupSchedule,omitempty"`
	Connection     *connection     `json:"connection,omitempty" jsonschema:"Absent until the connection is published."`
	Access         access          `json:"access"`
}

// databaseRow is the short form a list carries.
type databaseRow struct {
	ID          string `json:"id" jsonschema:"Pass this exactly to the tools that take an id."`
	Name        string `json:"name"`
	Engine      string `json:"engine"`
	Profile     string `json:"profile" jsonschema:"One of dev, staging, prod or custom."`
	Status      string `json:"status"`
	Members     int    `json:"members"`
	Environment string `json:"environment,omitempty"`
}

type databaseRows struct {
	Databases []databaseRow `json:"databases"`
}

type backup struct {
	ID         string `json:"id"`
	Status     string `json:"status" jsonschema:"One of pending, running, succeeded, failed, deleting or unknown."`
	SizeBytes  int64  `json:"sizeBytes"`
	Scheduled  bool   `json:"scheduled" jsonschema:"True when the backup schedule started it."`
	CreatedAt  string `json:"createdAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Message    string `json:"message,omitempty"`
}

type backups struct {
	Backups []backup `json:"backups"`
}

type listDatabasesInput struct {
	Environment string `json:"environment,omitempty" jsonschema:"Only the databases on this environment."`
}

// databaseInput is the arguments of a tool that takes one database. The id's
// shape is checked by the tool and not the schema, so a bad one comes back as
// the same invalid_input result as every other failure.
type databaseInput struct {
	ID string `json:"id" jsonschema:"A database id, copied exactly from list_databases."`
}

func databaseReads() []tool {
	return []tool{
		define(spec{
			name:  "list_databases",
			title: "List databases",
			description: "List the databases on every connected environment, or only on one environment. " +
				"Each row is short: use get_database for details. " +
				"Environment state can be a little behind the real environment.",
			annotations: readOnly(),
		}, listDatabases),

		define(spec{
			name:  "get_database",
			title: "Get a database",
			description: "Get one database: status, size, backup schedule, who can connect, " +
				"and connection details without the password. " +
				"A database is usable when status is ready and connection is present. " +
				"If message names a blocking problem, tell the user.",
			annotations: readOnly(),
		}, getDatabase),

		define(spec{
			name:  "list_backups",
			title: "List backups",
			description: "List the backups of a database, newest first. " +
				"Fails with not available when backups are not set up for the environment.",
			annotations: readOnly(),
		}, listBackups),
	}
}

func listDatabases(ctx context.Context, c *call, in listDatabasesInput) (databaseRows, error) {
	var query url.Values
	if in.Environment != "" {
		query = url.Values{"environment": {in.Environment}}
	}

	var answer struct {
		Databases []database `json:"databases"`
	}
	if err := c.get(ctx, "/api/databases", query, &answer); err != nil {
		return databaseRows{}, err
	}

	rows := make([]databaseRow, 0, len(answer.Databases))
	for _, d := range answer.Databases {
		rows = append(rows, databaseRow{
			ID: d.ID, Name: d.Name, Engine: d.Engine, Profile: d.Profile,
			Status: d.Status, Members: d.Members, Environment: d.Environment,
		})
	}

	return databaseRows{Databases: rows}, nil
}

func getDatabase(ctx context.Context, c *call, in databaseInput) (database, error) {
	path, err := databasePath(in.ID)
	if err != nil {
		return database{}, err
	}

	var out database
	if err := c.get(ctx, "/api/databases/"+path, nil, &out); err != nil {
		return database{}, err
	}

	return out, nil
}

func listBackups(ctx context.Context, c *call, in databaseInput) (backups, error) {
	path, err := databasePath(in.ID)
	if err != nil {
		return backups{}, err
	}

	var out backups
	if err := c.get(ctx, "/api/databases/"+path+"/backups", nil, &out); err != nil {
		return backups{}, err
	}
	out.Backups = orEmpty(out.Backups)

	return out, nil
}

// orEmpty keeps a list that the API left out as an empty list, so the result
// says there is nothing rather than null.
func orEmpty[T any](list []T) []T {
	if list == nil {
		return []T{}
	}

	return list
}
