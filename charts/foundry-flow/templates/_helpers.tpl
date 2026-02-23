{{/*
Standard labels for all Foundry Flow resources.
*/}}
{{- define "foundry-flow.labels" -}}
app.kubernetes.io/part-of: foundry-flow
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{/*
Selector labels for a specific component.
*/}}
{{- define "foundry-flow.selectorLabels" -}}
app.kubernetes.io/name: {{ . }}
{{- end }}

{{/*
Event Bus internal address for other services to connect to.
*/}}
{{- define "foundry-flow.eventBusAddress" -}}
{{ .Release.Name }}-eventbus:{{ .Values.eventbus.port }}
{{- end }}

{{/*
Friction Ledger internal address.
*/}}
{{- define "foundry-flow.frictionLedgerAddress" -}}
{{ .Release.Name }}-frictionledger:{{ .Values.frictionledger.port }}
{{- end }}

{{/*
Librarian internal address.
*/}}
{{- define "foundry-flow.librarianAddress" -}}
{{ .Release.Name }}-librarian:{{ .Values.librarian.port }}
{{- end }}

{{/*
Operator internal address.
*/}}
{{- define "foundry-flow.operatorAddress" -}}
{{ .Values.operator.serviceName }}:{{ .Values.operator.port }}
{{- end }}
