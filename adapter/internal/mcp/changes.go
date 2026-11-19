package mcp

import (
	"context"
	"regexp"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/percona/pds/adapter/internal/auth"
)

const (
	maxSourceRanges = 32
	defaultEngine   = "mysql"
)

const (
	msgSourceRangesRequired  = "A public database needs sourceRanges. Ask the user which addresses may connect. Do not choose them yourself."
	msgOpenToInternet        = "These source ranges open the database to the whole internet. Tell the user that any address can connect, and set acknowledgeOpenToInternet to true only if they still want that."
	msgOpenToInternetWarning = "Any address on the internet can connect to this database."
	msgConfirmMismatch       = "The confirm value does not match the name of this database, so nothing was changed. Ask the user to type the database name to confirm, and pass exactly that."
	msgScaleNeedsOne         = "Set at least one of cpu, memoryGb, storageGb or members."
	msgScaleNote             = "The sizes in database may still be the old ones. Call wait_for_database with expect to follow the change."

	msgCreateCheck   = "Call list_databases to see whether it was created."
	msgScaleCheck    = "Call get_database to see whether the size changed."
	msgBackupCheck   = "Call list_backups to see whether a backup started."
	msgScheduleCheck = "Call get_database to see the backup schedule."
	msgRestoreCheck  = "Call list_databases to see whether the new database exists."
	msgDeleteCheck   = "Call get_database to see whether it still exists."
)

// nextCall is the call that follows a change, with its arguments filled in.
type nextCall struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

// changed is what every change answers with: it was accepted, not finished.
type changed struct {
	Accepted  bool            `json:"accepted" jsonschema:"Always true: the change was accepted, and may not be finished."`
	Operation string          `json:"operation"`
	ID        string          `json:"id"`
	Status    string          `json:"status,omitempty"`
	Requested map[string]int  `json:"requested,omitempty" jsonschema:"The sizes that were asked for."`
	Database  *database       `json:"database,omitempty" jsonschema:"The database as it was when the change was sent."`
	Note      string          `json:"note,omitempty"`
	BackupID  string          `json:"backupId,omitempty"`
	Schedule  *backupSchedule `json:"schedule,omitempty" jsonschema:"The backup schedule that was written."`
	Warnings  []string        `json:"warnings,omitempty"`
	// Provisioning is how the run a change started stands, for a managed
	// environment.
	Provisioning *provisioning `json:"provisioning,omitempty" jsonschema:"How the run this change started stands."`
	Next         *nextCall     `json:"next,omitempty" jsonschema:"The call to make next to follow the change."`
}

func accepted(operation, id string) changed {
	return changed{Accepted: true, Operation: operation, ID: id}
}

func waitForReady(id string) *nextCall {
	return &nextCall{Tool: "wait_for_database", Arguments: map[string]any{"id": id, "until": untilReady}}
}

type createDatabaseInput struct {
	Engine                    string   `json:"engine,omitempty" jsonschema:"The database engine. Defaults to mysql."`
	Name                      string   `json:"name" jsonschema:"Lowercase letters, digits and dashes, at most 63 characters, 37 for postgresql."`
	Environment               string   `json:"environment" jsonschema:"The environment to create it on, from list_environments."`
	Profile                   string   `json:"profile" jsonschema:"Sets the default size."`
	CPU                       *int     `json:"cpu,omitempty" jsonschema:"CPU cores."`
	MemoryGB                  *int     `json:"memoryGb,omitempty" jsonschema:"Memory in GB."`
	StorageGB                 *int     `json:"storageGb,omitempty" jsonschema:"Storage in GB."`
	Access                    string   `json:"access,omitempty" jsonschema:"Defaults to private."`
	SourceRanges              []string `json:"sourceRanges,omitempty" jsonschema:"Required for public access: the IP addresses or CIDR ranges that may connect."`
	AcknowledgeOpenToInternet bool     `json:"acknowledgeOpenToInternet,omitempty" jsonschema:"Set to true only after the user chose an open range such as 0.0.0.0/0."`
}

// createDatabaseBody is the API's request, which names the sizes as it always
// has: memory and storage.
type createDatabaseBody struct {
	Name         string   `json:"name"`
	Engine       string   `json:"engine"`
	Profile      string   `json:"profile"`
	Environment  string   `json:"environment"`
	CPU          *int     `json:"cpu,omitempty"`
	Memory       *int     `json:"memory,omitempty"`
	Storage      *int     `json:"storage,omitempty"`
	Access       string   `json:"access,omitempty"`
	SourceRanges []string `json:"sourceRanges,omitempty"`
}

