{{- define "pds.notes" -}}
{{ .Chart.Name }} {{ .Chart.Version }} installed as {{ .Release.Name }} in namespace {{ .Release.Namespace }}.

Components:
  control plane  {{ include "pds.image" (dict "image" .Values.adapter.image "root" .) }}
{{- if .Values.ui.enabled }}
  ui             {{ include "pds.image" (dict "image" .Values.ui.image "root" .) }}
{{- end }}

{{- with include "pds.deprecations" . }}

Deprecated values. They work in this release and stop working in the next:
{{ . | indent 2 }}
{{- end }}

{{- if .Values.ui.enabled }}

Reach the UI:
{{- if .Values.ingress.enabled }}
  http{{ if .Values.ingress.tls.enabled }}s{{ end }}://{{ .Values.ingress.host }}/app/
{{- else }}
  kubectl -n {{ .Release.Namespace }} port-forward svc/{{ include "pds.ui.fullname" . }} 8080:{{ .Values.ui.service.port }}
  open http://localhost:8080/app/
{{- end }}

{{- $served := include "pds.hub.organization" . }}
{{- if .Values.adapter.auth.bootstrapOrganization.enabled }}

Sign in as {{ $served | quote }} with the password in
adapter.auth.bootstrapOrganization.password.

That organization is seeded for development. Its password is in this release's values,
so anybody who can read them can sign in. Turn adapter.auth.bootstrapOrganization off
on a customer deployment and onboard an organization instead.
{{- else }}
{{- if eq $served "*" }}

Signing in needs an organization, and nothing in the product creates one. Onboard one
and send the customer the link it prints:

  kubectl -n {{ .Release.Namespace }} exec deploy/{{ include "pds.adapter.fullname" . }} -- \
    adapter organization create <id>

Any valid id is accepted. A session for an organization that has a row stays signed in.
The link is single use and expires, and the customer sets their own password through
it, so Percona never holds one.
{{- else }}

Signing in needs an organization, and nothing in the product creates one. Onboard one
and send the customer the link it prints:

  kubectl -n {{ .Release.Namespace }} exec deploy/{{ include "pds.adapter.fullname" . }} -- \
    adapter organization create {{ $served }}

The id has to be {{ $served | quote }}, which is the organization this release serves
(adapter.state.organization). A session for any other organization reads as signed out. The
link is single use and expires, and the customer sets their own password through
it, so Percona never holds one.
{{- end }}
{{- end }}
{{- else }}

This release runs with no UI.
{{- if eq .Values.adapter.role "agent" }}
This environment dials the control plane and does not serve the API.
Watch it join from the control plane, or follow it here:
  kubectl -n {{ .Release.Namespace }} logs -f deploy/{{ include "pds.adapter.fullname" . }}
{{- end }}
{{- end }}

Verify the release:
  helm test {{ .Release.Name }} -n {{ .Release.Namespace }}

This design-partner preview supports a fresh install of the versions in
charts/pds/compatibility.yaml. In-place upgrades of the platform components
or their CRDs are unsupported.

{{- if .Values.stack.enabled }}
This release includes databases on this cluster:
  Percona Database Service InstancePresets for Dev, Staging, and Prod (MySQL, PostgreSQL and MongoDB) in {{ include "pds.dbNamespace" . }}
{{- if include "pds.stackPMM" . }}
  monitoring {{ .Values.platform.pmm.appVersion }} in {{ include "pds.pmmNamespace" . }}
{{- else }}
  monitoring is not in this release
{{- end }}
{{- if .Values.stack.minio.enabled }}
  MinIO bucket {{ .Values.stack.minio.bucket }} (BackupStorage {{ .Values.stack.minio.storageName }})
{{- end }}
{{- $monitoringConfig := include "pds.monitoringConfig" . }}
{{- if $monitoringConfig }}
  MonitoringConfig {{ $monitoringConfig }} (monitoring sidecars on new databases)
{{- end }}
{{- end }}

{{- if not .Values.servicenow.enabled }}
{{ if not .Values.servicenow.existingSecret }}
ServiceNow is not configured, so /api/support/tickets answers 501 and the UI
hides the support actions. Set servicenow.enabled with an API key and client
token, or point servicenow.existingSecret at a Secret you manage.
{{- end }}
{{- end }}
{{- end }}
