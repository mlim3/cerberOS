{{- define "memory-api.name" -}}memory-api{{- end }}

{{- define "memory-api.labels" -}}
app.kubernetes.io/name: memory-api
app.kubernetes.io/component: memory
app.kubernetes.io/part-of: cerberos
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "memory-api.selectorLabels" -}}
app.kubernetes.io/name: memory-api
app.kubernetes.io/component: memory
{{- end }}
