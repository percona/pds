{{/*
Chart name, overridable.
*/}}
{{- define "pds.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified resource name prefix.
*/}}
{{- define "pds.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart label value.
*/}}
{{- define "pds.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Labels shared by every object.
*/}}
{{- define "pds.labels" -}}
helm.sh/chart: {{ include "pds.chart" . }}
app.kubernetes.io/name: {{ include "pds.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: pds
{{- with .Values.commonLabels }}
{{- toYaml . | nindent 0 }}
{{- end }}
{{- end }}

{{/*
Selector labels for one component. Call with (dict "root" . "component" "adapter").
*/}}
{{- define "pds.selectorLabels" -}}
app.kubernetes.io/name: {{ include "pds.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/*
Component name, for example pds-adapter.
*/}}
{{- define "pds.componentName" -}}
{{- printf "%s-%s" (include "pds.fullname" .root) .component | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "pds.adapter.fullname" -}}
{{- include "pds.componentName" (dict "root" . "component" "adapter") }}
{{- end }}

{{- define "pds.ui.fullname" -}}
{{- include "pds.componentName" (dict "root" . "component" "ui") }}
{{- end }}

{{/*
ServiceAccount name used by both components.
*/}}
{{- define "pds.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "pds.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding the ServiceNow credentials. Reading it is always
optional: with no Secret the support endpoints answer 501.
*/}}
{{- define "pds.servicenow.secretName" -}}
{{- if .Values.servicenow.existingSecret }}
{{- .Values.servicenow.existingSecret }}
{{- else if .Values.servicenow.secretName }}
{{- .Values.servicenow.secretName }}
{{- else }}
{{- printf "%s-servicenow" (include "pds.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Name of the Secret the adapter writes a pasted Anthropic key to when it runs
without a database. Nothing in the chart creates it: only a key an
Organization pastes is used.
*/}}
{{- define "pds.assistant.secretName" -}}
{{- printf "%s-assistant" (include "pds.fullname" .) }}
{{- end }}

{{/*
Name of the Secret the adapter writes a pasted Cursor key to when it runs
without a database.
*/}}
{{- define "pds.cursor.secretName" -}}
{{- printf "%s-cursor" (include "pds.fullname" .) }}
{{- end }}

{{/*
Name of the Secret holding the Everest API token. Reading it is always
optional: with no Secret the adapter sends no Authorization header, which is
what an unauthenticated development Everest expects.
*/}}
{{- define "pds.everest.secretName" -}}
{{- if .Values.everest.existingSecret }}
{{- .Values.everest.existingSecret }}
{{- else if .Values.everest.secretName }}
{{- .Values.everest.secretName }}
{{- else }}
{{- printf "%s-everest" (include "pds.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding the Everest admin account. Everest writes it on
first install and keeps it, and the adapter reads it to log in again when the
token it is holding expires.
*/}}
{{- define "pds.everest.accountsSecretName" -}}
{{- default "everest-accounts" .Values.everest.accountsSecret }}
{{- end }}

{{/*
Name of the Secret holding the hub agent token. A chart-managed Secret is
created from adapter.hub.agentToken when no existingSecret is set.
*/}}
{{- define "pds.hub.agentTokenSecretName" -}}
{{- if .Values.adapter.hub.agentTokenExistingSecret }}
{{- .Values.adapter.hub.agentTokenExistingSecret }}
{{- else }}
{{- printf "%s-hub-agent-token" (include "pds.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding the hub operator token. A chart-managed Secret is
created from adapter.hub.operatorToken when no existingSecret is set.
*/}}
{{- define "pds.hub.operatorTokenSecretName" -}}
{{- if .Values.adapter.hub.operatorTokenExistingSecret }}
{{- .Values.adapter.hub.operatorTokenExistingSecret }}
{{- else }}
{{- printf "%s-hub-operator-token" (include "pds.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding the PMM service account token. Reading it is always
optional: with no Secret the metrics endpoints report pending.
*/}}
{{- define "pds.pmm.secretName" -}}
{{- if .Values.pmm.existingSecret }}
{{- .Values.pmm.existingSecret }}
{{- else if .Values.pmm.secretName }}
{{- .Values.pmm.secretName }}
{{- else }}
{{- printf "%s-pmm" (include "pds.fullname" .) }}
{{- end }}
{{- end }}

{{/*
Image reference. Call with (dict "image" .Values.adapter.image "root" .).
*/}}
{{- define "pds.image" -}}
{{- $tag := .image.tag | default .root.Chart.AppVersion }}
{{- printf "%s:%s" .image.repository $tag }}
{{- end }}

{{/*
Adapter base URL used by the UI proxy and by the test hooks.
*/}}
{{- define "pds.adapter.url" -}}
{{- printf "http://%s:%v" (include "pds.adapter.fullname" .) .Values.adapter.service.port }}
{{- end }}

{{- define "pds.ui.url" -}}
{{- printf "http://%s:%v" (include "pds.ui.fullname" .) .Values.ui.service.port }}
{{- end }}

{{/*
Everest managed namespace. Database Instances live here.
*/}}
{{- define "pds.dbNamespace" -}}
{{- if .Values.adapter.everestNamespace -}}
{{- .Values.adapter.everestNamespace -}}
{{- else if .Values.stack.enabled -}}
{{- .Values.stack.dbNamespace -}}
{{- else -}}
{{- .Release.Namespace -}}
{{- end -}}
{{- end }}

{{/*
Namespace helm notes report PMM in. Empty follows the release or database namespace.
*/}}
{{- define "pds.pmmNamespace" -}}
{{- if .Values.adapter.pmmNamespace -}}
{{- .Values.adapter.pmmNamespace -}}
{{- else if .Values.stack.enabled -}}
{{- .Release.Namespace -}}
{{- else -}}
{{- include "pds.dbNamespace" . -}}
{{- end -}}
{{- end }}

{{/*
BackupStorage name. S3 when stack.s3 is on, otherwise the stack MinIO store.
*/}}
{{- define "pds.backupStorage" -}}
{{- if .Values.adapter.everestBackupStorage -}}
{{- .Values.adapter.everestBackupStorage -}}
{{- else if and .Values.stack.enabled .Values.stack.s3.enabled -}}
{{- .Values.stack.s3.storageName -}}
{{- else if and .Values.stack.enabled .Values.stack.minio.enabled -}}
{{- .Values.stack.minio.storageName -}}
{{- end -}}
{{- end }}

{{/*
Whether this release installs PMM. stack.pmm.enabled is left unset by default,
so PMM follows stack.enabled: the subchart's condition falls through to
stack.enabled when the first path has no value, and this helper reads the same
way. Setting it false keeps PMM out of a stack install.
*/}}
{{- define "pds.stackPMM" -}}
{{- $pmm := .Values.stack.pmm.enabled -}}
{{- if and .Values.stack.enabled (or (kindIs "invalid" $pmm) $pmm) -}}
true
{{- end -}}
{{- end }}

{{/*
Whether something other than this release installs the PMM server. The
subchart is left out but the rest of the PMM wiring stays, so the token Job
has to run without helm waiting on it: the server only appears once Flux
reconciles, and that happens after helm returns.
*/}}
{{- define "pds.pmm.external" -}}
{{- $sub := .Values.stack.pmm.subchart -}}
{{- if and (include "pds.stackPMM" .) (kindIs "bool" $sub) (not $sub) -}}
true
{{- end -}}
{{- end }}

{{/*
URL pmm-client sidecars use to reach PMM from database pods.
*/}}
{{- define "pds.pmmClientURL" -}}
{{- if .Values.adapter.pmmClientURL -}}
{{- .Values.adapter.pmmClientURL -}}
{{- else if include "pds.stackPMM" . -}}
{{- printf "https://%s.%s.svc.cluster.local" (.Values.platform.pmm.service | default "monitoring-service") .Release.Namespace -}}
{{- end -}}
{{- end }}

{{/*
MonitoringConfig name new instances scrape through.
*/}}
{{- define "pds.monitoringConfig" -}}
{{- if .Values.adapter.everestMonitoringConfig -}}
{{- .Values.adapter.everestMonitoringConfig -}}
{{- else if include "pds.stackPMM" . -}}
pmm
{{- else if and .Values.stack.enabled (include "pds.pmmClientURL" .) .Values.pmm.existingSecret -}}
pmm
{{- end -}}
{{- end }}

{{/*
Whether the monitoring hook mints the PMM token itself. It does for the PMM
this chart installs, unless a token was handed over in pmm.existingSecret:
that Secret belongs to whoever supplied it.
*/}}
{{- define "pds.pmm.mintToken" -}}
{{- if and (include "pds.stackPMM" .) (not .Values.pmm.existingSecret) -}}
true
{{- end -}}
{{- end }}

{{/*
Name of the Secret that holds the PMM admin password.
*/}}
{{- define "pds.pmm.adminSecretName" -}}
{{- (index .Values "pmm-server" "secret" "name") | default "pmm-secret" -}}
{{- end }}

{{- define "pds.activity.configMapName" -}}
{{- printf "%s-activity" (include "pds.fullname" .) }}
{{- end }}

{{- define "pds.hub.joinSecretName" -}}
{{- with .Values.adapter.hub.joinSecret }}{{ . }}{{ else }}{{ printf "%s-join-tokens" (include "pds.fullname" .) }}{{ end }}
{{- end }}

{{- define "pds.hub.gitClaimName" -}}
{{- with .Values.adapter.hub.persistence.existingClaim }}{{ . }}{{ else }}{{ printf "%s-hub-git" (include "pds.fullname" .) }}{{ end }}
{{- end }}

{{/*
True when gitDir is on a PVC or an extra volume that is actually mounted at
gitDir. A nonempty extraVolumes list is not enough: that can be an unrelated
scratch volume while gitDir still sits on emptyDir.
*/}}
{{- define "pds.hub.gitDirHasStorage" -}}
{{- if or .Values.adapter.hub.persistence.enabled .Values.adapter.hub.persistence.existingClaim -}}
true
{{- else -}}
{{- $gitDir := .Values.adapter.hub.gitDir -}}
{{- $ok := false -}}
{{- range .Values.adapter.extraVolumeMounts -}}
{{- if eq .mountPath $gitDir -}}
{{- $volName := .name -}}
{{- range $.Values.adapter.extraVolumes -}}
{{- if eq .name $volName -}}
{{- $ok = true -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if $ok }}true{{ end -}}
{{- end -}}
{{- end }}

{{- define "pds.hub.inventoryConfigMapName" -}}
{{- with .Values.adapter.hub.inventoryConfigMap }}{{ . }}{{ else }}{{ printf "%s-hub-inventory" (include "pds.fullname" .) }}{{ end }}
{{- end }}

{{- define "pds.hub.workConfigMapName" -}}
{{- with .Values.adapter.hub.workConfigMap }}{{ . }}{{ else }}{{ printf "%s-hub-work" (include "pds.fullname" .) }}{{ end }}
{{- end }}

{{/*
A connected environment was once called a cluster, and a release that is
already installed carries adapter.hub.clusterName and adapter.hub.agentClusters
in its values. Both keys still work for one release and the notes name them.
The environment keys win when both are set.
*/}}
{{- define "pds.hub.environmentName" -}}
{{- .Values.adapter.hub.environmentName | default .Values.adapter.hub.clusterName -}}
{{- end }}

{{- define "pds.hub.agentEnvironments" -}}
{{- join "," (.Values.adapter.hub.agentEnvironments | default .Values.adapter.hub.agentClusters) -}}
{{- end }}

{{/*
One line per deprecated value this release sets. Empty when it sets none.
*/}}
{{- define "pds.deprecations" -}}
{{- $lines := list -}}
{{- if .Values.adapter.hub.clusterName -}}
{{- $lines = append $lines "adapter.hub.clusterName is deprecated: use adapter.hub.environmentName." -}}
{{- end -}}
{{- if .Values.adapter.hub.agentClusters -}}
{{- $lines = append $lines "adapter.hub.agentClusters is deprecated: use adapter.hub.agentEnvironments." -}}
{{- end -}}
{{- join "\n" $lines -}}
{{- end }}

{{/*
Shared adapter container env: Everest, PMM, and connection Secret names.
*/}}
{{/*
Name of the PerconaPGCluster the adapter keeps its own state in.
*/}}
{{/*
Name of the PerconaPGCluster the adapter keeps its own state in.

Bounded well below the 63 character object limit, because the operator derives
its own names from this one: the user Secret, the primary Service, the instance
pods and the pgBackRest repo host all append to it, and the longest of those
adds around twenty characters. The fullname is already truncated to 63, so
appending to it unbounded produced a 65 character cluster on a long release
even before the operator got to it.
*/}}
{{- define "pds.hub.databaseName" -}}
{{- $name := .Values.adapter.database.inCluster.name -}}
{{- if $name -}}
{{- if gt (len $name) 40 -}}
{{- fail "adapter.database.inCluster.name has to be 40 characters or fewer. The operator appends to it for the user Secret, the Services and the instance pods, and those names have a 63 character limit." -}}
{{- end -}}
{{- $name -}}
{{- else -}}
{{- printf "%s-hub-db" (include "pds.fullname" .) | trunc 40 | trimSuffix "-" -}}
{{- end -}}
{{- end }}

{{/*
Name shared by the hook ServiceAccount, RBAC and Job that apply the cluster.
The fullname is already truncated to 63, so appending to it can overflow the
limit and make the API server refuse every hook object.
*/}}
{{- define "pds.hub.databaseHookName" -}}
{{- printf "%s-hub-database" (include "pds.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
Secret the key that seals stored credentials is read from. A Secret you supply
wins over the generated one, which is how a key held in an external manager is
brought in.

Losing this Secret makes every stored credential unreadable. The adapter does
not fall back to the Secrets the credentials came from, so they have to be
pasted again in the connections panel.
*/}}
{{- define "pds.state.encryptionSecretName" -}}
{{- if .Values.adapter.state.existingSecret -}}
{{- .Values.adapter.state.existingSecret -}}
{{- else -}}
{{- printf "%s-state-key" (include "pds.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end }}

{{/*
Key inside the Secret above.
*/}}
{{- define "pds.state.encryptionSecretKey" -}}
{{- .Values.adapter.state.keyField | default "STATE_ENCRYPTION_KEY" -}}
{{- end }}

{{/*
Field in that same Secret holding the keys accepted for reading only, comma
separated. It lives beside the primary key rather than in values, because a
retired key still opens rows.
*/}}
{{- define "pds.state.retiredKeysSecretKey" -}}
{{- .Values.adapter.state.retiredKeysField | default "STATE_ENCRYPTION_KEYS_RETIRED" -}}
{{- end }}

{{/*
Name shared by the hook ServiceAccount, RBAC and Job that mint the key.
*/}}
{{- define "pds.state.keyHookName" -}}
{{- printf "%s-state-key" (include "pds.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
The Organization this release serves: adapter.state.organization, or a star for
every one.
*/}}
{{- define "pds.hub.organization" -}}
{{- .Values.adapter.state.organization | default "default" | trim -}}
{{- end }}

{{/*
Secret the connection URL is read from, and the key inside it. An external
database uses the Secret the operator of that database was given; the rendered
one uses the Secret the PostgreSQL operator mints for its user, where the URL
is under `uri`. Neither is written by this chart.
*/}}
{{- define "pds.hub.databaseSecretName" -}}
{{- if .Values.adapter.database.existingSecret -}}
{{- .Values.adapter.database.existingSecret -}}
{{- else -}}
{{- printf "%s-pguser-%s" (include "pds.hub.databaseName" .) .Values.adapter.database.inCluster.user -}}
{{- end -}}
{{- end }}

{{/*
urlKey names the key in a Secret you supply. The rendered database uses the
Secret the operator mints, where the URL is always under uri, so urlKey does
not apply to it.
*/}}
{{- define "pds.hub.databaseSecretKey" -}}
{{- if .Values.adapter.database.existingSecret -}}
{{- .Values.adapter.database.urlKey | default "DATABASE_URL" -}}
{{- else -}}
uri
{{- end -}}
{{- end }}

{{- define "pds.adapter.env" -}}
- name: ADAPTER_ADDR
  value: ":{{ .Values.adapter.service.port }}"
- name: ADAPTER_ENV
  value: {{ .Values.adapter.environment | quote }}
- name: ADAPTER_LOG_LEVEL
  value: {{ .Values.adapter.logLevel | quote }}
- name: ADAPTER_SHUTDOWN_TIMEOUT
  value: {{ .Values.adapter.shutdownTimeout | quote }}
- name: EVEREST_URL
  value: {{ .Values.adapter.everestURL | quote }}
- name: EVEREST_NAMESPACE
  value: {{ include "pds.dbNamespace" . | quote }}
- name: EVEREST_BACKUP_STORAGE
  value: {{ include "pds.backupStorage" . | quote }}
- name: EVEREST_MONITORING_CONFIG
  value: {{ include "pds.monitoringConfig" . | quote }}
- name: EVEREST_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ include "pds.everest.secretName" . }}
      key: EVEREST_TOKEN
      optional: true
- name: EVEREST_ACCOUNTS_SECRET
  value: {{ include "pds.everest.accountsSecretName" . | quote }}
{{- $pmmURL := .Values.adapter.pmmURL }}
{{- if and .Values.stack.enabled (not (include "pds.stackPMM" .)) }}
{{- if or (eq $pmmURL "") (eq $pmmURL "http://monitoring-service") }}
{{- $pmmURL = "" }}
{{- end }}
{{- else if and (include "pds.stackPMM" .) (eq $pmmURL "") }}
{{- $pmmURL = printf "http://%s" (.Values.platform.pmm.service | default "monitoring-service") }}
{{- end }}
- name: PMM_URL
  value: {{ $pmmURL | quote }}
- name: PMM_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ include "pds.pmm.secretName" . }}
      key: PMM_TOKEN
      optional: true
- name: OIDC_ISSUER
  value: {{ .Values.adapter.oidc.issuer | quote }}
{{- if .Values.adapter.oidc.issuer }}
- name: OIDC_CLIENT_ID
  value: {{ .Values.adapter.oidc.clientId | quote }}
- name: OIDC_SCOPES
  value: {{ .Values.adapter.oidc.scopes | quote }}
- name: OIDC_CLIENT_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ required "adapter.oidc.existingSecret is required when adapter.oidc.issuer is set" .Values.adapter.oidc.existingSecret }}
      key: {{ .Values.adapter.oidc.secretKey }}
{{- if .Values.adapter.oidc.signup.enabled }}
- name: SIGNUP_ENABLED
  value: "true"
- name: SIGNUP_ORGANIZATION_CEILING
  value: {{ .Values.adapter.oidc.signup.organizationCeiling | quote }}
{{- end }}
{{- end }}
- name: SN_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "pds.servicenow.secretName" . }}
      key: SN_API_KEY
      optional: true
- name: SN_CLIENT_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ include "pds.servicenow.secretName" . }}
      key: SN_CLIENT_TOKEN
      optional: true
- name: SN_SECRET_NAME
  value: {{ include "pds.servicenow.secretName" . | quote }}
- name: ASSISTANT_SECRET_NAME
  value: {{ include "pds.assistant.secretName" . | quote }}
- name: POD_NAMESPACE
  valueFrom:
    fieldRef:
      fieldPath: metadata.namespace
- name: CURSOR_SECRET_NAME
  value: {{ include "pds.cursor.secretName" . | quote }}
{{- with .Values.servicenow.url }}
{{- if not (hasPrefix "https://" .) }}
{{- fail "servicenow.url must be an https URL: both ServiceNow tokens travel on every call" }}
{{- end }}
- name: SN_URL
  value: {{ . | quote }}
{{- end }}
{{- if .Values.adapter.database.enabled }}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ include "pds.hub.databaseSecretName" . }}
      key: {{ include "pds.hub.databaseSecretKey" . }}
