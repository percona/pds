CREATE TABLE hub_chat_conversations (
    id character varying(32) NOT NULL,
    tenant_id character varying(128) NOT NULL,
    subject character varying(128) DEFAULT ''::character varying NOT NULL,
    title text NOT NULL,
    provider character varying(32) NOT NULL,
    model character varying(200) NOT NULL,
    database_id character varying(253) DEFAULT ''::character varying NOT NULL,
    request_key character varying(64) DEFAULT ''::character varying NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL
);
CREATE TABLE hub_chat_messages (
    id bigint NOT NULL,
    conversation_id character varying(32) NOT NULL,
    tenant_id character varying(128) NOT NULL,
    sequence bigint NOT NULL,
    role character varying(16) NOT NULL,
    content text NOT NULL,
    actions text NOT NULL,
    created_at timestamp with time zone NOT NULL
);
CREATE SEQUENCE hub_chat_messages_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE hub_chat_messages_id_seq OWNED BY hub_chat_messages.id;
CREATE TABLE hub_clone_keys (
    tenant_id character varying(128) NOT NULL,
    environment character varying(253) NOT NULL,
    sealed text NOT NULL,
    updated_at timestamp with time zone NOT NULL
);
CREATE TABLE hub_connections (
    tenant_id character varying(128) NOT NULL,
    name character varying(253) NOT NULL,
    field character varying(190) NOT NULL,
    sealed text NOT NULL,
    updated_at timestamp with time zone NOT NULL
);
CREATE TABLE hub_environments (
    tenant_id character varying(128) NOT NULL,
    name character varying(253) NOT NULL,
    document jsonb NOT NULL,
    updated_at timestamp with time zone NOT NULL
);
CREATE TABLE hub_history (
    id bigint NOT NULL,
    tenant_id character varying(128) NOT NULL,
    database_id character varying(253) NOT NULL,
    environment character varying(253) DEFAULT ''::character varying NOT NULL,
    kind character varying(32) NOT NULL,
    at timestamp with time zone NOT NULL,
    actor character varying(128),
    client character varying(32),
    request_id character varying(64),
    action character varying(64),
    target character varying(253),
    outcome character varying(32),
    detail character varying(500),
    error character varying(500),
    resource_kind character varying(32),
    resource_name character varying(253),
    from_phase character varying(32),
    to_phase character varying(32),
    message character varying(500),
    object_id character varying(253),
    storage character varying(253),
    size_bytes bigint,
    started_at timestamp with time zone,
    finished_at timestamp with time zone,
    duration_ms bigint,
    reason character varying(128),
    event_type character varying(32),
    count integer,
    legacy_id character varying(64)
);
CREATE SEQUENCE hub_history_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE hub_history_id_seq OWNED BY hub_history.id;
CREATE TABLE hub_imports (
    tenant_id character varying(128) NOT NULL,
    source character varying(64) NOT NULL,
    imported_at timestamp with time zone NOT NULL
);
CREATE TABLE hub_join_phases (
    id bigint NOT NULL,
    tenant_id character varying(128) NOT NULL,
    environment character varying(253) NOT NULL,
    run_id character varying(64) NOT NULL,
    phase character varying(128) NOT NULL,
    outcome character varying(32) NOT NULL,
    started_at timestamp with time zone,
    finished_at timestamp with time zone,
    duration_ms bigint,
    output character varying(4000)
);
CREATE SEQUENCE hub_join_phases_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE hub_join_phases_id_seq OWNED BY hub_join_phases.id;
CREATE TABLE hub_schema_state (
    id bigint NOT NULL,
    revision bigint NOT NULL,
    applied_at timestamp with time zone NOT NULL
);
CREATE SEQUENCE hub_schema_state_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE hub_schema_state_id_seq OWNED BY hub_schema_state.id;
CREATE TABLE hub_session_keys (
    id bigint NOT NULL,
    key bytea NOT NULL,
    created_at timestamp with time zone NOT NULL
);
CREATE SEQUENCE hub_session_keys_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE hub_session_keys_id_seq OWNED BY hub_session_keys.id;
CREATE TABLE hub_tenant_credentials (
    tenant_id character varying(128) NOT NULL,
    password_hash character varying(120) NOT NULL,
    failures bigint NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL
);
CREATE TABLE hub_tenant_invites (
    hash character varying(64) NOT NULL,
    tenant_id character varying(128) NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    claimed_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL
);
CREATE TABLE hub_tenant_sessions (
    hash character varying(64) NOT NULL,
    tenant_id character varying(128) NOT NULL,
    created_at timestamp with time zone NOT NULL,
    last_seen_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    client character varying(32) DEFAULT ''::character varying NOT NULL,
    client_version character varying(32) DEFAULT ''::character varying NOT NULL
);
CREATE TABLE hub_tokens (
    tenant_id character varying(128) NOT NULL,
    hash character varying(64) NOT NULL,
    document jsonb NOT NULL,
    updated_at timestamp with time zone NOT NULL
);
CREATE TABLE hub_usage_samples (
    id bigint NOT NULL,
    tenant_id character varying(128) NOT NULL,
    database_id character varying(253) NOT NULL,
    environment character varying(253) DEFAULT ''::character varying NOT NULL,
    at timestamp with time zone NOT NULL,
    cpu_avg numeric,
    cpu_max numeric,
    memory_avg numeric,
    memory_max numeric,
    storage_bytes bigint,
    metrics_reason character varying(300),
    monitored boolean DEFAULT false NOT NULL,
    monitor_cpu numeric,
    monitor_memory numeric,
    monitor_connections numeric,
    monitor_replication_lag numeric,
    monitor_disk numeric,
    monitor_pending character varying(64) DEFAULT ''::character varying NOT NULL
);
CREATE SEQUENCE hub_usage_samples_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE hub_usage_samples_id_seq OWNED BY hub_usage_samples.id;
CREATE TABLE hub_work (
    tenant_id character varying(128) NOT NULL,
    environment character varying(253) NOT NULL,
    document jsonb NOT NULL,
    updated_at timestamp with time zone NOT NULL
);
ALTER TABLE ONLY hub_chat_messages ALTER COLUMN id SET DEFAULT nextval('hub_chat_messages_id_seq'::regclass);
ALTER TABLE ONLY hub_history ALTER COLUMN id SET DEFAULT nextval('hub_history_id_seq'::regclass);
ALTER TABLE ONLY hub_join_phases ALTER COLUMN id SET DEFAULT nextval('hub_join_phases_id_seq'::regclass);
ALTER TABLE ONLY hub_schema_state ALTER COLUMN id SET DEFAULT nextval('hub_schema_state_id_seq'::regclass);
ALTER TABLE ONLY hub_session_keys ALTER COLUMN id SET DEFAULT nextval('hub_session_keys_id_seq'::regclass);
ALTER TABLE ONLY hub_usage_samples ALTER COLUMN id SET DEFAULT nextval('hub_usage_samples_id_seq'::regclass);
ALTER TABLE ONLY hub_chat_conversations
    ADD CONSTRAINT hub_chat_conversations_pkey PRIMARY KEY (id);
