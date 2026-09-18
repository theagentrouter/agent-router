// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	egextension "github.com/envoyproxy/gateway/proto/extension"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/envoyproxy/ai-gateway/internal/controller"
	"github.com/envoyproxy/ai-gateway/internal/extensionserver"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/pprof"
	"github.com/envoyproxy/ai-gateway/internal/ratelimit/runner"
)

type flags struct {
	envoyGatewayNamespace          string
	extProcLogLevel                string
	extProcLogFormat               string
	logFormat                      string
	extProcEnableRedaction         bool
	extProcImage                   string
	extProcImagePullPolicy         corev1.PullPolicy
	enableLeaderElection           bool
	logLevel                       zapcore.Level
	extensionServerPort            string
	tlsCertDir                     string
	tlsCertName                    string
	tlsKeyName                     string
	caBundleName                   string
	requestHeaderAttributes        *string
	spanRequestHeaderAttributes    *string
	metricsRequestHeaderAttributes *string
	logRequestHeaderAttributes     *string
	endpointPrefixes               string
	rootPrefix                     string
	extProcExtraEnvVars            string
	extProcImagePullSecrets        string
	// webhookPort is the port for the mutating webhook server.
	webhookPort int
	// extProcMaxRecvMsgSize is the maximum message size in bytes that the gRPC server can receive.
	extProcMaxRecvMsgSize int
	// maxRecvMsgSize is the maximum message size in bytes that the gRPC extension server can receive.
	maxRecvMsgSize                         int
	mcpSessionEncryptionSeed               string
	mcpFallbackSessionEncryptionSeed       string
	mcpSessionEncryptionIterations         int
	mcpFallbackSessionEncryptionIterations int
	watchNamespaces                        []string
	cacheSyncTimeout                       time.Duration
	quotaRateLimitServiceAddr              string
	quotaRateLimitTimeout                  int64
	quotaRateLimitFailureModeDeny          bool
}

func setOptionalString(dst **string) func(string) error {
	return func(value string) error {
		*dst = &value
		return nil
	}
}

// parsePullPolicy parses string into a k8s PullPolicy.
func parsePullPolicy(s string) (corev1.PullPolicy, error) {
	switch corev1.PullPolicy(s) {
	case corev1.PullAlways, corev1.PullNever, corev1.PullIfNotPresent:
		return corev1.PullPolicy(s), nil
	default:
		return "", fmt.Errorf("invalid external processor pull policy: %q", s)
	}
}

// parseWatchNamespaces parses a comma-separated list of namespaces into a slice of strings.
func parseWatchNamespaces(s string) []string {
	var namespaces []string
	for _, n := range strings.Split(s, ",") {
		ns := strings.TrimSpace(n)
		if ns != "" {
			namespaces = append(namespaces, ns)
		}
	}
	return namespaces
}