- name: HUB_ORGANIZATION
  value: {{ include "pds.hub.organization" . | quote }}
{{- /*
Two bounds on a session, because they answer different questions. The lifetime
caps a cookie that keeps being used; the idle window catches one that stopped
being used at all. The adapter refuses to start if the idle window is the
longer of the two, because it would then never apply.
*/}}
- name: SESSION_LIFETIME
  value: {{ .Values.adapter.auth.sessionLifetime | quote }}
- name: SESSION_IDLE_TIMEOUT
  value: {{ .Values.adapter.auth.sessionIdleTimeout | quote }}
- name: INVITE_TTL
  value: {{ .Values.adapter.auth.inviteTTL | quote }}
- name: STATE_ENCRYPTION_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "pds.state.encryptionSecretName" . }}
      key: {{ include "pds.state.encryptionSecretKey" . }}
{{- /*
Retired keys come from the same Secret, never from values. A retired key is a
live key: it opens every row that still names it. Rendering one as a value
would put it in the Deployment spec, in the release Secret and in the output of
helm get values, beside the ciphertext it opens.

The reference is optional, so a deployment that has never rotated needs nothing
in the Secret.
*/}}
- name: STATE_ENCRYPTION_KEYS_RETIRED
  valueFrom:
    secretKeyRef:
      name: {{ include "pds.state.encryptionSecretName" . }}
      key: {{ include "pds.state.retiredKeysSecretKey" . }}
      optional: true
{{- end }}
- name: ACTIVITY_CONFIGMAP
  value: {{ include "pds.activity.configMapName" . | quote }}
