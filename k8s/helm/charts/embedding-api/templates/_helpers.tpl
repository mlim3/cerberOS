{{- define "embedding-api.name" -}}embedding-api{{- end }}

{{- define "embedding-api.labels" -}}
app.kubernetes.io/name: embedding-api
app.kubernetes.io/component: ml-inference
app.kubernetes.io/part-of: cerberos
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "embedding-api.selectorLabels" -}}
app.kubernetes.io/name: embedding-api
app.kubernetes.io/component: ml-inference
{{- end }}
