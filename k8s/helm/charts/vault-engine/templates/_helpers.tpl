{{- define "vault-engine.name" -}}vault{{- end }}

{{- define "vault-engine.labels" -}}
app.kubernetes.io/name: vault
app.kubernetes.io/component: credential-broker
app.kubernetes.io/part-of: cerberos
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "vault-engine.selectorLabels" -}}
app.kubernetes.io/name: vault
app.kubernetes.io/component: credential-broker
{{- end }}