type scaleDatabaseInput struct {
	ID        string `json:"id" jsonschema:"A database id, copied exactly from list_databases."`
	CPU       *int   `json:"cpu,omitempty" jsonschema:"CPU cores."`
	MemoryGB  *int   `json:"memoryGb,omitempty" jsonschema:"Memory in GB."`
	StorageGB *int   `json:"storageGb,omitempty" jsonschema:"Storage in GB. It can only increase."`
	Members   *int   `json:"members,omitempty" jsonschema:"The number of members: 1, 3 or 5."`
}

type scaleDatabaseBody struct {
	CPU      *int `json:"cpu,omitempty"`
	Memory   *int `json:"memory,omitempty"`
	Storage  *int `json:"storage,omitempty"`
	Replicas *int `json:"replicas,omitempty"`
}

type backupScheduleInput struct {
	ID              string `json:"id" jsonschema:"A database id, copied exactly from list_databases."`
	Enabled         bool   `json:"enabled" jsonschema:"True turns the schedule on, false turns it off."`
	Period          string `json:"period,omitempty" jsonschema:"Required when enabled."`
	Minute          *int   `json:"minute,omitempty" jsonschema:"The minute, 0 to 59. Defaults to 0."`
	Hour            *int   `json:"hour,omitempty" jsonschema:"The hour, 0 to 23. Required for daily and weekly."`
	Weekday         *int   `json:"weekday,omitempty" jsonschema:"The weekday, 0 for Sunday to 6. Required for weekly."`
	RetentionCopies *int   `json:"retentionCopies,omitempty" jsonschema:"How many backups to keep, 1 to 30. Defaults to 7."`
}

type backupScheduleBody struct {
	Enabled         bool   `json:"enabled"`
	Period          string `json:"period,omitempty"`
	Minute          *int   `json:"minute,omitempty"`
	Hour            *int   `json:"hour,omitempty"`
	Weekday         *int   `json:"weekday,omitempty"`
	RetentionCopies *int   `json:"retentionCopies,omitempty"`
}

type restoreDatabaseInput struct {
	ID       string `json:"id" jsonschema:"A database id, copied exactly from list_databases."`
	BackupID string `json:"backupId" jsonschema:"A succeeded backup, from list_backups."`
	Name     string `json:"name" jsonschema:"The new database name: lowercase letters, digits and dashes, at most 19 characters."`
}

type restoreDatabaseBody struct {
	BackupID string `json:"backupId"`
	Name     string `json:"name"`
}

type restored struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// deleteDatabaseInput leaves confirm optional in the schema, so that a call
// with none is refused by the tool in the same words as a wrong one.
type deleteDatabaseInput struct {
	ID      string `json:"id" jsonschema:"A database id, copied exactly from list_databases."`
	Confirm string `json:"confirm,omitempty" jsonschema:"The database name, typed by the user. It must match exactly."`
}