- name: HUB_JOIN_SECRET
  value: {{ include "pds.hub.joinSecretName" . | quote }}
- name: HUB_INVENTORY_CONFIGMAP
  value: {{ include "pds.hub.inventoryConfigMapName" . | quote }}
- name: HUB_WORK_CONFIGMAP
  value: {{ include "pds.hub.workConfigMapName" . | quote }}
- name: HUB_JOIN_TOKEN_TTL
  value: {{ .Values.adapter.hub.joinTokenTTL | default "24h" | quote }}
- name: HUB_HEARTBEAT_STALE
  value: {{ .Values.adapter.hub.heartbeatStale | default "90s" | quote }}
- name: HUB_CLUSTER_REGION
  value: {{ .Values.adapter.hub.clusterRegion | default "us-west-2" | quote }}
{{- with .Values.adapter.hub.gitDir }}
- name: HUB_GIT_DIR
  value: {{ . | quote }}
{{- end }}
{{- with .Values.adapter.hub.publicURL }}
- name: HUB_PUBLIC_URL
  value: {{ . | quote }}
{{- end }}
{{- with .Values.adapter.hub.invitationFrom }}
- name: INVITATION_FROM
  value: {{ . | quote }}
{{- end }}
{{- with .Values.adapter.hub.sesRegion }}
- name: SES_REGION
  value: {{ . | quote }}
{{- end }}
{{- with .Values.adapter.hub.oauthIssuer }}
- name: HUB_OAUTH_ISSUER
  value: {{ . | quote }}
{{- end }}
{{- with .Values.adapter.hub.gitURL }}
- name: HUB_GIT_URL
  value: {{ . | quote }}
{{- end }}
{{- with .Values.adapter.hub.github.org }}
- name: HUB_GITHUB_ORG
  value: {{ . | quote }}
{{- end }}
{{- with .Values.adapter.hub.github.existingSecret }}
{{- if $.Values.adapter.hub.github.appPrivateKeyKey }}
- name: HUB_GITHUB_APP_ID
  valueFrom:
    secretKeyRef:
      name: {{ . | quote }}
      key: {{ $.Values.adapter.hub.github.appIdKey | quote }}
