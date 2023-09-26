---
kind: Deployment
apiVersion: apps/v1
metadata:
  name: {{ .Release.Name }}
  {{- with .Values.deployment.annotations }}
  annotations:
  {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
{{- if .Values.hpa.enabled }}
  replicas: {{ .Values.deployment.replicas }}
{{- end }}
  minReadySeconds: {{ .Values.minReadySeconds }}
  selector:
    matchLabels:
      app: {{ .Release.Name }}
  template:
    metadata:
    {{- with .Values.pod.annotations }}
      annotations:
    {{- toYaml . | nindent 8 }}
    {{- end }}
      labels:
        app: {{ .Release.Name }}
      {{- with .Values.pod.labels }}
        {{- toYaml . | nindent 8 }}
      {{- end }}
    spec:
      serviceAccountName: {{ .Release.Name }}
      serviceAccount: {{ .Release.Name }}
      initContainers: {{ .Values.initContainers | toPrettyJson }}
      containers:
        - name: {{ .Release.Name }}
          {{- if (contains "sha256:" .Values.image.tag) }}
          image: "{{ .Values.image.repository }}@{{ .Values.image.tag }}"
          imagePullPolicy: IfNotPresent
          {{- else }}
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          {{- end }}
          {{- with .Values.command }}
          command: {{ . | toPrettyJson }}
          {{- end }}
          env:
          {{- with .Values.env }}
          {{- range $key, $value := . }}
          - name: {{ $key }}
          {{- if kindIs "string" $value }}
          {{- $v := dict "value" (tpl $value $) }}
          {{- toYaml $v | nindent 12 }}
          {{- else }}
            {{- tpl (toYaml $value) $ | nindent 12 }}
          {{- end }}
          {{- end }}
          {{- end }}
          lifecycle: {{ .Values.lifecycle | toJson }}
          readinessProbe: {{ .Values.readinessProbe | toJson }}
          startupProbe: {{ .Values.startupProbe | toJson }}
          resources: {{ .Values.resources | toJson }}
          ports: {{ .Values.ports | toJson }}
          livenessProbe: {{ .Values.livenessProbe | toJson }}
          volumeMounts: {{ .Values.volumeMounts | toJson }}
      volumes:
      {{- if .Values.volumes }}
{{ tpl ($.Values.volumes | toYaml) $ | indent 6 }}
      {{- end }}
      hostNetwork: {{ .Values.hostNetwork }}
      {{- with .Values.dnsConfig }}
      dnsConfig: {{ . | toJson }}
      {{- end }}
      terminationGracePeriodSeconds: {{ .Values.terminationGracePeriodSeconds }}
      imagePullSecrets: {{ .Values.image.imagePullSecrets | toJson }}
{{- with .Values.topologySpreadConstraints }}
      topologySpreadConstraints:
        {{- tpl (toYaml .) $ | nindent 8 }}
{{- end }}
{{- with .Values.affinity }}
      affinity:
        {{- tpl (toYaml .) $ | nindent 8 }}
{{- end }}
{{- with $.Values.tolerations }}
      tolerations:
        {{- tpl (toYaml .) $ | nindent 8 }}
{{- end }}
  strategy: {{ .Values.strategy | toJson }}
