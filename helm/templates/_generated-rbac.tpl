{{/* Code generated from config/rbac/role.yaml. DO NOT EDIT. */}}
{{- define "composite.rbacRules" -}}
- apiGroups:
  - ""
  - events.k8s.io
  resources:
  - events
  verbs:
  - create
  - patch
- apiGroups:
  - apiextensions.k8s.io
  resources:
  - customresourcedefinitions
  verbs:
  - get
  - list
- apiGroups:
  - authentication.k8s.io
  resources:
  - tokenreviews
  verbs:
  - create
- apiGroups:
  - authorization.k8s.io
  resources:
  - subjectaccessreviews
  verbs:
  - create
- apiGroups:
  - bootstrap.cluster.x-k8s.io
  resources:
  - kubeadmconfigs
  verbs:
  - get
- apiGroups:
  - bootstrap.cluster.x-k8s.io
  resources:
  - kubeadmconfigtemplates
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - cluster.x-k8s.io
  resources:
  - clusters
  - machinedeployments
  - machines
  - machinesets
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - coordination.k8s.io
  resources:
  - leases
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - infrastructure.cluster.x-k8s.io
  resources:
  - '*'
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
{{- end -}}