- name: HUB_GITHUB_APP_INSTALLATION_ID
  valueFrom:
    secretKeyRef:
      name: {{ . | quote }}
      key: {{ $.Values.adapter.hub.github.appInstallationIdKey | quote }}
- name: HUB_GITHUB_APP_PRIVATE_KEY
  valueFrom:
    secretKeyRef:
      name: {{ . | quote }}
      key: {{ $.Values.adapter.hub.github.appPrivateKeyKey | quote }}
{{- else }}
- name: HUB_GITHUB_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ . | quote }}
      key: {{ $.Values.adapter.hub.github.tokenKey | default "HUB_GITHUB_TOKEN" | quote }}
{{- end }}
{{- with $.Values.adapter.hub.github.registryTokenKey }}
- name: HUB_REGISTRY_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ $.Values.adapter.hub.github.existingSecret | quote }}
      key: {{ . | quote }}
{{- end }}
{{- end }}
{{- $agentImage := .Values.adapter.hub.agentImage }}
{{- if not $agentImage }}
{{- $agentImage = include "pds.image" (dict "image" .Values.adapter.image "root" .) }}
{{- end }}
- name: HUB_AGENT_IMAGE
  value: {{ $agentImage | quote }}
{{- with .Values.adapter.backupKey.user }}
{{- if not (and $.Values.stack.s3.enabled $.Values.stack.s3.bucket $.Values.stack.s3.region $.Values.stack.s3.existingSecret) }}
{{- fail "adapter.backupKey.user needs stack.s3.enabled with a bucket, a region and an existingSecret for the key to go into." }}
{{- end }}
- name: AGENT_BACKUP_USER
  value: {{ . | quote }}
