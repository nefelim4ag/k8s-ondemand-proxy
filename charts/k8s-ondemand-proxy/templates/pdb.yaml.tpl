{{- if .Values.pdb.enabled }}
{{- with .Values.pdb }}
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {{ $.Release.Name }}
  labels:
    app: {{ $.Release.Name }}
spec:
  selector:
    matchLabels:
      app: {{ $.Release.Name }}
  minAvailable: {{ .minAvailable }}
{{- end }}
{{- end }}
