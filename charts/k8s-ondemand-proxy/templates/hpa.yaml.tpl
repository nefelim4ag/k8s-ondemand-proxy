{{- if .Values.hpa.enabled }}
{{- if ne .Values.hpa.minReplicas .Values.hpa.maxReplicas }}
{{- with .Values.hpa }}
kind: HorizontalPodAutoscaler
apiVersion: autoscaling/v2
metadata:
  name: {{ $.Release.Name }}
spec:
  scaleTargetRef:
    kind: Deployment
    name: {{ $.Release.Name }}
    apiVersion: apps/v1
  minReplicas: {{ .minReplicas }}
  maxReplicas: {{ .maxReplicas }}
  metrics: {{ .metrics | toJson }}
  behavior: {{ .behavior | toJson }}
{{- end }}
{{- end }}
{{- end }}
