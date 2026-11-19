package store

import "time"

// SchemaRevision is bumped whenever the models change in a way an operator
// needs to be able to see from the database. GORM reconciles the shape on its
// own; this is the number a running deployment reports back.
const SchemaRevision = 20

// schemaStateID is the primary key of the single marker row. The table holds
// one row by construction, so the key is a constant rather than a sequence.
const schemaStateID = 1

// SchemaState records that a process reconciled the schema, and to which
// revision. It is what proves a deployment actually migrated rather than
// merely connecting.
type SchemaState struct {
	ID        int       `gorm:"primaryKey"`
	Revision  int       `gorm:"not null"`
	AppliedAt time.Time `gorm:"not null"`
}

// TableName pins the table name. GORM would pluralize the struct name, and the
// name is part of what an operator reads with psql.
func (SchemaState) TableName() string { return "hub_schema_state" }

// HubEnvironment is one spoke: its namespace, backup storage, last heartbeat
// snapshot and pending backups, as the document the hub already exchanged with
// the ConfigMap that held this before.
//
// The Team leads the primary key, so two Teams naming an environment the same
// hold two rows rather than one shared object.
type HubEnvironment struct {
	TeamID    uint      `gorm:"primaryKey"`
	Name      string    `gorm:"primaryKey;size:253"`
	Document  string    `gorm:"type:jsonb;not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

// TableName pins the table name.
func (HubEnvironment) TableName() string { return "hub_environments" }

// HubToken is one join or agent grant, keyed by the SHA-256 hash of the token.
// The plaintext is returned once when it is minted and never stored, so this
// table cannot hand anyone a working credential.
type HubToken struct {
	TeamID    uint      `gorm:"primaryKey"`
	Hash      string    `gorm:"primaryKey;size:64"`
	Document  string    `gorm:"type:jsonb;not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

// TableName pins the table name.
func (HubToken) TableName() string { return "hub_tokens" }

// HubWork is one environment's outbound queue. A delete or a scale lives here
// between the click and the spoke's next beat, so an empty queue is a row
// holding an empty array rather than a missing row: a later read has to be
// able to tell drained from never seen.
type HubWork struct {
	TeamID      uint      `gorm:"primaryKey"`
	Environment string    `gorm:"primaryKey;size:253"`
	Document    string    `gorm:"type:jsonb;not null"`
	UpdatedAt   time.Time `gorm:"not null"`
}

// TableName pins the table name.
func (HubWork) TableName() string { return "hub_work" }

// HubSupportAccess is one environment's support access record: the address it is
// reached at and the current window. The row holds current state only, because
// the activity log is the history. Like the environment it belongs to, it is
// keyed by Team first.
type HubSupportAccess struct {
	TeamID      uint      `gorm:"primaryKey"`
	Environment string    `gorm:"primaryKey;size:253"`
	Document    string    `gorm:"type:jsonb;not null"`
	UpdatedAt   time.Time `gorm:"not null"`
}

// TableName pins the table name.
func (HubSupportAccess) TableName() string { return "hub_support_access" }

// HubCloneKey is the OpenSSH identity Flux on one environment clones with. It is a
// private key, so the row holds it sealed and the key that opens it lives in a
// Kubernetes Secret rather than in this database.
type HubCloneKey struct {
	TeamID      uint      `gorm:"primaryKey"`
	Environment string    `gorm:"primaryKey;size:253"`
	Sealed      string    `gorm:"type:text;not null"`
	UpdatedAt   time.Time `gorm:"not null"`
}

// TableName pins the table name.
func (HubCloneKey) TableName() string { return "hub_clone_keys" }

// HubConnection is one field of one outbound credential, sealed.
//
// These cannot be hashed the way join tokens are: the adapter replays the real
// ServiceNow, Anthropic and Cursor keys downstream, so it has to be able to read
// them back. One row per field keeps a rotated key from rewriting the fields
// beside it.
type HubConnection struct {
	// OrganizationID is the Organization that owns this credential. Connections
	// are shared by every Team in it.
	OrganizationID uint      `gorm:"primaryKey"`
	Name           string    `gorm:"primaryKey;size:253"`
	Field          string    `gorm:"primaryKey;size:190"`
	Sealed         string    `gorm:"type:text;not null"`
	UpdatedAt      time.Time `gorm:"not null"`
}

// TableName pins the table name.
func (HubConnection) TableName() string { return "hub_connections" }

// PasswordInvite is one single-use, expiring link that sets a password login's
// password. The row is keyed by the SHA-256 hash of the token, so a full read
// of this database hands nobody a working link.
//
// The hash leads the key rather than the login, because the link carries only
// the token: the adapter has to be able to find out who it is for from the
// token alone.
type PasswordInvite struct {
	Hash      string    `gorm:"primaryKey;size:64"`
	UserID    uint      `gorm:"not null;index"`
	ExpiresAt time.Time `gorm:"not null"`
	// ClaimedAt is nil until the link is spent. A spent row is kept rather
	// than deleted so a replay can be refused as spent, which is a different
	// answer from a link that never existed.
	ClaimedAt *time.Time
	CreatedAt time.Time `gorm:"not null"`
}

// TableName pins the table name.
func (PasswordInvite) TableName() string { return "hub_password_invites" }

// Session is one signed-in browser, keyed by the SHA-256 hash of the
// session id the cookie carries.
//
// The row is the authority rather than the cookie, which is what lets a logout
// or a password change end a session immediately on every replica. LastSeenAt
// carries the idle window and ExpiresAt the absolute one.
type Session struct {
	Hash       string    `gorm:"primaryKey;size:64"`
	CreatedAt  time.Time `gorm:"not null"`
	LastSeenAt time.Time `gorm:"not null"`
	ExpiresAt  time.Time `gorm:"not null;index"`

	// Method is how the browser signed in, one of the SignIn constants. A
	// session that predates it holds an empty string, which reads as the
	// password.
	Method string `gorm:"size:16;not null;default:''"`
	// UserID and OrganizationID name the membership the session acts through,
	// for a password login as for an identity provider sign in. The migration
	// fills them in for a session that predates Organizations.
	UserID         uint `gorm:"not null;default:0"`
	OrganizationID uint `gorm:"not null;default:0"`
}

// How a session was opened.
const (
	SignInPassword = "password"
	SignInOIDC     = "oidc"
)

// TableName pins the table name.
func (Session) TableName() string { return "hub_sessions" }

// SessionKey is the key session cookies are signed with, one row for the whole
// deployment, shared by every Organization it serves.
//
// One row rather than one per Organization is deliberate. The signature only says
// that this deployment minted the cookie; which Organization it belongs to is the
// one on the session row it points at. A per Organization key would add nothing,
// because every key sits in this one table behind one database user, so
// reading one Organization's key means reading them all.
//
// It lives here rather than in a Secret for two reasons: every replica has to
// sign with the same key, and a restart must not invalidate every cookie.
// Keeping it beside the session rows costs nothing, because anyone who can
// read this table can write a session row directly anyway. What the signature
// buys is refusing a forged cookie without a database read.
type SessionKey struct {
	ID        int       `gorm:"primaryKey"`
	Key       []byte    `gorm:"type:bytea;not null"`
	CreatedAt time.Time `gorm:"not null"`
}

// TableName pins the table name.
func (SessionKey) TableName() string { return "hub_session_keys" }

// HistoryRow is one fact the control plane kept: an action, a lifecycle
// transition, a backup, a restore, or a Kubernetes event. Rows are not pruned.
// A deleted database stops answering its history route, and the rows stay.
type HistoryRow struct {
	ID     uint64 `gorm:"primaryKey;autoIncrement"`
	TeamID uint   `gorm:"not null;index:idx_history_scope,priority:1;uniqueIndex:uidx_history_object,priority:1,where:object_id <> ''"`
	// UserID is the User who acted. It is NULL for an actor with no User row,
	// such as system, and for a row written before Users acted on events. The
	// row keeps no name: it is read from the User, so a removed one shows as such.
	UserID       *uint
	DatabaseID   string    `gorm:"size:253;not null;index:idx_history_scope,priority:2;uniqueIndex:uidx_history_object,priority:2,where:object_id <> ''"`
	Environment  string    `gorm:"size:253;not null;default:'';uniqueIndex:uidx_history_object,priority:3,where:object_id <> ''"`
	Kind         string    `gorm:"size:32;not null;index:idx_history_scope,priority:3;uniqueIndex:uidx_history_object,priority:4,where:object_id <> ''"`
	At           time.Time `gorm:"not null;index"`
	Actor        string    `gorm:"size:128"`
	Client       string    `gorm:"size:32"`
	Agent        string    `gorm:"size:64"`
	RequestID    string    `gorm:"size:64"`
	Action       string    `gorm:"size:64"`
	Target       string    `gorm:"size:253"`
	Outcome      string    `gorm:"size:32"`
	Detail       string    `gorm:"size:500"`
	Error        string    `gorm:"size:500"`
	ResourceKind string    `gorm:"size:32"`
	ResourceName string    `gorm:"size:253"`
	FromPhase    string    `gorm:"size:32"`
	ToPhase      string    `gorm:"size:32"`
	Message      string    `gorm:"size:500"`
	ObjectID     string    `gorm:"size:253;uniqueIndex:uidx_history_object,priority:5,where:object_id <> ''"`
	Storage      string    `gorm:"size:253"`
	SizeBytes    int64
	StartedAt    *time.Time
	FinishedAt   *time.Time
	DurationMS   int64
	Reason       string `gorm:"size:128"`
	EventType    string `gorm:"size:32"`
	Count        int32
}

// TableName pins the table name.
func (HistoryRow) TableName() string { return "hub_history" }

// UsageSample is one database's resource sample. CPU and memory are nil when
// metrics-server did not answer; StorageBytes still is, and MetricsReason says
// why the processor figures are missing.
type UsageSample struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement"`
	TeamID        uint      `gorm:"not null;index:idx_usage_scope,priority:1"`
	DatabaseID    string    `gorm:"size:253;not null;index:idx_usage_scope,priority:2"`
	Environment   string    `gorm:"size:253;not null;default:''"`
	At            time.Time `gorm:"not null;index"`
	CPUAvg        *float64
	CPUMax        *float64
	MemoryAvg     *float64
	MemoryMax     *float64
	StorageBytes  *int64
	MetricsReason string `gorm:"size:300"`
	// Monitored is true when the agent sent a PMM reading with this sample,
	// so a reading with no values reads back as one rather than as none.
	Monitored             bool `gorm:"not null;default:false"`
	MonitorCPU            *float64
	MonitorMemory         *float64
	MonitorConnections    *float64
	MonitorReplicationLag *float64
	MonitorDisk           *float64
	MonitorPending        string `gorm:"size:64;not null;default:''"`
}