func databaseChanges() []tool {
	return []tool{
		define(spec{
			name:  "create_database",
			title: "Create a database",
			description: "Create a MySQL or PostgreSQL database on one environment of this control plane. " +
				"engine is mysql or postgresql and defaults to mysql: ask the user when they have not said. " +
				"environment is required: call list_environments and ask the user which one. " +
				"profile sets the default size: dev is 1 member, 1 CPU, 2 GB memory, 20 GB storage and no high availability; " +
				"staging is 3 members, 2 CPU, 8 GB and 100 GB; prod is 3 members, 4 CPU, 16 GB and 250 GB. " +
				"cpu, memoryGb and storageGb override the profile within limits: 1 to 64 CPU, 1 to 512 GB memory, 10 to 16000 GB storage. " +
				"Databases are private by default, for both engines: reachable only on private address ranges, through an internal load balancer. " +
				"For access public you must ask the user which addresses may connect and pass them as sourceRanges: never choose 0.0.0.0/0 yourself. " +
				"If the user chooses it, warn them that any address on the internet can connect. A client that can ask the user asks them to confirm, and the call goes on only on their yes, whatever acknowledgeOpenToInternet says. For any other client, pass acknowledgeOpenToInternet true after the user confirms. " +
				"The call returns before the database is ready: use wait_for_database. " +
				"A new database starts as pending until the environment picks it up.",
			annotations: changing(false, false),
			access:      auth.AccessChange,
			tune: func(input, _ *jsonschema.Schema) {
				input.Properties["engine"].Enum = []any{"mysql", "postgresql"}
				input.Properties["profile"].Enum = []any{"dev", "staging", "prod"}
				input.Properties["access"].Enum = []any{"private", "public"}
				limit := maxSourceRanges
				input.Properties["sourceRanges"].MaxItems = &limit
			},
		}, createDatabase),

		define(spec{
			name:  "scale_database",
			title: "Scale a database",
			description: "Change the size of a database: CPU cores, memory in GB, storage in GB (increase only) or members (1, 3 or 5). " +
				"Set at least one. Members restart one at a time. Refused while a backup is running. " +
				"The call returns before the change is applied: use wait_for_database with expect.",
			annotations: changing(true, false),
			access:      auth.AccessChange,
		}, scaleDatabase),

		define(spec{
			name:  "backup_database",
			title: "Back up a database",
			description: "Start an on-demand backup of a database. Returns a backupId right away: use wait_for_backup to follow it. " +
				"Fails with not available when backups are not set up for the environment.",
			annotations: changing(false, false),
			access:      auth.AccessChange,
		}, backupDatabase),

		define(spec{
			name:  "set_backup_schedule",
			title: "Set the backup schedule",
			description: "Turn the backup schedule of a database on or off, or change it. enabled is required. " +
				"When enabled, set period to hourly, daily or weekly. daily and weekly also need hour, and weekly needs weekday (0 is Sunday). " +
				"All times are UTC. The result shows the schedule that was written. " +
				"On a control plane with several environments this works only for databases created through it, " +
				"and the change appears on the environment after a short delay: check with get_database.",
			annotations: changing(true, false),
			access:      auth.AccessChange,
			tune: func(input, _ *jsonschema.Schema) {
				input.Properties["period"].Enum = []any{"hourly", "daily", "weekly"}
				bounds(input.Properties["minute"], 0, 59)
				bounds(input.Properties["hour"], 0, 23)
				bounds(input.Properties["weekday"], 0, 6)
				bounds(input.Properties["retentionCopies"], 1, 30)
			},
		}, setBackupSchedule),

		define(spec{
			name:  "restore_database",
			title: "Restore a backup",
			description: "Restore a backup into a new database on the same environment. The original database is left alone. " +
				"name is the new database name. The copy has no public address. " +
				"The call returns before the new database is ready: use wait_for_database on the returned id.",
			annotations: changing(false, false),
			access:      auth.AccessChange,
		}, restoreDatabase),

		define(spec{
			name:  "delete_database",
			title: "Delete a database",
			description: "Delete a database. This cannot be undone. A client that can ask the user asks them to type the database name, and the call goes on only when it matches, whatever confirm says. For any other client, ask the user to confirm and pass the database name as confirm. " +
				"The call returns before the database is gone: use wait_for_database with until deleted.",
			annotations: changing(false, true),
			access:      auth.AccessChangeDelete,
		}, deleteDatabase),
	}
}

// opensToInternet is a range that admits every address: a prefix of length
// zero, or an address that is all zeros, in either family.
var opensToInternet = regexp.MustCompile(`(?:/0+|^(?:0\.0\.0\.0|::))$`)

func createDatabase(ctx context.Context, c *call, in createDatabaseInput) (changed, error) {
	if in.Access == "public" && len(in.SourceRanges) == 0 {
		return changed{}, refuse(msgSourceRangesRequired)
	}
	open := false
	for _, source := range in.SourceRanges {
		open = open || opensToInternet.MatchString(strings.TrimSpace(source))
	}
	if open {
		if err := c.confirmOpen(in); err != nil {
			return changed{}, err
		}
	}

	var created database
	err := c.post(ctx, "/api/databases", createDatabaseBody{
		Name: in.Name, Engine: orDefault(in.Engine, defaultEngine), Profile: in.Profile, Environment: in.Environment,
		CPU: in.CPU, Memory: in.MemoryGB, Storage: in.StorageGB, Access: in.Access, SourceRanges: in.SourceRanges,
	}, msgCreateCheck, &created)
	if err != nil {
		return changed{}, err
	}

	out := accepted("create_database", created.ID)
	out.Status = created.Status
	out.Next = waitForReady(created.ID)
	if open {
		out.Warnings = []string{msgOpenToInternetWarning}
	}

	return out, nil
}

// confirmOpen is the user's word that any address may connect. A client that
// can ask the person asks them, and the model's acknowledgement counts for
// nothing there. Any other client falls back to the acknowledgement, which the
// model sets after asking.
func (c *call) confirmOpen(in createDatabaseInput) error {
	if !c.canAsk {
		if !in.AcknowledgeOpenToInternet {
			return &toolError{Kind: kindConfirmationRequired, Message: msgOpenToInternet}
		}

		return nil
	}

	shown := append([]string{"open", in.Engine, in.Name, in.Environment}, in.SourceRanges...)
	for _, value := range shown[2:] {
		if !plainText.MatchString(strings.TrimSpace(value)) {
			return refuse(msgInvalidInput)
		}
	}
	answer, err := c.ask(openQuestion(in), shown...)
	if err != nil {
		return err
	}
	if confirmed, _ := answer.Content["confirm"].(bool); !saidYes(answer) || !confirmed {
		return &toolError{Kind: kindConfirmationRequired, Message: msgOpenDeclined}
	}

	return nil
}

