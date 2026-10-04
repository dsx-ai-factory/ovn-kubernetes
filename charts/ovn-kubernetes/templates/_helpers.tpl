{{- /*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/ -}}
{{/*
Expand the name of the chart.
*/}}
{{- define "ovn-kubernetes.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "ovn-kubernetes.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "ovn-kubernetes.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "ovn-kubernetes.labels" -}}
helm.sh/chart: {{ include "ovn-kubernetes.chart" . }}
{{ include "ovn-kubernetes.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "ovn-kubernetes.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ovn-kubernetes.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "ovn-kubernetes.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "ovn-kubernetes.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create dockerconfigjson to access container registry
*/}}
{{- define "ovn-kubernetes.dockerconfigjson" -}}
{{- printf "{\"auths\": {\"%s\": {\"auth\": \"%s\"}}}" .registry .auth }}
{{- end }}

{{/*
Get coredns cluster IP
*/}}
{{- define "ovn-kubernetes.corednsClusterIP" -}}
{{- if .Values.global.skipCallToK8s }}
  {{- print "10.96.0.10" }}
{{- else }}
  {{- $svcName := .Values.global.coreDNSServiceName | default "kube-dns" -}}
  {{- $service := lookup "v1" "Service" "kube-system" $svcName -}}
  {{- if $service -}}
    {{- $service.spec.clusterIP -}}
  {{- else }}
    {{- required "global.corednsClusterIP is required when the CoreDNS service cannot be looked up" .Values.global.corednsClusterIP }}
  {{- end }}
{{- end }}
{{- end }}

{{/*
Check if namespace spec is needed or not
- if namespace doesn't exist, return "true"
- if namespace exists but not managed by Helm, return "true", otherwise the namespace will be deleted
*/}}
{{- define "ovn-kubernetes.needNamespace" -}}
{{- $ns := lookup "v1" "Namespace" "" . }}
{{- if not $ns }}
{{- print "true" }}
{{- else }}
{{- $managedBy := get $ns.metadata.labels "app.kubernetes.io/managed-by" }}
  {{- if eq $managedBy "Helm" }}
    {{- print "true" }}
  {{- else }}
    {{- print "false" }}
  {{- end }}
{{- end }}
{{- end }}

{{/*
Get gateway options
*/}}
{{- define "ovn-kubernetes.getGatewayOptions" -}}
{{- $gwOptions := .gatewayOptionsVal | default "" }}
{{- trim $gwOptions }}
{{- end }}

{{/*
Generate full container image string by combining repository, tag and possible sha256 signature
*/}}

{{- define "ovn-kubernetes.generateContainerImageString" -}}
  {{- $repository := .image.repository }}
  {{- /* A first path segment carrying a "." or a ":" (or spelled "localhost") is a
         registry host, so the repository is already fully qualified and callers
         passing one must not have the prefix prepended on top of it. */}}
  {{- $host := splitList "/" $repository | first }}
  {{- if not (or (contains "." $host) (contains ":" $host) (eq $host "localhost")) }}
    {{- $prefix := required "global.imageRepositoryPrefix is required unless image.repository includes a registry host" .Values.global.imageRepositoryPrefix | trimSuffix "/" }}
    {{- $repository = printf "%s/%s" $prefix $repository }}
  {{- end }}
  {{- $imageString := printf "%s:%s" $repository .image.tag }}
  {{- if .Values.global.useImageDigest | default false }}
    {{- if empty .image.sha256 }}
      {{ fail "sha256 signature is required for ngn clusters" }}
    {{- else }}
      {{- $imageString = printf "%s@sha256:%s" $imageString .image.sha256 }}
    {{- end }}
  {{- end }}
  {{- trim $imageString }}
{{- end }}

{{/*
getClusterDefaultNad returns the name of cluster wide default net-attach-def
*/}}
{{- define "ovn-kubernetes.getClusterDefaultNad" }}
{{- $clusterDefaultNad := hasKey .Values.global "clusterDefaultNad" | ternary .Values.global.clusterDefaultNad "" }}
{{- if eq $clusterDefaultNad "" }}
  {{- $clusterDefaultNad = "default/cluster-network" }}
{{- end }}
{{- print $clusterDefaultNad }}
{{- end }}

{{/*
Additional groups authenticated by the Kubernetes API server that receive the
ovn-node permissions. Keep the existing service-account/OVN identity subjects.
*/}}
{{- define "ovn-kubernetes.ovnNodeExtraGroupSubjects" -}}
{{- range .Values.global.ovnNodeExtraGroups }}
- kind: Group
  name: {{ . | quote }}
  apiGroup: rbac.authorization.k8s.io
{{- end }}
{{- end }}
