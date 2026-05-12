{{- define "aegis-databus.name" -}}aegis-databus{{- end }}

{{- define "aegis-databus.labels" -}}
app.kubernetes.io/name: aegis-databus
app.kubernetes.io/component: databus
app.kubernetes.io/part-of: cerberos
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "aegis-databus.selectorLabels" -}}
app.kubernetes.io/name: aegis-databus
app.kubernetes.io/component: databus
{{- end }}