- name: AGENT_BACKUP_USER_PATH
  value: {{ $.Values.adapter.backupKey.path | quote }}
- name: AGENT_BACKUP_BOUNDARY_ARN
  value: {{ $.Values.adapter.backupKey.boundaryArn | quote }}
- name: AGENT_BACKUP_BUCKET
  value: {{ $.Values.stack.s3.bucket | quote }}
- name: AGENT_BACKUP_SECRET
  value: {{ $.Values.stack.s3.existingSecret | quote }}
- name: AGENT_BACKUP_TAG
  value: {{ $.Values.adapter.backupKey.tag | quote }}
- name: AWS_REGION
  value: {{ $.Values.stack.s3.region | quote }}
{{- end }}
{{- with .Values.adapter.hub.releaseRef }}
- name: AGENT_RELEASE_REF
  value: {{ . | quote }}
{{- end }}
{{- with .Values.adapter.hub.managedChartVersion }}
- name: HUB_MANAGED_CHART_VERSION
  value: {{ . | quote }}
{{- end }}
{{- with .Values.adapter.hub.managedOutgoingCIDRs }}
- name: HUB_MANAGED_OUTGOING_CIDRS
  value: {{ join "," . | quote }}
{{- end }}
{{- with .Values.adapter.hub.url }}
- name: HUB_URL
  value: {{ . | quote }}
{{- end }}
{{- with include "pds.hub.environmentName" . }}
- name: ENVIRONMENT_NAME
  value: {{ . | quote }}
{{- end }}
{{- if or .Values.adapter.hub.agentToken .Values.adapter.hub.agentTokenExistingSecret }}
- name: HUB_AGENT_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ include "pds.hub.agentTokenSecretName" . }}
      key: {{ .Values.adapter.hub.agentTokenKey | default "HUB_AGENT_TOKEN" }}
{{- end }}
{{- if or .Values.adapter.hub.operatorToken .Values.adapter.hub.operatorTokenExistingSecret }}
- name: HUB_OPERATOR_TOKEN
  valueFrom:
    secretKeyRef:
      name: {{ include "pds.hub.operatorTokenSecretName" . }}
      key: {{ .Values.adapter.hub.operatorTokenKey | default "HUB_OPERATOR_TOKEN" }}
{{- end }}
{{- with include "pds.hub.agentEnvironments" . }}
- name: HUB_AGENT_ENVIRONMENTS
  value: {{ . | quote }}
{{- end }}
{{- range $surface, $on := .Values.adapter.features }}
- name: ADAPTER_FEATURE_{{ $surface | upper | replace "-" "_" }}
  value: {{ $on | quote }}
{{- end }}
{{- if .Values.adapter.database.enabled }}
{{- range .Values.adapter.extraEnv }}
{{- if eq .name "DATABASE_URL" }}
{{- fail "adapter.extraEnv sets DATABASE_URL, which would override the Secret reference and put the connection URL in the rendered release. Point adapter.database.existingSecret at a Secret instead." }}
{{- end }}
{{- if eq .name "STATE_ENCRYPTION_KEY" }}
{{- fail "adapter.extraEnv sets STATE_ENCRYPTION_KEY, which would override the Secret reference and put the key that opens every stored credential in the rendered release. Point adapter.state.existingSecret at a Secret instead." }}
{{- end }}
{{- if eq .name "STATE_ENCRYPTION_KEYS_RETIRED" }}
{{- fail "adapter.extraEnv sets STATE_ENCRYPTION_KEYS_RETIRED. A retired key still opens every row that names it, so it belongs in the state Secret beside the primary key, not in the rendered release." }}
{{- end }}
{{- end }}
{{- end }}
{{- with .Values.adapter.extraEnv }}
{{ toYaml . }}
{{- end }}
{{- end }}

