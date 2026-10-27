{{- define "composite.fullname" -}}
{{- default .Release.Name .Values.fullnameOverride | trunc 55 | trimSuffix "-" -}}
{{- end -}}

{{- define "composite.roleName" -}}
{{- printf "%s-%s" .Release.Namespace (include "composite.fullname" .) -}}
{{- end -}}

{{- define "composite.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "composite.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- required "serviceAccount.name is required when create is false" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "composite.labels" -}}
app.kubernetes.io/name: cluster-api-provider-composite
app.kubernetes.io/instance: {{ .Release.Name | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
cluster.x-k8s.io/provider: infrastructure-composite
{{- end -}}

{{- define "composite.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}
{{- end -}}
