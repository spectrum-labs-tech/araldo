{{- define "araldo.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "araldo.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "araldo.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "araldo.labels" -}}
app.kubernetes.io/name: {{ include "araldo.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "araldo.selector" -}}
app.kubernetes.io/name: {{ include "araldo.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "araldo.image" -}}
{{ .Values.image.repository }}:{{ default .Chart.AppVersion .Values.image.tag }}
{{- end -}}

{{/* Environment shared by server and worker. */}}
{{- define "araldo.env" -}}
- name: ARALDO_BASE_URL
  value: {{ required "config.baseURL is required" .Values.config.baseURL | quote }}
- name: ARALDO_LOG_LEVEL
  value: {{ .Values.config.logLevel | quote }}
- name: ARALDO_AUTO_MIGRATE
  value: {{ not .Values.migrations.job | quote }}
- name: ARALDO_ALLOW_PRIVATE_NETWORKS
  value: {{ .Values.config.allowPrivateNetworks | quote }}
{{- with .Values.config.clientIPHeader }}
- name: ARALDO_CLIENT_IP_HEADER
  value: {{ . | quote }}
{{- end }}
{{- with .Values.extraEnv }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* Metrics export for the long-running roles (server, worker); before araldo.env so extraEnv can override it. */}}
{{- define "araldo.metricsEnv" -}}
{{- if .Values.metrics.enabled }}
- name: OTEL_METRICS_EXPORTER
  value: prometheus
# All interfaces, so the pod IP answers scrapes; the Service does not expose this port.
- name: OTEL_EXPORTER_PROMETHEUS_HOST
  value: "0.0.0.0"
- name: OTEL_EXPORTER_PROMETHEUS_PORT
  value: {{ .Values.metrics.port | quote }}
{{- end }}
{{- end -}}

{{- define "araldo.metricsPort" -}}
{{- if .Values.metrics.enabled }}
- { name: metrics, containerPort: {{ .Values.metrics.port }} }
{{- end }}
{{- end -}}

{{/* PromQL matchers for this release's series. */}}
{{- define "araldo.metricsSelector" -}}
{{- with .Values.metrics.prometheusRule.selector -}}
{{ . }}
{{- else -}}
namespace="{{ .Release.Namespace }}"
{{- if .Values.metrics.podMonitor.enabled }}, job="{{ .Release.Namespace }}/{{ include "araldo.fullname" . }}"{{ end }}
{{- end -}}
{{- end -}}

{{- define "araldo.podSecurity" -}}
securityContext:
  runAsNonRoot: true
  runAsUser: 65532
  runAsGroup: 65532
  seccompProfile: { type: RuntimeDefault }
{{- end -}}

{{- define "araldo.containerSecurity" -}}
securityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities: { drop: [ALL] }
{{- end -}}