// parseAndValidateFlags parses the command-line arguments provided in args,
// validates them, and returns the parsed configuration.
func parseAndValidateFlags(args []string) (*flags, error) {
	fs := flag.NewFlagSet("AI Gateway Controller", flag.ContinueOnError)

	envoyGatewayNamespace := fs.String(
		"envoyGatewayNamespace",
		"envoy-gateway-system",
		"The namespace where Envoy Gateway is deployed.",
	)
	extProcLogLevelPtr := fs.String(
		"extProcLogLevel",
		"info",
		"The log level for the external processor. One of 'debug', 'info', 'warn', or 'error'.",
	)
	extProcLogFormatPtr := fs.String(
		"extProcLogFormat",
		internalapi.LogFormatText,
		"The log output format for the external processor. One of 'text' or 'json'.",
	)
	extProcEnableRedactionPtr := fs.Bool(
		"extProcEnableRedaction",
		false,
		"Enable redaction of sensitive information in debug logs for the external processor.",
	)
	extProcImagePtr := fs.String(
		"extProcImage",
		"docker.io/envoyproxy/ai-gateway-extproc:latest",
		"The image for the external processor",
	)
	extProcImagePullPolicyPtr := fs.String(
		"extProcImagePullPolicy",
		"IfNotPresent",
		"The image pull policy for the external processor. One of 'Always', 'Never', 'IfNotPresent'",
	)
	enableLeaderElectionPtr := fs.Bool(
		"enableLeaderElection",
		true,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.",
	)
	logLevelPtr := fs.String(
		"logLevel",
		"info",
		"The log level for the controller manager. One of 'debug', 'info', 'warn', or 'error'.",
	)
	logFormatPtr := fs.String(
		"logFormat",
		internalapi.LogFormatText,
		"The log output format for the controller manager. One of 'text' or 'json'.",
	)
	extensionServerPortPtr := fs.String(
		"port",
		":1063",
		"gRPC port for the extension server",
	)
	webhookPort := fs.Int(
		"webhookPort",
		9443,
		"The port for the mutating webhook server.",
	)
	tlsCertDir := fs.String(
		"tlsCertDir",
		"/certs",
		"The directory containing the TLS certificate and key for the webhook server.",
	)
	caBundleName := fs.String(
		"caBundleName",
		"ca.crt",
		"The name of the CA bundle file.",
	)
	tlsCertName := fs.String(
		"tlsCertName",
		"tls.crt",
		"The name of the TLS certificate file.",
	)
	tlsKeyName := fs.String(
		"tlsKeyName",
		"tls.key",
		"The name of the TLS key file.",
	)
	var requestHeaderAttributes *string
	fs.Func("requestHeaderAttributes",
		"Comma-separated key-value pairs for mapping HTTP request headers to Otel attributes shared across metrics, spans, and access logs. Format: x-tenant-id:tenant.id.",
		setOptionalString(&requestHeaderAttributes),
	)
	var spanRequestHeaderAttributes *string
	fs.Func("spanRequestHeaderAttributes",
		"Comma-separated key-value pairs for mapping HTTP request headers to otel span attributes. Format: agent-session-id:session.id,x-tenant-id:tenant.id. Default: agent-session-id:session.id (when unset). Set to empty to disable.",
		setOptionalString(&spanRequestHeaderAttributes),
	)
	var metricsRequestHeaderAttributes *string
	fs.Func("metricsRequestHeaderAttributes",
		"Comma-separated key-value pairs for mapping HTTP request headers to Otel metric attributes. Format: x-tenant-id:tenant.id,x-tenant-id:tenant.id.",
		setOptionalString(&metricsRequestHeaderAttributes),
	)
	var logRequestHeaderAttributes *string
	fs.Func("logRequestHeaderAttributes",
		"Comma-separated key-value pairs for mapping HTTP request headers to access log attributes. Format: agent-session-id:session.id,x-tenant-id:tenant.id. Default: agent-session-id:session.id (when unset). Set to empty to disable.",
		setOptionalString(&logRequestHeaderAttributes),
	)
	endpointPrefixes := fs.String(
		"endpointPrefixes",
		"",
		"Comma-separated key-value pairs for endpoint prefixes. Format: openai:/,cohere:/cohere,anthropic:/anthropic.",
	)
	rootPrefix := fs.String(
		"rootPrefix",
		"/",
		`The root prefix for all supported endpoints. Default is "/"`,
	)
	extProcExtraEnvVars := fs.String(
		"extProcExtraEnvVars",
		"",
		"Semicolon-separated key=value pairs for extra environment variables in extProc container. Format: OTEL_SERVICE_NAME=ai-gateway;OTEL_TRACES_EXPORTER=otlp",
	)
	extProcImagePullSecrets := fs.String(
		"extProcImagePullSecrets",
		"",
		"Semicolon-separated list of image pull secret names for extProc container. Format: my-registry-secret;another-secret",
	)
	extProcMaxRecvMsgSize := fs.Int(
		"extProcMaxRecvMsgSize",
		512*1024*1024,
		"Maximum message size in bytes that the gRPC server can receive for extProc. Default is 512MB.",
	)
	maxRecvMsgSize := fs.Int(
		"maxRecvMsgSize",
		4*1024*1024,
		"Maximum message size in bytes that the gRPC extension server can receive. Default is 4MB.",
	)
	watchNamespaces := fs.String(
		"watchNamespaces",
		"",
		"Comma-separated list of namespaces to watch. If not set, the controller watches all namespaces.",
	)
	cacheSyncTimeout := fs.Duration(
		"cacheSyncTimeout",
		2*time.Minute, // This is the controller-runtime default
		"Maximum time to wait for k8s caches to sync",
	)
	mcpSessionEncryptionSeed := fs.String("mcpSessionEncryptionSeed", "default-insecure-seed",
		"Seed used to derive the MCP session encryption key. This should be changed and set to a secure value.")
	mcpSessionEncryptionIterations := fs.Int("mcpSessionEncryptionIterations", 100_000,
		"Number of iterations to use for PBKDF2 key derivation for MCP session encryption.")
	mcpFallbackSessionEncryptionSeed := fs.String("mcpFallbackSessionEncryptionSeed", "",
		"Optional fallback seed used for MCP session key rotation")
	mcpFallbackSessionEncryptionIterations := fs.Int("mcpFallbackSessionEncryptionIterations", 100_000,
		"Number of iterations used in the fallback PBKDF2 key derivation for MCP session encryption.")
	quotaRateLimitServiceAddr := fs.String("quotaRateLimitServiceAddr", "envoy-ai-gateway-ratelimit.envoy-gateway-system",
		"Host (or host:port) for the AI Gateway quota rate limit service. If no port is specified, 8081 is used.")
	quotaRateLimitTimeout := fs.Int64("quotaRateLimitTimeout", 5,
		"Timeout in seconds for the quota rate limit service.")
	quotaRateLimitFailureModeDeny := fs.Bool("quotaRateLimitFailureModeDeny", false,
		"If true, the rate limit filter will deny requests when the rate limit service is unavailable.")

	if err := fs.Parse(args); err != nil {
		err = fmt.Errorf("failed to parse flags: %w", err)
		return nil, err
	}

	var slogLevel slog.Level
	if err := slogLevel.UnmarshalText([]byte(*extProcLogLevelPtr)); err != nil {
		err = fmt.Errorf("invalid external processor log level: %q", *extProcLogLevelPtr)
		return nil, err
	}

	var zapLogLevel zapcore.Level
	if err := zapLogLevel.UnmarshalText([]byte(*logLevelPtr)); err != nil {
		err = fmt.Errorf("invalid log level: %q", *logLevelPtr)
		return nil, err
	}

	if err := internalapi.ValidateLogFormat(*logFormatPtr); err != nil {
		return nil, err
	}
	if err := internalapi.ValidateLogFormat(*extProcLogFormatPtr); err != nil {
		return nil, fmt.Errorf("external processor: %w", err)
	}

	extProcPullPolicy, err := parsePullPolicy(*extProcImagePullPolicyPtr)
	if err != nil {
		return nil, err
	}

	// Validate request header attributes if provided.
	if requestHeaderAttributes != nil && *requestHeaderAttributes != "" {
		_, err := internalapi.ParseRequestHeaderAttributeMapping(*requestHeaderAttributes)
		if err != nil {
			return nil, fmt.Errorf("invalid request header attributes: %w", err)
		}
	}

	// Validate tracing header attributes if provided.
	if spanRequestHeaderAttributes != nil && *spanRequestHeaderAttributes != "" {
		_, err := internalapi.ParseRequestHeaderAttributeMapping(*spanRequestHeaderAttributes)
		if err != nil {
			return nil, fmt.Errorf("invalid tracing header attributes: %w", err)
		}
	}

	// Validate metrics header attributes if provided.
	if metricsRequestHeaderAttributes != nil && *metricsRequestHeaderAttributes != "" {
		_, err := internalapi.ParseRequestHeaderAttributeMapping(*metricsRequestHeaderAttributes)
		if err != nil {
			return nil, fmt.Errorf("invalid metrics header attributes: %w", err)
		}
	}

	// Validate access log header attributes if provided.
	if logRequestHeaderAttributes != nil && *logRequestHeaderAttributes != "" {
		_, err := internalapi.ParseRequestHeaderAttributeMapping(*logRequestHeaderAttributes)
		if err != nil {
			return nil, fmt.Errorf("invalid access log header attributes: %w", err)
		}
	}

	// Validate endpoint prefixes if provided.
	if *endpointPrefixes != "" {
		if _, err := internalapi.ParseEndpointPrefixes(*endpointPrefixes); err != nil {
			return nil, fmt.Errorf("invalid endpoint prefixes: %w", err)
		}
	}

	// Validate extProc extra env vars if provided.
	if *extProcExtraEnvVars != "" {
		_, err := controller.ParseExtraEnvVars(*extProcExtraEnvVars)
		if err != nil {
			return nil, fmt.Errorf("invalid extProc extra env vars: %w", err)
		}
	}

	// Validate extProc image pull secrets if provided.
	if *extProcImagePullSecrets != "" {
		_, err := controller.ParseImagePullSecrets(*extProcImagePullSecrets)
		if err != nil {
			return nil, fmt.Errorf("invalid extProc image pull secrets: %w", err)
		}
	}

	if *mcpSessionEncryptionIterations <= 0 {
		return nil, fmt.Errorf("mcp session encryption iterations must be positive: %d", *mcpSessionEncryptionIterations)
	}
	if *mcpFallbackSessionEncryptionSeed != "" && *mcpFallbackSessionEncryptionIterations <= 0 {
		return nil, fmt.Errorf("mcp fallback session encryption iterations must be positive: %d", *mcpFallbackSessionEncryptionIterations)
	}

	return &flags{
		envoyGatewayNamespace:                  *envoyGatewayNamespace,
		extProcLogLevel:                        *extProcLogLevelPtr,
		extProcLogFormat:                       *extProcLogFormatPtr,
		logFormat:                              *logFormatPtr,
		extProcEnableRedaction:                 *extProcEnableRedactionPtr,
		extProcImage:                           *extProcImagePtr,
		extProcImagePullPolicy:                 extProcPullPolicy,
		enableLeaderElection:                   *enableLeaderElectionPtr,
		logLevel:                               zapLogLevel,
		extensionServerPort:                    *extensionServerPortPtr,
		tlsCertDir:                             *tlsCertDir,
		tlsCertName:                            *tlsCertName,
		tlsKeyName:                             *tlsKeyName,
		caBundleName:                           *caBundleName,
		requestHeaderAttributes:                requestHeaderAttributes,
		spanRequestHeaderAttributes:            spanRequestHeaderAttributes,
		metricsRequestHeaderAttributes:         metricsRequestHeaderAttributes,
		logRequestHeaderAttributes:             logRequestHeaderAttributes,
		endpointPrefixes:                       *endpointPrefixes,
		rootPrefix:                             *rootPrefix,
		extProcExtraEnvVars:                    *extProcExtraEnvVars,
		extProcImagePullSecrets:                *extProcImagePullSecrets,
		webhookPort:                            *webhookPort,
		extProcMaxRecvMsgSize:                  *extProcMaxRecvMsgSize,
		maxRecvMsgSize:                         *maxRecvMsgSize,
		watchNamespaces:                        parseWatchNamespaces(*watchNamespaces),
		cacheSyncTimeout:                       *cacheSyncTimeout,
		mcpSessionEncryptionSeed:               *mcpSessionEncryptionSeed,
		mcpFallbackSessionEncryptionSeed:       *mcpFallbackSessionEncryptionSeed,
		mcpSessionEncryptionIterations:         *mcpSessionEncryptionIterations,
		mcpFallbackSessionEncryptionIterations: *mcpFallbackSessionEncryptionIterations,
		quotaRateLimitServiceAddr:              *quotaRateLimitServiceAddr,
		quotaRateLimitTimeout:                  *quotaRateLimitTimeout,
		quotaRateLimitFailureModeDeny:          *quotaRateLimitFailureModeDeny,
	}, nil
}

