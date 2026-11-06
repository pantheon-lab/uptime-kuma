{{- define "uptime-kuma.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* Name of a component, such as "grafana". */}}
{{- define "uptime-kuma.component" -}}
{{- printf "%s-%s" (include "uptime-kuma.fullname" .root) .name -}}
{{- end -}}

{{- define "uptime-kuma.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .root.Chart.Name .root.Chart.Version }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
{{ include "uptime-kuma.selectorLabels" . }}
{{- end -}}

{{- define "uptime-kuma.selectorLabels" -}}
app.kubernetes.io/name: {{ .root.Chart.Name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .name }}
{{- end -}}

{{- define "uptime-kuma.secretName" -}}
{{- default (printf "%s-secrets" (include "uptime-kuma.fullname" .)) .Values.existingSecret -}}
{{- end -}}

{{/*
A secret value: the one given in values, else the one already in the cluster, else a new
random one. Call with (dict "root" $ "key" "ENV_NAME" "value" .Values.x "length" 32).
*/}}
{{- define "uptime-kuma.secretValue" -}}
{{- $existing := lookup "v1" "Secret" .root.Release.Namespace (include "uptime-kuma.secretName" .root) -}}
{{- if .value -}}
{{- .value -}}
{{- else if and $existing (hasKey $existing.data .key) -}}
{{- index $existing.data .key | b64dec -}}
{{- else -}}
{{- randAlphaNum (int .length) -}}
{{- end -}}
{{- end -}}

{{- define "uptime-kuma.image" -}}
{{- printf "%s:%s" .repository (toString .tag) -}}
{{- end -}}

{{- define "uptime-kuma.storageClass" -}}
{{- if . }}
storageClassName: {{ . | quote }}
{{- end }}
{{- end -}}
