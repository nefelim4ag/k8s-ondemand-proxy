---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ $.Release.Name }}
data:
  config.yaml: |
{{ tpl (.Values.config | toYaml) $ | indent 4}}