// TableName pins the table name.
func (UsageSample) TableName() string { return "hub_usage_samples" }

// JoinPhase is one step of one environment join. Output is the scrubbed tail of a
// failed step. A run is every phase that shares RunID.
type JoinPhase struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement"`
	TeamID      uint   `gorm:"not null;index:idx_join_scope,priority:1;uniqueIndex:uidx_join_phase,priority:1"`
	Environment string `gorm:"size:253;not null;index:idx_join_scope,priority:2;uniqueIndex:uidx_join_phase,priority:2"`
	RunID       string `gorm:"size:64;not null;index:idx_join_scope,priority:3;uniqueIndex:uidx_join_phase,priority:3"`
	Phase       string `gorm:"size:128;not null;uniqueIndex:uidx_join_phase,priority:4"`
	Outcome     string `gorm:"size:32;not null"`
	StartedAt   time.Time
	FinishedAt  *time.Time
	DurationMS  int64
	Output      string `gorm:"size:4000"`
}

// TableName pins the table name.
func (JoinPhase) TableName() string { return "hub_join_phases" }

// HubChatConversation is one saved chat. The title is sealed, because it is the
// customer's first message and says as much about their databases as the
// messages do. Provider and model are fixed when the conversation starts, so
// every turn in it was answered by the same model.
//
// Subject is who started it. Lists are scoped by Team rather than by subject.
type HubChatConversation struct {
	ID         string `gorm:"primaryKey;size:32"`
	TeamID     uint   `gorm:"not null;index:idx_chat_conversations_scope,priority:1;uniqueIndex:uidx_chat_conversations_request,priority:1,where:request_key <> ''"`
	Subject    string `gorm:"size:128;not null;default:''"`
	Title      string `gorm:"type:text;not null"`
	Provider   string `gorm:"size:32;not null"`
	Model      string `gorm:"size:200;not null"`
	DatabaseID string `gorm:"size:253;not null;default:''"`
	// RequestKey is the id the browser minted for the message that opened
	// this conversation. A retry of that message carries the same key, and
	// the unique index turns it into the conversation already saved rather
	// than a second one. Empty for a conversation opened without one.
	RequestKey string `gorm:"size:64;not null;default:'';uniqueIndex:uidx_chat_conversations_request,priority:2,where:request_key <> ''"`
	// AgentID is the Cursor cloud agent the conversation runs on, kept so a
	// later turn starts a run on it instead of a new agent. Empty for a
	// conversation on another provider, and until its first Cursor turn ends.
	AgentID   string    `gorm:"size:128;not null;default:''"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null;index:idx_chat_conversations_scope,priority:2"`
}

