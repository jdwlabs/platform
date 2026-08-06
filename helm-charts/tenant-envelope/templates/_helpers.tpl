{{/*
Tenant envelope helpers
*/}}

{{/*
Grafana's classic folder-permission API takes a numeric level, not a name.
1 = View, 2 = Edit. (4 = Admin is deliberately not exposed via the tenant
schema — an admin-privileged team is a control-plane grant, not a tenancy
setting.)
*/}}
{{- define "tenant-envelope.grafanaPermissionLevel" -}}
{{- if eq . "edit" -}}
2
{{- else -}}
1
{{- end -}}
{{- end -}}