// newZapOpts builds the controller-runtime logger options for the requested log output format.
//
// zap.UseFlagOptions assigns the whole Options struct, so it has to stay first: an encoder chosen
// before it would be silently discarded and the logger would fall back to console output.
func newZapOpts(logFormat string, level zapcore.LevelEnabler) []zap.Opts {
	opts := []zap.Opts{zap.UseFlagOptions(&zap.Options{Development: true, Level: level})}
	if logFormat == internalapi.LogFormatJSON {
		// Development defaults the encoder to console; override it so structured log pipelines get JSON.
		// zap.JSONEncoder builds the encoder eagerly, which skips the RFC3339 time encoder that
		// controller-runtime would otherwise apply, so pass it explicitly to keep timestamps in the
		// same format the console encoder prints instead of zap's production epoch float.
		opts = append(opts, zap.JSONEncoder(func(ec *zapcore.EncoderConfig) {
			ec.EncodeTime = zapcore.RFC3339TimeEncoder
		}))
	}
	return opts
}

func main() {
	setupLog := ctrl.Log.WithName("setup")

	parsedFlags, err := parseAndValidateFlags(os.Args[1:])
	if err != nil {
		setupLog.Error(err, "failed to parse and validate flags")
		os.Exit(1)
	}

	ctrl.SetLogger(zap.New(newZapOpts(parsedFlags.logFormat, parsedFlags.logLevel)...))
	k8sConfig := ctrl.GetConfigOrDie()

	lis, err := net.Listen("tcp", parsedFlags.extensionServerPort)
	if err != nil {
		setupLog.Error(err, "failed to listen", "port", parsedFlags.extensionServerPort)
		os.Exit(1)
	}

	setupLog.Info("configuring kubernetes cache", "watch-namespaces", parsedFlags.watchNamespaces, "sync-timeout", parsedFlags.cacheSyncTimeout)

	ctx := ctrl.SetupSignalHandler()
	pprof.Run(ctx)
	mgrOpts := ctrl.Options{
		Cache:            setupCache(parsedFlags),
		Controller:       config.Controller{CacheSyncTimeout: parsedFlags.cacheSyncTimeout},
		Scheme:           controller.Scheme,
		LeaderElection:   parsedFlags.enableLeaderElection,
		LeaderElectionID: "envoy-ai-gateway-controller",
		WebhookServer: webhook.NewServer(webhook.Options{
			CertDir:  parsedFlags.tlsCertDir,
			CertName: parsedFlags.tlsCertName,
			KeyName:  parsedFlags.tlsKeyName,
			Port:     parsedFlags.webhookPort,
		}),
	}
	mgr, err := ctrl.NewManager(k8sConfig, mgrOpts)
	if err != nil {
		setupLog.Error(err, "failed to create manager")
		os.Exit(1)
	}

	cli, err := client.New(k8sConfig, client.Options{Scheme: controller.Scheme})
	if err != nil {
		setupLog.Error(err, "failed to create client")
		os.Exit(1)
	}
	if err = maybePatchAdmissionWebhook(ctx, cli, filepath.Join(parsedFlags.tlsCertDir, parsedFlags.caBundleName)); err != nil {
		setupLog.Error(err, "failed to patch admission webhook")
		os.Exit(1)
	}

	// Start the extension server running alongside the controller.
	const extProcUDSPath = "/etc/ai-gateway-extproc-uds/run.sock"
	s := grpc.NewServer(grpc.MaxRecvMsgSize(parsedFlags.maxRecvMsgSize))
	extSrv, err := extensionserver.New(mgr.GetClient(), ctrl.Log, extProcUDSPath, false, parsedFlags.requestHeaderAttributes, parsedFlags.logRequestHeaderAttributes, parsedFlags.quotaRateLimitServiceAddr, parsedFlags.quotaRateLimitTimeout, parsedFlags.quotaRateLimitFailureModeDeny)
	if err != nil {
		setupLog.Error(err, "failed to create extension server")
		os.Exit(1)
	}
	egextension.RegisterEnvoyGatewayExtensionServer(s, extSrv)
	grpc_health_v1.RegisterHealthServer(s, extSrv)
	go func() {
		<-ctx.Done()
		s.GracefulStop()
	}()
	go func() {
		if err := s.Serve(lis); err != nil {
			setupLog.Error(err, "failed to serve extension server")
		}
	}()

	// Start the rate limit xDS config server.
	rlRunner := runner.New(ctrl.Log, runner.DefaultPort)
	go func() {
		if err := rlRunner.Start(ctx); err != nil {
			setupLog.Error(err, "failed to start rate limit xDS server")
			os.Exit(1)
		}
	}()

	// Start the controller.
	if err := controller.StartControllers(ctx, mgr, k8sConfig, ctrl.Log.WithName("controller"), &controller.Options{
		EnvoyGatewayNamespace:                  parsedFlags.envoyGatewayNamespace,
		ExtProcImage:                           parsedFlags.extProcImage,
		ExtProcImagePullPolicy:                 parsedFlags.extProcImagePullPolicy,
		ExtProcLogLevel:                        parsedFlags.extProcLogLevel,
		ExtProcLogFormat:                       parsedFlags.extProcLogFormat,
		ExtProcEnableRedaction:                 parsedFlags.extProcEnableRedaction,
		EnableLeaderElection:                   parsedFlags.enableLeaderElection,
		UDSPath:                                extProcUDSPath,
		RequestHeaderAttributes:                parsedFlags.requestHeaderAttributes,
		TracingRequestHeaderAttributes:         parsedFlags.spanRequestHeaderAttributes,
		MetricsRequestHeaderAttributes:         parsedFlags.metricsRequestHeaderAttributes,
		LogRequestHeaderAttributes:             parsedFlags.logRequestHeaderAttributes,
		EndpointPrefixes:                       parsedFlags.endpointPrefixes,
		RootPrefix:                             parsedFlags.rootPrefix,
		ExtProcExtraEnvVars:                    parsedFlags.extProcExtraEnvVars,
		ExtProcImagePullSecrets:                parsedFlags.extProcImagePullSecrets,
		ExtProcMaxRecvMsgSize:                  parsedFlags.extProcMaxRecvMsgSize,
		MCPSessionEncryptionSeed:               parsedFlags.mcpSessionEncryptionSeed,
		MCPSessionEncryptionIterations:         parsedFlags.mcpSessionEncryptionIterations,
		MCPFallbackSessionEncryptionSeed:       parsedFlags.mcpFallbackSessionEncryptionSeed,
		MCPFallbackSessionEncryptionIterations: parsedFlags.mcpFallbackSessionEncryptionIterations,
		RateLimitRunner:                        rlRunner,
		ExtensionServerInvoked:                 extSrv.PostTranslateModifyInvoked,
	}); err != nil {
		setupLog.Error(err, "failed to start controller")
	}
}