// TableName pins the table name.
func (HubChatConversation) TableName() string { return "hub_chat_conversations" }

// HubChatMessage is one turn of one conversation. Content and actions are
// sealed against the conversation and the sequence, so a turn cannot be moved
// to another place in the thread and still open.
type HubChatMessage struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement"`
	ConversationID string    `gorm:"size:32;not null;uniqueIndex:uidx_chat_messages_sequence,priority:1"`
	TeamID         uint      `gorm:"not null;index"`
	Sequence       int       `gorm:"not null;uniqueIndex:uidx_chat_messages_sequence,priority:2"`
	Role           string    `gorm:"size:16;not null"`
	Content        string    `gorm:"type:text;not null"`
	Actions        string    `gorm:"type:text;not null"`
	CreatedAt      time.Time `gorm:"not null"`
}

// TableName pins the table name.
func (HubChatMessage) TableName() string { return "hub_chat_messages" }

// Role is what a User may do in an Organization.
const (
	// RoleUser works in their own Team.
	RoleUser = "user"
	// RoleAdmin manages the Organization and acts in any of its Teams.
	RoleAdmin = "admin"
)

// Organization is a company: the owner of Teams, Users and shared connections.
type Organization struct {
	ID        uint      `gorm:"primaryKey;autoIncrement"`
	Name      string    `gorm:"size:200;not null"`
	CreatedAt time.Time `gorm:"not null"`
	// CreatedBy is the User who made the Organization, counted against their
	// creation cap. An Organization migrated from the release before Organizations has none.
	CreatedBy uint `gorm:"not null;default:0"`
	// AIAccess is the most agents and the Chat may do for this Organization:
	// read, change or change_delete. The column default gives every existing
	// Organization read when the column is added.
	AIAccess string `gorm:"column:ai_access;size:16;not null;default:read"`
	// AIPasswords lets local agents fetch database passwords.
	AIPasswords bool `gorm:"column:ai_passwords;not null;default:false"`
	// AISQLWrites lets local agents change data with SQL.
	AISQLWrites bool `gorm:"column:ai_sql_writes;not null;default:false"`
}

