{{- define "fortigate-external-dns.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "fortigate-external-dns.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "fortigate-external-dns.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "fortigate-external-dns.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "fortigate-external-dns.selectorLabels" -}}
app.kubernetes.io/name: {{ include "fortigate-external-dns.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "fortigate-external-dns.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "fortigate-external-dns.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- required "serviceAccount.name is required when serviceAccount.create=false" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- /* Pod template (metadata + spec) shared by the Deployment and the once=true Job. Callers indent it with nindent 4. */ -}}
{{- define "fortigate-external-dns.podTemplate" -}}
{{- $platformEnabled := or .Values.platform.targetMode.enabled .Values.platform.sharedOwnership.enabled .Values.platform.planApproval.enabled .Values.platform.policy.enabled .Values.platform.events.enabled .Values.platform.sourceExpansion.externalName.enabled .Values.platform.sourceExpansion.headless.enabled .Values.platform.status.enabled -}}
metadata:
  labels:
    {{- include "fortigate-external-dns.selectorLabels" . | nindent 4 }}
    {{- with .Values.podLabels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  {{- if or .Values.fortigate.caBundle .Values.podAnnotations $platformEnabled }}
  annotations:
    {{- with .Values.podAnnotations }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
    {{- if .Values.fortigate.caBundle }}
    checksum/fortigate-ca: {{ .Values.fortigate.caBundle | sha256sum | quote }}
    {{- end }}
    {{- if $platformEnabled }}
    checksum/platform-values: {{ .Values.platform | toJson | sha256sum | quote }}
    {{- end }}
  {{- end }}
spec:
  {{- if .Values.once }}
  restartPolicy: Never
  {{- end }}
  {{- with .Values.priorityClassName }}
  priorityClassName: {{ . | quote }}
  {{- end }}
  serviceAccountName: {{ include "fortigate-external-dns.serviceAccountName" . }}
  {{- with .Values.imagePullSecrets }}
  imagePullSecrets:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  securityContext:
    {{- toYaml .Values.podSecurityContext | nindent 4 }}
  containers:
    - name: controller
      image: "{{ .Values.image.repository }}{{ if .Values.image.digest }}@{{ .Values.image.digest }}{{ else }}:{{ .Values.image.tag | default .Chart.AppVersion }}{{ end }}"
      imagePullPolicy: {{ .Values.image.pullPolicy }}
      args:
        - --provider=fortigate
        {{- if .Values.platform.targetMode.enabled }}
        - --target-mode
        - --platform-namespace={{ .Release.Namespace }}
        {{- if .Values.platform.events.enabled }}
        - --event-driven
        - --debounce={{ .Values.platform.events.debounce }}
        - --resync={{ .Values.platform.events.resync }}
        {{- end }}
        - --status-retention={{ .Values.platform.status.retention }}
        - --plan-retention={{ .Values.platform.planApproval.retention }}
        {{- end }}
        {{- if .Values.platform.policy.enabled }}
        - --policy-enforcement
        {{- end }}
        {{- if .Values.platform.sourceExpansion.externalName.enabled }}
        - --publish-external-name-services
        {{- end }}
        {{- if .Values.platform.sourceExpansion.headless.enabled }}
        - --publish-headless-services
        {{- end }}
        {{- range .Values.sources }}
        - --source={{ . }}
        {{- end }}
        {{- range .Values.namespaces }}
        - --namespace={{ . }}
        {{- end }}
        {{- range .Values.gatewayTargetNamespaces }}
        - --gateway-target-namespace={{ . }}
        {{- end }}
        {{- range .Values.domainFilters }}
        - --domain-filter={{ . }}
        {{- end }}
        - --owner-id={{ .Values.ownerID }}
        - --default-ttl={{ .Values.defaultTTL }}
        - --cleanup-policy={{ .Values.cleanupPolicy }}
        {{- if .Values.allowEmptyDesiredCleanup }}
        - --allow-empty-desired-cleanup
        {{- end }}
        {{- if .Values.maxCleanupPerCycle }}
        - --max-cleanup-per-cycle={{ .Values.maxCleanupPerCycle }}
        {{- end }}
        - --interval={{ .Values.interval }}
        - --reconcile-timeout={{ .Values.reconcileTimeout }}
        {{- /* The probe/metrics server always binds: liveness and readiness
             probes depend on it. metrics.enabled gates only scrape exposure
             (Service/NetworkPolicy), never this address. */}}
        - --metrics-addr=:{{ .Values.metrics.port }}
        {{- if .Values.healthzMaxStaleness }}
        - --healthz-max-staleness={{ .Values.healthzMaxStaleness }}
        {{- end }}
        {{- if ne .Values.logFormat "text" }}
        - --log-format={{ .Values.logFormat }}
        {{- end }}
        {{- if ne .Values.logLevel "info" }}
        - --log-level={{ .Values.logLevel }}
        {{- end }}
        {{- if .Values.leaderElection.enabled }}
        - --leader-election
        - --leader-election-id={{ .Values.leaderElection.id | default (include "fortigate-external-dns.fullname" .) }}
        - --leader-election-namespace={{ .Values.leaderElection.namespace | default .Release.Namespace }}
        {{- else }}
        - --leader-election=false
        {{- end }}
        {{- if not .Values.platform.targetMode.enabled }}
        - --fortigate-url={{ required "fortigate.url is required" .Values.fortigate.url }}
        - --fortigate-zone={{ required "fortigate.zone is required" .Values.fortigate.zone }}
        - --fortigate-vdom={{ .Values.fortigate.vdom }}
        {{- if .Values.fortigate.exclusiveZoneOwnership }}
        - --fortigate-exclusive-zone-ownership
        {{- end }}
        - --fortigate-timeout={{ .Values.fortigate.timeout }}
        - --fortigate-retries={{ .Values.fortigate.retries }}
        {{- if .Values.dryRun }}
        - --dry-run
        {{- end }}
        {{- if .Values.fortigate.insecureSkipVerify }}
        - --fortigate-insecure-skip-verify
        {{- end }}
        {{- if .Values.fortigate.caBundle }}
        - --fortigate-ca-file=/etc/fortigate-external-dns/ca/ca.crt
        {{- end }}
        {{- end }}
        {{- if .Values.once }}
        - --once
        {{- end }}
      env:
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        {{- if not .Values.platform.targetMode.enabled }}
        - name: FORTIGATE_API_TOKEN
          valueFrom:
            secretKeyRef:
              name: {{ required "fortigate.existingSecret is required" .Values.fortigate.existingSecret }}
              key: {{ .Values.fortigate.apiTokenSecretKey }}
        {{- end }}
      ports:
        - name: metrics
          containerPort: {{ .Values.metrics.port }}
      {{- /* A one-shot Job exits after a single reconcile, so it carries no
           probes: a readiness/liveness failure must not restart or gate it. */}}
      {{- if not .Values.once }}
      {{- /* Liveness tolerates the controller's heartbeat staleness window
           (default 5m at the default interval): 30s x 4 failures adds ~2m of
           kubelet-side confirmation before a wedged pod restarts. */}}
      livenessProbe:
        httpGet:
          path: /healthz
          port: metrics
        initialDelaySeconds: 5
        periodSeconds: 30
        timeoutSeconds: 5
        failureThreshold: 4
      readinessProbe:
        httpGet:
          path: /readyz
          port: metrics
        initialDelaySeconds: 5
        periodSeconds: 10
        timeoutSeconds: 5
        failureThreshold: 3
      {{- end }}
      {{- if .Values.fortigate.caBundle }}
      volumeMounts:
        - name: fortigate-ca
          mountPath: /etc/fortigate-external-dns/ca
          readOnly: true
      {{- end }}
      securityContext:
        {{- toYaml .Values.securityContext | nindent 8 }}
      {{- with .Values.resources }}
      resources:
        {{- toYaml . | nindent 8 }}
      {{- end }}
  {{- if .Values.fortigate.caBundle }}
  volumes:
    - name: fortigate-ca
      configMap:
        name: {{ include "fortigate-external-dns.fullname" . }}-ca
  {{- end }}
  {{- with .Values.nodeSelector }}
  nodeSelector:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with .Values.affinity }}
  affinity:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with .Values.tolerations }}
  tolerations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with .Values.topologySpreadConstraints }}
  topologySpreadConstraints:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end -}}
