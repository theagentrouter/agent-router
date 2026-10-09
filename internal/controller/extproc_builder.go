// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"crypto/sha256"
	"encoding/hex"
	stdjson "encoding/json" //nolint: depguard // the webhook and reconciler need byte-stable hashing; sonic does not guarantee stable field order.
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	aigv1b1 "github.com/envoyproxy/ai-gateway/api/v1beta1"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// extProcConfigHashAnnotationKey stores the desired extproc config hash on
// workload pod templates so Kubernetes rolls pods when injected config changes.
const extProcConfigHashAnnotationKey = "aigateway.envoyproxy.io/extproc-config-hash"

// extProcAdminPort is the admin port of the extproc container.
const extProcAdminPort = 1064

// newExtProcBuilder constructs the shared extproc builder from the controller
// options plus the runtime-resolved extProcAsSideCar flag. It is called once in
// StartControllers; the resulting builder is shared by the mutating webhook (to
// inject the container) and the gateway reconciler (to compute the workload
// template hash), guaranteeing the two sides use identical extproc config.
func newExtProcBuilder(options *Options, extProcAsSideCar bool, logger logr.Logger) *extProcBuilder {
	var parsedEnvVars []corev1.EnvVar
	if options.ExtProcExtraEnvVars != "" {
		var err error
		parsedEnvVars, err = ParseExtraEnvVars(options.ExtProcExtraEnvVars)
		if err != nil {
			logger.Error(err, "failed to parse extProc extra env vars, skipping",
				"envVars", options.ExtProcExtraEnvVars)
		}
	}

	var parsedImagePullSecrets []corev1.LocalObjectReference
	if options.ExtProcImagePullSecrets != "" {
		// ParseImagePullSecrets only splits on ';' and trims; it never returns
		// an error, so there is no defensive log branch here (unlike env vars).
		parsedImagePullSecrets, _ = ParseImagePullSecrets(options.ExtProcImagePullSecrets)
	}

	return &extProcBuilder{
		image:                                  options.ExtProcImage,
		imagePullPolicy:                        options.ExtProcImagePullPolicy,
		logLevel:                               options.ExtProcLogLevel,
		logFormat:                              options.ExtProcLogFormat,
		enableRedaction:                        options.ExtProcEnableRedaction,
		udsPath:                                options.UDSPath,
		requestHeaderAttributes:                options.RequestHeaderAttributes,
		spanRequestHeaderAttributes:            options.TracingRequestHeaderAttributes,
		metricsRequestHeaderAttributes:         options.MetricsRequestHeaderAttributes,
		logRequestHeaderAttributes:             options.LogRequestHeaderAttributes,
		rootPrefix:                             options.RootPrefix,
		endpointPrefixes:                       options.EndpointPrefixes,
		extraEnvVars:                           parsedEnvVars,
		imagePullSecrets:                       parsedImagePullSecrets,
		maxRecvMsgSize:                         options.ExtProcMaxRecvMsgSize,
		extProcAsSideCar:                       extProcAsSideCar,
		mcpSessionEncryptionSeed:               options.MCPSessionEncryptionSeed,
		mcpSessionEncryptionIterations:         options.MCPSessionEncryptionIterations,
		mcpFallbackSessionEncryptionSeed:       options.MCPFallbackSessionEncryptionSeed,
		mcpFallbackSessionEncryptionIterations: options.MCPFallbackSessionEncryptionIterations,
	}
}

// extProcBuilder holds the controller-global extproc configuration used by both
// the mutating webhook and the gateway reconciler.
type extProcBuilder struct {
	image           string
	imagePullPolicy corev1.PullPolicy
	logLevel        string
	logFormat       string
	enableRedaction bool
	udsPath         string

	requestHeaderAttributes        *string
	spanRequestHeaderAttributes    *string
	metricsRequestHeaderAttributes *string
	logRequestHeaderAttributes     *string

	rootPrefix       string
	endpointPrefixes string

	extraEnvVars     []corev1.EnvVar
	imagePullSecrets []corev1.LocalObjectReference
	maxRecvMsgSize   int
	extProcAsSideCar bool

	mcpSessionEncryptionSeed               string
	mcpSessionEncryptionIterations         int
	mcpFallbackSessionEncryptionSeed       string
	mcpFallbackSessionEncryptionIterations int
}