// TableName pins the table name.
func (Organization) TableName() string { return "hub_organizations" }

// Team is the unit of isolation inside an Organization. Environments belong to
// one Team and cannot move.
type Team struct {
	ID             uint      `gorm:"primaryKey;autoIncrement"`
	OrganizationID uint      `gorm:"not null;uniqueIndex:uidx_teams_name,priority:1"`
	Name           string    `gorm:"size:128;not null;uniqueIndex:uidx_teams_name,priority:2"`
	CreatedAt      time.Time `gorm:"not null"`
}

// TableName pins the table name.
func (Team) TableName() string { return "hub_teams" }

// User is one person. Users are global: Issuer and Subject name the identity
// the sign-in provider vouches for, and the pair is unique. A login carried
// over from the password era has the issuer "password" and the old login name
// as its subject.
type User struct {
	ID       uint   `gorm:"primaryKey;autoIncrement"`
	Issuer   string `gorm:"size:512;not null;uniqueIndex:uidx_users_identity,priority:1"`
	Subject  string `gorm:"size:255;not null;uniqueIndex:uidx_users_identity,priority:2"`
	Username string `gorm:"size:128;not null;default:''"`
	Email    string `gorm:"size:320;not null;default:''"`
	Name     string `gorm:"size:200;not null;default:''"`
	// PasswordHash and Failures are only used by the password login.
	PasswordHash string `gorm:"size:120;not null;default:''"`
	Failures     int    `gorm:"not null;default:0"`
	// LastOrganizationID is the Organization the User acted in last, where
	// their next sign in lands. Zero means none yet.
	LastOrganizationID uint      `gorm:"not null;default:0"`
	CreatedAt          time.Time `gorm:"not null"`
	UpdatedAt          time.Time `gorm:"not null"`
}

