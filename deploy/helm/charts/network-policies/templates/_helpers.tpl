{{- define "network-policies.labels" -}}
app.kubernetes.io/component: network-policy
app.kubernetes.io/part-of: cerberos
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