// extProcContainerInput is the per-gateway input for extproc injection.
type extProcContainerInput struct {
	// gatewayConfig is the GatewayConfig referenced by the Gateway (nil if none).
	gatewayConfig *aigv1b1.GatewayConfig
	// needMCP is true when at least one MCPRoute is attached to the Gateway,
	// i.e. the extproc must run the MCP proxy.
	needMCP bool
}

// buildExtProcContainer builds the extproc container without secret-presence
// config args or mounts; those are handled by the webhook at pod creation time.
func (b *extProcBuilder) buildExtProcContainer(input extProcContainerInput) corev1.Container {
	var (
		extProcSpec       *aigv1b1.GatewayConfigExtProc
		kubernetesExtProc *egv1a1.KubernetesContainerSpec
	)
	if extProcSpec = extProcSpecFromInput(input); extProcSpec != nil {
		if extProcSpec.Kubernetes != nil {
			kubernetesExtProc = extProcSpec.Kubernetes
		}
	}

	// Use resources from GatewayConfig if present.
	var resources corev1.ResourceRequirements
	if kubernetesExtProc != nil && kubernetesExtProc.Resources != nil {
		resources = *kubernetesExtProc.Resources
	}

	envVars := b.mergeEnvVars(input.gatewayConfig)
	image := b.extProcImage(input)

	udsMountPath := filepath.Dir(b.udsPath)
	securityContext := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		Privileged:   ptr.To(false),
		RunAsGroup:   ptr.To(int64(65532)),
		RunAsNonRoot: ptr.To(true),
		RunAsUser:    ptr.To(int64(65532)),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
	if kubernetesExtProc != nil && kubernetesExtProc.SecurityContext != nil {
		securityContext = kubernetesExtProc.SecurityContext
	}

	container := corev1.Container{
		Name:            extProcContainerName,
		Image:           image,
		ImagePullPolicy: b.imagePullPolicy,
		Ports: []corev1.ContainerPort{
			{Name: "aigw-admin", ContainerPort: extProcAdminPort},
		},
		Args: b.buildExtProcBaseArgs(input.needMCP),
		Env:  envVars,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      extProcUDSVolumeName,
				MountPath: udsMountPath,
				ReadOnly:  false,
			},
		},
		SecurityContext: securityContext,
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Port:   intstr.FromInt32(extProcAdminPort),
					Path:   "/health",
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 2,
			TimeoutSeconds:      5,
			PeriodSeconds:       10,
			SuccessThreshold:    1,
			FailureThreshold:    1,
		},
		Resources: resources,
	}

	// Mounts contributed by GatewayConfig are part of the drift signal.
	if kubernetesExtProc != nil && len(kubernetesExtProc.VolumeMounts) > 0 {
		container.VolumeMounts = append(container.VolumeMounts, kubernetesExtProc.VolumeMounts...)
	}

	if b.extProcAsSideCar {
		// When running as a sidecar, we want to ensure the extProc container is shutdown last after Envoy is shutdown.
		container.RestartPolicy = ptr.To(corev1.ContainerRestartPolicyAlways)
	}
	return container
}

// extProcConfigDigest contains the config inputs that affect extproc injection.
// It intentionally avoids hashing the full Kubernetes Container API object.
type extProcConfigDigest struct {
	Image           string
	ImagePullPolicy corev1.PullPolicy
	LogLevel        string
	// LogFormat is omitted when it is the default, so deployments that never asked for JSON keep
	// the same hash as before this field existed and are not rolled on upgrade.
	LogFormat                              string `json:",omitempty"`
	EnableRedaction                        bool
	UDSPath                                string
	RequestHeaderAttributes                *string
	SpanRequestHeaderAttributes            *string
	MetricsRequestHeaderAttributes         *string
	LogRequestHeaderAttributes             *string
	RootPrefix                             string
	EndpointPrefixes                       string
	ExtraEnvVars                           []corev1.EnvVar
	ImagePullSecrets                       []corev1.LocalObjectReference
	MaxRecvMsgSize                         int
	ExtProcAsSideCar                       bool
	MCPSessionEncryptionSeed               string
	MCPSessionEncryptionIterations         int
	MCPFallbackSessionEncryptionSeed       string
	MCPFallbackSessionEncryptionIterations int
	NeedMCP                                bool
	GatewayConfigExtProc                   *aigv1b1.GatewayConfigExtProc
}

