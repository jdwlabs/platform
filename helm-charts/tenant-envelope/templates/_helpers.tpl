{{/*
Tenant envelope helpers
*/}}

{{/*
Collect unique Helm registry URLs from services list for AppProject sourceRepos.
*/}}
{{- define "envelope.serviceRepos" -}}
{{- $repos := list -}}
{{- range .Values.services -}}
  {{- if not (has .repo $repos) -}}
    {{- $repos = append $repos .repo -}}
  {{- end -}}
{{- end -}}
{{- toJson $repos -}}
{{- end -}}