// This should be the name of the mutating webhook configuration in helm chart.
const mutatingWebhookConfigurationName = "envoy-ai-gateway-gateway-pod-mutator"

// maybePatchAdmissionWebhook checks if the CA bundle in the mutating webhook configuration installed as part of the
// helm chart is the same as the one specified in the bundlePath. If not, it updates the CA bundle in the webhook configuration.
//
// This allows users to only need to manage the secret and not take care of mutating webhook configuration.
func maybePatchAdmissionWebhook(ctx context.Context, cli client.Client, bundlePath string) error {
	webhookConfigName := fmt.Sprintf("%s.%s", mutatingWebhookConfigurationName, os.Getenv("POD_NAMESPACE"))
	webhookCfg := &admissionregistrationv1.MutatingWebhookConfiguration{}
	if err := cli.Get(ctx, client.ObjectKey{Name: webhookConfigName}, webhookCfg); err != nil {
		return fmt.Errorf("failed to get mutating webhook configuration: %w", err)
	}

	if len(webhookCfg.Webhooks) != 1 {
		return fmt.Errorf("expected 1 webhook in %s, got %d", webhookConfigName, len(webhookCfg.Webhooks))
	}

	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		return fmt.Errorf("failed to read CA bundle: %w", err)
	}
	if !bytes.Equal(webhookCfg.Webhooks[0].ClientConfig.CABundle, bundle) {
		webhookCfg.Webhooks[0].ClientConfig.CABundle = bundle
		if err := cli.Update(ctx, webhookCfg); err != nil {
			return fmt.Errorf("failed to update mutating webhook configuration: %w", err)
		}
	}
	return nil
}

// setupCache sets up the cache options based on the provided flags.
func setupCache(f *flags) cache.Options {
	var namespaceCacheConfig map[string]cache.Config
	if len(f.watchNamespaces) > 0 {
		namespaceCacheConfig = make(map[string]cache.Config, len(f.watchNamespaces))
		for _, ns := range f.watchNamespaces {
			namespaceCacheConfig[ns] = cache.Config{}
		}
	}

	return cache.Options{
		DefaultNamespaces: namespaceCacheConfig,
		DefaultTransform:  cache.TransformStripManagedFields(),
	}
}