// extProcContainerHash returns a stable hash of the extproc injection inputs.
func (b *extProcBuilder) extProcContainerHash(input extProcContainerInput) string {
	digest := extProcConfigDigest{
		Image:                                  b.extProcImage(input),
		ImagePullPolicy:                        b.imagePullPolicy,
		LogLevel:                               b.logLevel,
		LogFormat:                              b.nonDefaultLogFormat(),
		EnableRedaction:                        b.enableRedaction,
		UDSPath:                                b.udsPath,
		RequestHeaderAttributes:                b.requestHeaderAttributes,
		SpanRequestHeaderAttributes:            b.spanRequestHeaderAttributes,
		MetricsRequestHeaderAttributes:         b.metricsRequestHeaderAttributes,
		LogRequestHeaderAttributes:             b.logRequestHeaderAttributes,
		RootPrefix:                             b.rootPrefix,
		EndpointPrefixes:                       b.endpointPrefixes,
		ExtraEnvVars:                           b.extraEnvVars,
		ImagePullSecrets:                       b.imagePullSecrets,
		MaxRecvMsgSize:                         b.maxRecvMsgSize,
		ExtProcAsSideCar:                       b.extProcAsSideCar,
		MCPSessionEncryptionSeed:               b.mcpSessionEncryptionSeed,
		MCPSessionEncryptionIterations:         b.mcpSessionEncryptionIterations,
		MCPFallbackSessionEncryptionSeed:       b.mcpFallbackSessionEncryptionSeed,
		MCPFallbackSessionEncryptionIterations: b.mcpFallbackSessionEncryptionIterations,
		NeedMCP:                                input.needMCP,
		GatewayConfigExtProc:                   extProcSpecFromInput(input),
	}
	marshaled, _ := stdjson.Marshal(digest)
	sum := sha256.Sum256(marshaled)
	return hex.EncodeToString(sum[:])
}

func extProcSpecFromInput(input extProcContainerInput) *aigv1b1.GatewayConfigExtProc {
	if input.gatewayConfig == nil {
		return nil
	}
	return input.gatewayConfig.Spec.ExtProc
}

func (b *extProcBuilder) extProcImage(input extProcContainerInput) string {
	return b.resolveExtProcImage(extProcSpecFromInput(input))
}

// nonDefaultLogFormat returns the configured log format only when it differs from the extproc
// default. The extproc already emits text without being told to, so passing it explicitly would
// change the container args and hash of every existing deployment for no behavioral difference.
func (b *extProcBuilder) nonDefaultLogFormat() string {
	if b.logFormat == internalapi.LogFormatText {
		return ""
	}
	return b.logFormat
}

