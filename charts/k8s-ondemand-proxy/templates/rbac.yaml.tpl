---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ .Release.Name }}
rules:
- apiGroups: ["apps"]
  resources: ["deployments", "deployments/scale", "replicasets", "replicasets/scale", "statefulsets", "statefulsets/scale"]
  verbs: ["get", "watch", "list", "update", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ .Release.Name }}
subjects:
- kind: ServiceAccount
  name: {{ .Release.Name }}
  apiGroup: ""
  namespace: {{ .Release.Namespace }}
roleRef:
  kind: Role
  name: {{ .Release.Name }}
  apiGroup: ""
