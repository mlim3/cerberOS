{{- define "aegis-agents.name" -}}aegis-agents{{- end }}

{{- define "aegis-agents.labels" -}}
app.kubernetes.io/name: aegis-agents
app.kubernetes.io/component: agents
app.kubernetes.io/part-of: cerberos
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "aegis-agents.selectorLabels" -}}
app.kubernetes.io/name: aegis-agents
app.kubernetes.io/component: agents
{{- end }}
