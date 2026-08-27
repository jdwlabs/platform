{{/*
Environment shared verbatim by the controller and node driver containers.
The optional ConfigMap keys stay optional at the env layer too, so an empty
value in values.yaml omits both the key and any startup dependency on it.
*/}}
{{- define "truenas-csi.driverEnv" -}}
- name: CSI_ENDPOINT
  value: unix:///csi/csi.sock
- name: TRUENAS_URL
  valueFrom:
    configMapKeyRef:
      name: truenas-csi-config
      key: truenasURL
- name: TRUENAS_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.apiKeySecret.name }}
      key: {{ .Values.apiKeySecret.key }}
- name: TRUENAS_DEFAULT_POOL
  valueFrom:
    configMapKeyRef:
      name: truenas-csi-config
      key: defaultPool
- name: TRUENAS_NFS_SERVER
  valueFrom:
    configMapKeyRef:
      name: truenas-csi-config
      key: nfsServer
- name: TRUENAS_ISCSI_PORTAL
  valueFrom:
    configMapKeyRef:
      name: truenas-csi-config
      key: iscsiPortal
- name: TRUENAS_NVMEOF_PORTAL
  valueFrom:
    configMapKeyRef:
      name: truenas-csi-config
      key: nvmeofPortal
      optional: true
- name: TRUENAS_ISCSI_IQN_BASE
  valueFrom:
    configMapKeyRef:
      name: truenas-csi-config
      key: iscsiIQNBase
      optional: true
- name: TRUENAS_INSECURE_SKIP_VERIFY
  valueFrom:
    configMapKeyRef:
      name: truenas-csi-config
      key: truenasInsecure
      optional: true
- name: NODE_ID
  valueFrom:
    fieldRef:
      fieldPath: spec.nodeName
{{- end }}
