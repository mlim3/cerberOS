{{- define "io.name" -}}io{{- end }}

{{- define "io.labels" -}}
app.kubernetes.io/name: io
app.kubernetes.io/component: user-interface
app.kubernetes.io/part-of: cerberos
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "io.selectorLabels" -}}
app.kubernetes.io/name: io
app.kubernetes.io/component: user-interface
{{- end }}
