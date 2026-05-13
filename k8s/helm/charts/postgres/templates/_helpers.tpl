{{- define "postgres.name" -}}
{{- "memory-db" }}
{{- end }}

{{- define "postgres.labels" -}}
app.kubernetes.io/name: memory-db
app.kubernetes.io/component: database
app.kubernetes.io/part-of: cerberos
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "postgres.selectorLabels" -}}
app.kubernetes.io/name: memory-db
app.kubernetes.io/component: database
{{- end }}

{{- define "postgres.secretName" -}}
{{ include "postgres.name" . }}-credentials
{{- end }}