{{/*
Load the compatibility pin shipped with the chart. Fail if the file is
missing, does not parse, is not a map, or is missing required nested maps,
because every version check reads from it. Helm fromYaml stores a parse
failure under Error. Nested maps are checked before later templates index
them.
*/}}
{{- define "pds.compatibilityPin" -}}
{{- $raw := .Files.Get "compatibility.yaml" -}}
{{- if not $raw -}}
{{- fail "compatibility.yaml is missing from the chart. It is the supported version pin." -}}
{{- end -}}
{{- $pin := $raw | fromYaml -}}
{{- if not (kindIs "map" $pin) -}}
{{- fail "compatibility.yaml did not parse. It is the supported version pin." -}}
{{- end -}}
{{- if index $pin "Error" -}}
{{- fail "compatibility.yaml did not parse. It is the supported version pin." -}}
{{- end -}}
{{- if not (kindIs "map" (index $pin "openeverest")) -}}
{{- fail "compatibility.yaml is missing the openeverest key. It is the supported version pin." -}}
{{- end -}}
{{- if not (kindIs "map" (index $pin "providers")) -}}
{{- fail "compatibility.yaml is missing the providers key. It is the supported version pin." -}}
{{- end -}}
{{- if not (kindIs "map" (index $pin "providers" "pxc")) -}}
{{- fail "compatibility.yaml is missing the providers.pxc key. It is the supported version pin." -}}
{{- end -}}
{{- if not (kindIs "map" (index $pin "providers" "postgresql")) -}}
{{- fail "compatibility.yaml is missing the providers.postgresql key. It is the supported version pin." -}}
{{- end -}}
{{- if not (kindIs "map" (index $pin "pmm")) -}}
{{- fail "compatibility.yaml is missing the pmm key. It is the supported version pin." -}}
{{- end -}}
{{- $raw -}}
{{- end }}

{{/*
Fail when a rendered version leaves the pin. name, got, and want are
required. The message names both values and the pin file.
*/}}
{{- define "pds.mustPin" -}}
{{- if ne (printf "%s" .got) (printf "%s" .want) -}}
{{- fail (printf "%s is %q. The compatibility pin requires %q. Use the versions in charts/pds/compatibility.yaml. Mixed OpenEverest, provider, and operator versions are not supported." .name .got .want) -}}
{{- end -}}
{{- end }}

{{/*
The development organization hook. Named after the release so two installs in one
cluster do not share a Job.
*/}}
{{- define "pds.auth.bootstrapHookName" -}}
{{- printf "%s-bootstrap-organization" (include "pds.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "pds.auth.bootstrapSecretName" -}}
{{- if .Values.adapter.auth.bootstrapOrganization.existingSecret -}}
{{- .Values.adapter.auth.bootstrapOrganization.existingSecret -}}
{{- else -}}
{{- printf "%s-bootstrap-organization" (include "pds.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