ALTER TABLE ONLY hub_chat_messages
    ADD CONSTRAINT hub_chat_messages_pkey PRIMARY KEY (id);
ALTER TABLE ONLY hub_clone_keys
    ADD CONSTRAINT hub_clone_keys_pkey PRIMARY KEY (tenant_id, environment);
ALTER TABLE ONLY hub_connections
    ADD CONSTRAINT hub_connections_pkey PRIMARY KEY (tenant_id, name, field);
ALTER TABLE ONLY hub_environments
    ADD CONSTRAINT hub_environments_pkey PRIMARY KEY (tenant_id, name);
ALTER TABLE ONLY hub_history
    ADD CONSTRAINT hub_history_pkey PRIMARY KEY (id);
ALTER TABLE ONLY hub_imports
    ADD CONSTRAINT hub_imports_pkey PRIMARY KEY (tenant_id, source);
ALTER TABLE ONLY hub_join_phases
    ADD CONSTRAINT hub_join_phases_pkey PRIMARY KEY (id);
ALTER TABLE ONLY hub_schema_state
    ADD CONSTRAINT hub_schema_state_pkey PRIMARY KEY (id);
ALTER TABLE ONLY hub_session_keys
    ADD CONSTRAINT hub_session_keys_pkey PRIMARY KEY (id);
ALTER TABLE ONLY hub_tenant_credentials
    ADD CONSTRAINT hub_tenant_credentials_pkey PRIMARY KEY (tenant_id);
ALTER TABLE ONLY hub_tenant_invites
    ADD CONSTRAINT hub_tenant_invites_pkey PRIMARY KEY (hash);
ALTER TABLE ONLY hub_tenant_sessions
    ADD CONSTRAINT hub_tenant_sessions_pkey PRIMARY KEY (hash);
ALTER TABLE ONLY hub_tokens
    ADD CONSTRAINT hub_tokens_pkey PRIMARY KEY (tenant_id, hash);
ALTER TABLE ONLY hub_usage_samples
    ADD CONSTRAINT hub_usage_samples_pkey PRIMARY KEY (id);
ALTER TABLE ONLY hub_work
    ADD CONSTRAINT hub_work_pkey PRIMARY KEY (tenant_id, environment);
CREATE INDEX idx_chat_conversations_scope ON hub_chat_conversations USING btree (tenant_id, updated_at);
CREATE INDEX idx_history_scope ON hub_history USING btree (tenant_id, database_id, kind);
CREATE INDEX idx_hub_chat_messages_tenant_id ON hub_chat_messages USING btree (tenant_id);
CREATE INDEX idx_hub_history_at ON hub_history USING btree (at);
CREATE INDEX idx_hub_tenant_invites_tenant_id ON hub_tenant_invites USING btree (tenant_id);
CREATE INDEX idx_hub_tenant_sessions_expires_at ON hub_tenant_sessions USING btree (expires_at);
CREATE INDEX idx_hub_tenant_sessions_tenant_id ON hub_tenant_sessions USING btree (tenant_id);
CREATE INDEX idx_hub_usage_samples_at ON hub_usage_samples USING btree (at);
CREATE INDEX idx_join_scope ON hub_join_phases USING btree (tenant_id, environment, run_id);
CREATE INDEX idx_usage_scope ON hub_usage_samples USING btree (tenant_id, database_id);
CREATE UNIQUE INDEX uidx_chat_conversations_request ON hub_chat_conversations USING btree (tenant_id, request_key) WHERE ((request_key)::text <> ''::text);
CREATE UNIQUE INDEX uidx_chat_messages_sequence ON hub_chat_messages USING btree (conversation_id, sequence);
CREATE UNIQUE INDEX uidx_history_legacy ON hub_history USING btree (tenant_id, legacy_id) WHERE ((legacy_id)::text <> ''::text);
CREATE UNIQUE INDEX uidx_history_object ON hub_history USING btree (tenant_id, database_id, environment, kind, object_id) WHERE ((object_id)::text <> ''::text);
CREATE UNIQUE INDEX uidx_join_phase ON hub_join_phases USING btree (tenant_id, environment, run_id, phase);
