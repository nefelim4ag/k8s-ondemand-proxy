---
kind: Service
apiVersion: v1
metadata:
  name: {{ .Release.Name }}
  annotations:
    {{- with .Values.service.annotations }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  labels:
    app: {{ .Release.Name }}
spec:
  ports:
  {{- .Values.service.ports | toYaml | nindent 4 }}
  selector:
    app: {{ .Release.Name }}
  {{- with .Values.service.clusterIP }}
  clusterIP: {{ . }}
  {{- end }}
  type: {{ .Values.service.type }}
{{- if eq .Values.service.type "ClusterIP" }}
---
kind: Service
apiVersion: v1
metadata:
  name: {{ .Release.Name }}-headless
  annotations:
    {{- with .Values.service.annotations }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  labels:
    app: {{ .Release.Name }}
spec:
  ports:
  {{- .Values.service.ports | toYaml | nindent 4 }}
  selector:
    app: {{ .Release.Name }}
  clusterIP: None
  type: ClusterIP
{{- end }}