// TableName pins the table name.
func (User) TableName() string { return "hub_users" }

// Membership places a User in one Team of one Organization with a role. A
// User holds at most one per Organization.
type Membership struct {
	UserID         uint      `gorm:"primaryKey"`
	OrganizationID uint      `gorm:"primaryKey"`
	TeamID         uint      `gorm:"not null"`
	Role           string    `gorm:"size:16;not null"`
	CreatedAt      time.Time `gorm:"not null"`
}

// TableName pins the table name.
func (Membership) TableName() string { return "hub_memberships" }

// Invitation lets one person join an Organization in a Team with a role. The
// row is keyed by the SHA-256 hash of the token, so a read of this table hands
// nobody a working link. A claimed or revoked row is kept, so a replayed link
// is refused as spent rather than reading as one that never existed.
type Invitation struct {
	ID             uint   `gorm:"primaryKey;autoIncrement"`
	Hash           string `gorm:"size:64;not null;uniqueIndex"`
	OrganizationID uint   `gorm:"not null;index"`
	TeamID         uint   `gorm:"not null"`
	Role           string `gorm:"size:16;not null"`
	// Email is the verified address that may claim the invitation, trimmed and
	// lower case. It is empty for an any-email invitation.
	Email     string    `gorm:"size:320;not null;default:''"`
	AnyEmail  bool      `gorm:"not null;default:false"`
	InvitedBy uint      `gorm:"not null;default:0"`
	ExpiresAt time.Time `gorm:"not null"`
	CreatedAt time.Time `gorm:"not null"`
	ClaimedAt *time.Time
	ClaimedBy uint `gorm:"not null;default:0"`
	RevokedAt *time.Time
}

// TableName pins the table name.
func (Invitation) TableName() string { return "hub_invitations" }

// HubRepoClaim is the owner of one Git repository name. It is not scoped to a
// Team: two Teams must not both write to a repository, so the name is the key
// across every one of them.
type HubRepoClaim struct {
	Name      string    `gorm:"primaryKey;size:100"`
	Owner     string    `gorm:"size:400;not null"`
	CreatedAt time.Time `gorm:"not null"`
}

// TableName pins the table name.
func (HubRepoClaim) TableName() string { return "hub_repo_claims" }

// Models is everything AutoMigrate reconciles. A new model is added here and
// nowhere else.
func Models() []any {
	return []any{
		&SchemaState{},
		&HubEnvironment{},
		&HubToken{},
		&HubWork{},
		&HubSupportAccess{},
		&HubCloneKey{},
		&HubConnection{},
		&Session{},
		&PasswordInvite{},
		&SessionKey{},
		&HistoryRow{},
		&UsageSample{},
		&JoinPhase{},
		&HubChatConversation{},
		&HubChatMessage{},
		&Organization{},
		&Team{},
		&User{},
		&Membership{},
		&Invitation{},
		&HubRepoClaim{},
		&OAuthClient{},
		&OAuthRequest{},
		&OAuthCode{},
		&AgentSession{},
		&OAuthToken{},
		&ProvisioningRun{},
		&ProvisioningStep{},
	}
}

// OAuthClient is a client that registered itself with Dynamic Client
// Registration. A client that publishes a metadata document is never stored:
// its document is fetched and its snapshot rides with each request.
type OAuthClient struct {
	ID           string    `gorm:"primaryKey;size:64"`
	Name         string    `gorm:"size:64;not null"`
	RedirectURIs []string  `gorm:"type:jsonb;serializer:json;not null"`
	CreatedAt    time.Time `gorm:"not null;index"`
	// Granted is true once a session was made for the client. A registration
	// that never is dropped after a day.
	Granted bool `gorm:"not null;default:false"`
}

// TableName pins the table name.
func (OAuthClient) TableName() string { return "oauth_clients" }

