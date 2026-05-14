{{- define "kubelet-serving-cert-approver.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kubelet-serving-cert-approver.fullname" -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- printf "%s" $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kubelet-serving-cert-approver.labels" -}}
app.kubernetes.io/name: {{ include "kubelet-serving-cert-approver.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "kubelet-serving-cert-approver.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubelet-serving-cert-approver.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