// buildExtProcBaseArgs builds the command line arguments for the extproc
// container excluding the secret-presence-driven -configBundlePath
// flags. The mutating webhook prepends those based on which filter-config
// secrets exist; they are intentionally kept out of the drift hash.
func (b *extProcBuilder) buildExtProcBaseArgs(needMCP bool) []string {
	args := []string{
		"-logLevel", b.logLevel,
		"-extProcAddr", "unix://" + b.udsPath,
		"-adminPort", fmt.Sprintf("%d", extProcAdminPort),
		"-rootPrefix", b.rootPrefix,
		"-maxRecvMsgSize", fmt.Sprintf("%d", b.maxRecvMsgSize),
	}
	if f := b.nonDefaultLogFormat(); f != "" {
		args = append(args, "-logFormat", f)
	}
	if needMCP {
		args = append(args,
			"-mcpAddr", ":"+strconv.Itoa(internalapi.MCPProxyPort),
			"-mcpSessionEncryptionSeed", b.mcpSessionEncryptionSeed,
			"-mcpSessionEncryptionIterations", strconv.Itoa(b.mcpSessionEncryptionIterations),
		)
		if b.mcpFallbackSessionEncryptionSeed != "" {
			args = append(args,
				"-mcpFallbackSessionEncryptionSeed", b.mcpFallbackSessionEncryptionSeed,
				"-mcpFallbackSessionEncryptionIterations", strconv.Itoa(b.mcpFallbackSessionEncryptionIterations),
			)
		}
	}

	if b.requestHeaderAttributes != nil {
		args = append(args, "-requestHeaderAttributes", *b.requestHeaderAttributes)
	}
	if b.spanRequestHeaderAttributes != nil {
		args = append(args, "-spanRequestHeaderAttributes", *b.spanRequestHeaderAttributes)
	}
	if b.metricsRequestHeaderAttributes != nil {
		args = append(args, "-metricsRequestHeaderAttributes", *b.metricsRequestHeaderAttributes)
	}
	if b.logRequestHeaderAttributes != nil {
		args = append(args, "-logRequestHeaderAttributes", *b.logRequestHeaderAttributes)
	}
	if b.endpointPrefixes != "" {
		args = append(args, "-endpointPrefixes", b.endpointPrefixes)
	}
	if b.enableRedaction {
		args = append(args, "-enableRedaction")
	}
	return args
}

// extProcUDSVolumeName is the name of the volume backing the extproc UDS socket,
// shared between the extproc container and the envoy container.
const extProcUDSVolumeName = mutationNamePrefix + "extproc-uds"

// mergeEnvVars merges env vars; GatewayConfig overrides global while preserving order.
func (b *extProcBuilder) mergeEnvVars(gatewayConfig *aigv1b1.GatewayConfig) []corev1.EnvVar {
	result := make([]corev1.EnvVar, 0, len(b.extraEnvVars))
	index := make(map[string]int, len(b.extraEnvVars))

	// Add global env vars first (lowest precedence) preserving input order.
	for _, env := range b.extraEnvVars {
		result = append(result, env)
		index[env.Name] = len(result) - 1
	}

	// Add GatewayConfig env vars (highest precedence) overriding in-place when names collide,
	// otherwise append in the order they are defined.
	if gatewayConfig != nil && gatewayConfig.Spec.ExtProc != nil && gatewayConfig.Spec.ExtProc.Kubernetes != nil {
		for _, env := range gatewayConfig.Spec.ExtProc.Kubernetes.Env {
			if i, ok := index[env.Name]; ok {
				result[i] = env
			} else {
				result = append(result, env)
				index[env.Name] = len(result) - 1
			}
		}
	}

	return result
}

// resolveExtProcImage chooses the extProc image honoring GatewayConfig overrides.
func (b *extProcBuilder) resolveExtProcImage(extProc *aigv1b1.GatewayConfigExtProc) string {
	if extProc == nil || extProc.Kubernetes == nil {
		return b.image
	}

	kubernetesExtProc := extProc.Kubernetes
	switch {
	case kubernetesExtProc.Image != nil:
		return *kubernetesExtProc.Image
	case kubernetesExtProc.ImageRepository != nil:
		return mergeImageWithRepository(b.image, *kubernetesExtProc.ImageRepository)
	default:
		return b.image
	}
}

// mergeImageWithRepository reuses the tag or digest from baseImage when a repository override is provided.
func mergeImageWithRepository(baseImage, repository string) string {
	if repository == "" {
		return baseImage
	}

	suffix := imageTagOrDigest(baseImage)
	if suffix == "" {
		return repository
	}
	return repository + suffix
}

// imageTagOrDigest extracts the tag (":vX") or digest ("@sha256:...") from an image reference.
func imageTagOrDigest(image string) string {
	if image == "" {
		return ""
	}
	if idx := strings.Index(image, "@"); idx != -1 {
		return image[idx:]
	}
	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")
	if lastColon != -1 && lastColon > lastSlash {
		return image[lastColon:]
	}
	return ""
}
