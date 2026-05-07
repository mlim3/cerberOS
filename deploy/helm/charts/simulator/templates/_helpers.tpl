{{- define "simulator.name" -}}simulator{{- end }}

{{- define "simulator.labels" -}}
app.kubernetes.io/name: simulator
app.kubernetes.io/component: simulator
app.kubernetes.io/part-of: cerberos
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "simulator.selectorLabels" -}}
app.kubernetes.io/name: simulator
app.kubernetes.io/component: simulator
{{- end }}
