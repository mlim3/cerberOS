{{- define "openbao.name" -}}
{{- "openbao" }}
{{- end }}

{{- define "openbao.labels" -}}
app.kubernetes.io/name: openbao
app.kubernetes.io/component: credential-store
app.kubernetes.io/part-of: cerberos
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "openbao.selectorLabels" -}}
app.kubernetes.io/name: openbao
app.kubernetes.io/component: credential-store
{{- end }}
