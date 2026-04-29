{{- define "orchestrator.name" -}}orchestrator{{- end }}

{{- define "orchestrator.labels" -}}
app.kubernetes.io/name: orchestrator
app.kubernetes.io/component: control-plane
app.kubernetes.io/part-of: cerberos
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "orchestrator.selectorLabels" -}}
app.kubernetes.io/name: orchestrator
app.kubernetes.io/component: control-plane
{{- end }}