// OAuthRequest is an authorization request that passed every check and waits
// for the person to consent. The row is found by the hash of a one time token
// that only the person's browser holds, and is bound to the User who signed
// in, so nobody else can approve it.
type OAuthRequest struct {
	Hash          string    `gorm:"primaryKey;size:64"`
	UserID        uint      `gorm:"not null"`
	ClientID      string    `gorm:"size:2048;not null"`
	ClientName    string    `gorm:"size:64;not null"`
	ClientHost    string    `gorm:"size:253;not null;default:''"`
	ClientKind    string    `gorm:"size:8;not null"`
	RedirectURI   string    `gorm:"size:2048;not null"`
	Scope         string    `gorm:"size:512;not null;default:''"`
	State         string    `gorm:"size:2048;not null;default:''"`
	CodeChallenge string    `gorm:"size:64;not null"`
	Resource      string    `gorm:"size:2048;not null"`
	CreatedAt     time.Time `gorm:"not null"`
	ExpiresAt     time.Time `gorm:"not null;index"`
}

// TableName pins the table name.
func (OAuthRequest) TableName() string { return "oauth_requests" }

// OAuthCode is an authorization code, keyed by its hash. It carries everything
// the session will be made from, so no session exists until the client
// exchanges the code. UsedAt and SessionID are how a second use is noticed and
// the session the first use bought is found and ended.
type OAuthCode struct {
	Hash           string    `gorm:"primaryKey;size:64"`
	UserID         uint      `gorm:"not null"`
	OrganizationID uint      `gorm:"not null"`
	TeamID         uint      `gorm:"not null"`
	Method         string    `gorm:"size:16;not null;default:''"`
	Access         string    `gorm:"size:16;not null"`
	Passwords      bool      `gorm:"not null;default:false"`
	SQLWrites      bool      `gorm:"not null;default:false"`
	ClientID       string    `gorm:"size:2048;not null"`
	ClientName     string    `gorm:"size:64;not null"`
	ClientHost     string    `gorm:"size:253;not null;default:''"`
	ClientKind     string    `gorm:"size:8;not null"`
	RedirectURI    string    `gorm:"size:2048;not null"`
	CodeChallenge  string    `gorm:"size:64;not null"`
	Resource       string    `gorm:"size:2048;not null"`
	CreatedAt      time.Time `gorm:"not null"`
	ExpiresAt      time.Time `gorm:"not null;index"`
	UsedAt         *time.Time
	SessionID      string `gorm:"size:32;not null;default:''"`
}

// TableName pins the table name.
func (OAuthCode) TableName() string { return "oauth_codes" }

// AgentSession is what an agent's tokens are backed by: who approved it, where
// it acts and what it may do. Revoking the session ends every token it holds
// at once. It lasts until ExpiresAt, and until it has gone unused for the idle
// window.
type AgentSession struct {
	ID             string `gorm:"primaryKey;size:32"`
	UserID         uint   `gorm:"not null;index"`
	OrganizationID uint   `gorm:"not null"`
	TeamID         uint   `gorm:"not null"`
	// Method is how the person signed in to approve it, which decides how the
	// activity log names them.
	Method     string    `gorm:"size:16;not null;default:''"`
	Access     string    `gorm:"size:16;not null"`
	Passwords  bool      `gorm:"not null;default:false"`
	SQLWrites  bool      `gorm:"not null;default:false"`
	ClientID   string    `gorm:"size:2048;not null"`
	ClientName string    `gorm:"size:64;not null"`
	ClientHost string    `gorm:"size:253;not null;default:''"`
	ClientKind string    `gorm:"size:8;not null"`
	Resource   string    `gorm:"size:2048;not null"`
	CreatedAt  time.Time `gorm:"not null"`
	LastUsedAt time.Time `gorm:"not null"`
	ExpiresAt  time.Time `gorm:"not null;index"`
}

// TableName pins the table name.
func (AgentSession) TableName() string { return "agent_sessions" }

// The two kinds of token a session holds.
const (
	TokenAccess  = "access"
	TokenRefresh = "refresh"
)

// OAuthToken is one issued token, keyed by the SHA-256 hash of its value. The
// value is returned once when it is minted and never stored. RotatedAt is set
// on a refresh token the moment it is exchanged, which is how presenting it
// again is told from presenting it the first time.
type OAuthToken struct {
	Hash      string    `gorm:"primaryKey;size:64"`
	SessionID string    `gorm:"size:32;not null;index"`
	Kind      string    `gorm:"size:8;not null"`
	ExpiresAt time.Time `gorm:"not null;index"`
	RotatedAt *time.Time
}

// TableName pins the table name.
func (OAuthToken) TableName() string { return "oauth_tokens" }
