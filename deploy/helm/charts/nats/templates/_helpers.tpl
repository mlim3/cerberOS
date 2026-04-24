{{- define "nats.name" -}}nats{{- end }}

{{- define "nats.labels" -}}
app.kubernetes.io/name: nats
app.kubernetes.io/component: messaging
app.kubernetes.io/part-of: cerberos
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "nats.selectorLabels" -}}
app.kubernetes.io/name: nats
app.kubernetes.io/component: messaging
{{- end }}