func scaleDatabase(ctx context.Context, c *call, in scaleDatabaseInput) (changed, error) {
	path, err := databasePath(in.ID)
	if err != nil {
		return changed{}, err
	}
	requested := map[string]int{}
	for name, size := range map[string]*int{"cpu": in.CPU, "memoryGb": in.MemoryGB, "storageGb": in.StorageGB, "members": in.Members} {
		if size != nil {
			requested[name] = *size
		}
	}
	if len(requested) == 0 {
		return changed{}, refuse(msgScaleNeedsOne)
	}

	var scaled database
	err = c.post(ctx, "/api/databases/"+path+"/scale", scaleDatabaseBody{
		CPU: in.CPU, Memory: in.MemoryGB, Storage: in.StorageGB, Replicas: in.Members,
	}, msgScaleCheck, &scaled)
	if err != nil {
		return changed{}, err
	}

	out := accepted("scale_database", scaled.ID)
	out.Status, out.Requested, out.Database, out.Note = scaled.Status, requested, &scaled, msgScaleNote
	out.Next = &nextCall{Tool: "wait_for_database", Arguments: map[string]any{"id": scaled.ID, "until": untilReady, "expect": requested}}

	return out, nil
}

func backupDatabase(ctx context.Context, c *call, in databaseInput) (changed, error) {
	path, err := databasePath(in.ID)
	if err != nil {
		return changed{}, err
	}

	var started backup
	if err := c.post(ctx, "/api/databases/"+path+"/backup", nil, msgBackupCheck, &started); err != nil {
		return changed{}, err
	}

	out := accepted("backup_database", in.ID)
	out.BackupID = started.ID
	out.Next = &nextCall{Tool: "wait_for_backup", Arguments: map[string]any{"id": in.ID, "backupId": started.ID}}

	return out, nil
}

func setBackupSchedule(ctx context.Context, c *call, in backupScheduleInput) (changed, error) {
	path, err := databasePath(in.ID)
	if err != nil {
		return changed{}, err
	}

	var written database
	err = c.post(ctx, "/api/databases/"+path+"/backup-schedule", backupScheduleBody{
		Enabled: in.Enabled, Period: in.Period, Minute: in.Minute, Hour: in.Hour, Weekday: in.Weekday, RetentionCopies: in.RetentionCopies,
	}, msgScheduleCheck, &written)
	if err != nil {
		return changed{}, err
	}

	out := accepted("set_backup_schedule", in.ID)
	out.Schedule = written.BackupSchedule

	return out, nil
}

func restoreDatabase(ctx context.Context, c *call, in restoreDatabaseInput) (changed, error) {
	path, err := databasePath(in.ID)
	if err != nil {
		return changed{}, err
	}

	var copied restored
	if err := c.post(ctx, "/api/databases/"+path+"/restore", restoreDatabaseBody{BackupID: in.BackupID, Name: in.Name}, msgRestoreCheck, &copied); err != nil {
		return changed{}, hintedAs(err, msgBackupNotFoundHint)
	}

	out := accepted("restore_database", copied.ID)
	out.Status = copied.Status
	out.Next = waitForReady(copied.ID)

	return out, nil
}

// deleteDatabase never trusts the model's claim of the name: the name is read
// from the control plane and compared, and a mismatch sends nothing.
func deleteDatabase(ctx context.Context, c *call, in deleteDatabaseInput) (changed, error) {
	path, err := databasePath(in.ID)
	if err != nil {
		return changed{}, err
	}

	var stored database
	if err := c.get(ctx, "/api/databases/"+path, nil, &stored); err != nil {
		return changed{}, err
	}
	if err := c.confirmDelete(stored, in); err != nil {
		return changed{}, err
	}
	if err := c.remove(ctx, "/api/databases/"+path, msgDeleteCheck); err != nil {
		return changed{}, err
	}

	out := accepted("delete_database", in.ID)
	out.Next = &nextCall{Tool: "wait_for_database", Arguments: map[string]any{"id": in.ID, "until": untilDeleted}}

	return out, nil
}

// confirmDelete is the user's word on the stored name. A client that can ask
// the person asks them to type it, and the model's confirm counts for nothing
// there. Any other client falls back to confirm, which the model fills after
// asking.
func (c *call) confirmDelete(stored database, in deleteDatabaseInput) error {
	if !c.canAsk {
		if in.Confirm == "" || in.Confirm != stored.Name {
			return refuse(msgConfirmMismatch)
		}

		return nil
	}

	answer, err := c.ask(deleteQuestion(stored), "delete", in.ID, stored.Name)
	if err != nil {
		return err
	}
	if !saidYes(answer) {
		return &toolError{Kind: kindConfirmationRequired, Message: msgDeleteDeclined}
	}
	if typed, _ := answer.Content["name"].(string); typed == "" || typed != stored.Name {
		return refuse(msgTypedNameMismatch)
	}

	return nil
}
