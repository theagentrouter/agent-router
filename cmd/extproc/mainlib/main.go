// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mainlib

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/prometheus/client_golang/prometheus"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/envoyproxy/ai-gateway/internal/endpointspec"
	"github.com/envoyproxy/ai-gateway/internal/extproc"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/mcpproxy"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/requestheaderattrs"
	"github.com/envoyproxy/ai-gateway/internal/tracing"
	"github.com/envoyproxy/ai-gateway/internal/version"
)

// extProcFlags is the struct that holds the flags passed to the external processor.
type extProcFlags struct {
	configBundlePath                       string        // path to the sharded configuration bundle directory.
	extProcAddr                            string        // gRPC address for the external processor.
	logLevel                               slog.Level    // log level for the external processor.
	logFormat                              string        // log output format for the external processor, "text" or "json".
	enableRedaction                        bool          // enable redaction of sensitive information in debug logs.
	adminPort                              int           // HTTP port for the admin server (metrics and health).
	requestHeaderAttributes                *string       // comma-separated key-value pairs for mapping HTTP request headers to otel attributes shared across metrics, spans, and access logs.
	spanRequestHeaderAttributes            *string       // comma-separated key-value pairs for mapping HTTP request headers to otel span attributes.
	metricsRequestHeaderAttributes         *string       // comma-separated key-value pairs for mapping HTTP request headers to otel metric attributes.
	logRequestHeaderAttributes             *string       // comma-separated key-value pairs for mapping HTTP request headers to access log attributes.
	mcpAddr                                string        // address for the MCP proxy server which can be either tcp or unix domain socket.
	mcpSessionEncryptionSeed               string        // Seed for deriving the key for encrypting MCP sessions.
	mcpSessionEncryptionIterations         int           // Number of iterations to use for PBKDF2 key derivation for MCP session encryption.
	mcpFallbackSessionEncryptionSeed       string        // Fallback seed for deriving the key for encrypting MCP sessions.
	mcpFallbackSessionEncryptionIterations int           // Number of iterations to use for PBKDF2 key derivation for fallback MCP session encryption.
	mcpWriteTimeout                        time.Duration // the maximum duration before timing out writes of the MCP response.
	// rootPrefix is the root prefix for all the processors.
	rootPrefix string
	// maxRecvMsgSize is the maximum message size in bytes that the gRPC server can receive.
	maxRecvMsgSize int
	// endpointPrefixes is the comma-separated key-value pairs for endpoint prefixes.
	endpointPrefixes string
}

// newLogHandler returns the slog handler for the requested output format. Anything other than
// json gets the text handler, which is what the external processor has always emitted.
func newLogHandler(w io.Writer, level slog.Level, format string) slog.Handler {
	opts := &slog.HandlerOptions{Level: level}
	if format == internalapi.LogFormatJSON {
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewTextHandler(w, opts)
}

func setOptionalString(dst **string) func(string) error {
	return func(value string) error {
		*dst = &value
		return nil
	}
}

// parseAndValidateFlags parses and validates the flags passed to the external processor.
func parseAndValidateFlags(args []string) (extProcFlags, error) {
	var (
		flags extProcFlags
		errs  []error
		fs    = flag.NewFlagSet("AI Gateway External Processor", flag.ContinueOnError)
	)

	fs.StringVar(&flags.configBundlePath,
		"configBundlePath",
		"",
		"path to the sharded configuration bundle directory (index.yaml + parts).",
	)
	fs.StringVar(&flags.extProcAddr,
		"extProcAddr",
		":1063",
		"gRPC address for the external processor. For example, :1063 or unix:///tmp/ext_proc.sock.",
	)
	logLevelPtr := fs.String(
		"logLevel",
		"info",
		"log level for the external processor. One of 'debug', 'info', 'warn', or 'error'.",
	)
	fs.StringVar(&flags.logFormat,
		"logFormat",
		internalapi.LogFormatText,
		"log output format for the external processor. One of 'text' or 'json'.",
	)
	fs.BoolVar(&flags.enableRedaction, "enableRedaction", false,
		"Enable redaction of sensitive information in debug logs.")
	fs.IntVar(&flags.adminPort, "adminPort", 1064, "HTTP port for the admin server (serves /metrics and /health endpoints).")
	fs.Func("requestHeaderAttributes",
		"Comma-separated key-value pairs for mapping HTTP request headers to otel attributes shared across metrics, spans, and access logs. Format: x-tenant-id:tenant.id.",
		setOptionalString(&flags.requestHeaderAttributes),
	)
	fs.Func("spanRequestHeaderAttributes",
		"Comma-separated key-value pairs for mapping HTTP request headers to otel span attributes. Format: agent-session-id:session.id,x-tenant-id:tenant.id. Default: agent-session-id:session.id (when unset). Set to empty to disable.",
		setOptionalString(&flags.spanRequestHeaderAttributes),
	)
	fs.Func("metricsRequestHeaderAttributes",
		"Comma-separated key-value pairs for mapping HTTP request headers to otel metric attributes. Format: x-tenant-id:tenant.id,x-tenant-id:tenant.id.",
		setOptionalString(&flags.metricsRequestHeaderAttributes),
	)
	fs.Func("logRequestHeaderAttributes",
		"Comma-separated key-value pairs for mapping HTTP request headers to access log attributes. Format: agent-session-id:session.id,x-tenant-id:tenant.id. Default: agent-session-id:session.id (when unset). Set to empty to disable.",
		setOptionalString(&flags.logRequestHeaderAttributes),
	)
	fs.StringVar(&flags.rootPrefix,
		"rootPrefix",
		"/",
		"The root path prefix for all the processors.",
	)
	fs.StringVar(&flags.endpointPrefixes,
		"endpointPrefixes",
		"",
		"Comma-separated key-value pairs for endpoint prefixes. Format: openai:/,cohere:/cohere,anthropic:/anthropic.",
	)
	fs.IntVar(&flags.maxRecvMsgSize,
		"maxRecvMsgSize",
		math.MaxInt,
		"Maximum message size in bytes that the gRPC server can receive. Default is unlimited since the flow control should be handled by Envoy.",
	)
	fs.StringVar(&flags.mcpAddr, "mcpAddr", "", "the address (TCP or UDS) for the MCP proxy server, such as :1063 or unix:///tmp/ext_proc.sock. Optional.")
	fs.StringVar(&flags.mcpSessionEncryptionSeed, "mcpSessionEncryptionSeed", "default-insecure-seed",
		"Seed used to derive the MCP session encryption key. This should be changed and set to a secure value.")
	fs.IntVar(&flags.mcpSessionEncryptionIterations, "mcpSessionEncryptionIterations", 100_000,
		"Number of iterations to use for PBKDF2 key derivation for MCP session encryption.")
	fs.StringVar(&flags.mcpFallbackSessionEncryptionSeed, "mcpFallbackSessionEncryptionSeed", "",
		"Optional fallback seed used for MCP session key rotation.")
	fs.IntVar(&flags.mcpFallbackSessionEncryptionIterations, "mcpFallbackSessionEncryptionIterations", 100_000,
		"Number of iterations used in the fallback PBKDF2 key derivation for MCP session encryption.")
	fs.DurationVar(&flags.mcpWriteTimeout, "mcpWriteTimeout", 120*time.Second,
		"The maximum duration before timing out writes of the MCP response")

	if err := fs.Parse(args); err != nil {
		return extProcFlags{}, fmt.Errorf("failed to parse extProcFlags: %w", err)
	}

	if flags.configBundlePath == "" {
		errs = append(errs, fmt.Errorf("configBundlePath must be provided"))
	}
	if err := flags.logLevel.UnmarshalText([]byte(*logLevelPtr)); err != nil {
		errs = append(errs, fmt.Errorf("failed to unmarshal log level: %w", err))
	}
	if err := internalapi.ValidateLogFormat(flags.logFormat); err != nil {
		errs = append(errs, err)
	}
	if flags.requestHeaderAttributes != nil && *flags.requestHeaderAttributes != "" {
		if _, err := internalapi.ParseRequestHeaderAttributeMapping(*flags.requestHeaderAttributes); err != nil {
			errs = append(errs, fmt.Errorf("failed to parse request header mapping: %w", err))
		}
	}
	if flags.spanRequestHeaderAttributes != nil && *flags.spanRequestHeaderAttributes != "" {
		if _, err := internalapi.ParseRequestHeaderAttributeMapping(*flags.spanRequestHeaderAttributes); err != nil {
			errs = append(errs, fmt.Errorf("failed to parse tracing header mapping: %w", err))
		}
	}
	if flags.metricsRequestHeaderAttributes != nil && *flags.metricsRequestHeaderAttributes != "" {
		if _, err := internalapi.ParseRequestHeaderAttributeMapping(*flags.metricsRequestHeaderAttributes); err != nil {
			errs = append(errs, fmt.Errorf("failed to parse metrics header mapping: %w", err))
		}
	}
	if flags.logRequestHeaderAttributes != nil && *flags.logRequestHeaderAttributes != "" {
		if _, err := internalapi.ParseRequestHeaderAttributeMapping(*flags.logRequestHeaderAttributes); err != nil {
			errs = append(errs, fmt.Errorf("failed to parse access log header mapping: %w", err))
		}
	}
	if flags.endpointPrefixes != "" {
		if _, err := internalapi.ParseEndpointPrefixes(flags.endpointPrefixes); err != nil {
			errs = append(errs, fmt.Errorf("failed to parse endpoint prefixes: %w", err))
		}
	}

	return flags, errors.Join(errs...)
}

// Main is a main function for the external processor exposed
// for allowing users to build their own external processor.
//
// * ctx is the context for the external processor.
// * args are the command line arguments passed to the external processor without the program name.
// * stderr is the writer to use for standard error where the external processor will output logs.
//
// This returns an error if the external processor fails to start, or nil otherwise. When the `ctx` is canceled,
// the function will return nil.
func Main(ctx context.Context, args []string, stderr io.Writer) (err error) {
	defer func() {
		// Don't err the caller about normal shutdown scenarios.
		if errors.Is(err, context.Canceled) || errors.Is(err, grpc.ErrServerStopped) {
			err = nil
		}
	}()
	flags, err := parseAndValidateFlags(args)
	if err != nil {
		return fmt.Errorf("failed to parse and validate extProcFlags: %w", err)
	}

	l := slog.New(newLogHandler(stderr, flags.logLevel, flags.logFormat))

	l.Info("starting external processor",
		slog.String("version", version.Parse()),
		slog.String("address", flags.extProcAddr),
		slog.String("configBundlePath", flags.configBundlePath),
	)

	network, address := listenAddress(flags.extProcAddr)
	extProcLis, err := listen(ctx, "external processor", network, address)
	if err != nil {
		return err
	}
	if network == "unix" {
		// Change the permission of the UDS to 0775 so that the envoy process (the same group) can access it.
		err = os.Chmod(address, 0o775)
		if err != nil {
			return fmt.Errorf("failed to change UDS permission: %w", err)
		}
	}

	adminLis, err := listen(ctx, "admin server", "tcp", fmt.Sprintf(":%d", flags.adminPort))
	if err != nil {
		return err
	}

	var mcpLis net.Listener
	if flags.mcpAddr != "" {
		mcpNetwork, mcpAddress := listenAddress(flags.mcpAddr)
		mcpLis, err = listen(ctx, "mcp proxy", mcpNetwork, mcpAddress)
		if err != nil {
			return err
		}
		if mcpNetwork == "unix" {
			// Change the permission of the UDS to 0775 so that the envoy process (the same group) can access it.
			err = os.Chmod(mcpAddress, 0o775)
			if err != nil {
				return fmt.Errorf("failed to change UDS permission: %w", err)
			}
		}
		l.Info("MCP proxy is enabled", "address", flags.mcpAddr)
	}

	spanRequestHeaderAttributes, metricsRequestHeaderAttributes, logRequestHeaderAttributes, err := requestheaderattrs.ResolveAll(
		flags.requestHeaderAttributes,
		flags.spanRequestHeaderAttributes,
		flags.metricsRequestHeaderAttributes,
		flags.logRequestHeaderAttributes,
	)
	if err != nil {
		return err
	}

	// Parse endpoint prefixes and apply defaults for any missing values.
	endpointPrefixes, err := internalapi.ParseEndpointPrefixes(flags.endpointPrefixes)
	if err != nil {
		return fmt.Errorf("failed to parse endpoint prefixes: %w", err)
	}

	tracing, err := tracing.NewTracingFromEnv(ctx, os.Stdout, spanRequestHeaderAttributes)
	if err != nil {
		return err
	}

	// Create Prometheus registry and reader which automatically converts
	// attribute to Prometheus-compatible format (e.g. dots to underscores).
	promRegistry := prometheus.NewRegistry()
	promReader, err := otelprom.New(otelprom.WithRegisterer(promRegistry))
	if err != nil {
		return fmt.Errorf("failed to create prometheus reader: %w", err)
	}

	// Create meter with Prometheus + optionally OTEL.
	meter, metricsShutdown, err := metrics.NewMeterFromEnv(ctx, os.Stdout, promReader)
	if err != nil {
		return fmt.Errorf("failed to create metrics: %w", err)
	}
	chatCompletionMetricsFactory := metrics.NewMetricsFactory(meter, metricsRequestHeaderAttributes, metrics.GenAIOperationChat)
	messagesMetricsFactory := metrics.NewMetricsFactory(meter, metricsRequestHeaderAttributes, metrics.GenAIOperationMessages)
	completionMetricsFactory := metrics.NewMetricsFactory(meter, metricsRequestHeaderAttributes, metrics.GenAIOperationCompletion)
	embeddingsMetricsFactory := metrics.NewMetricsFactory(meter, metricsRequestHeaderAttributes, metrics.GenAIOperationEmbedding)
	imageGenerationMetricsFactory := metrics.NewMetricsFactory(meter, metricsRequestHeaderAttributes, metrics.GenAIOperationImageGeneration)
	responsesMetricsFactory := metrics.NewMetricsFactory(meter, metricsRequestHeaderAttributes, metrics.GenAIOperationResponses)
	speechMetricsFactory := metrics.NewMetricsFactory(meter, metricsRequestHeaderAttributes, metrics.GenAIOperationSpeech)
	transcriptionMetricsFactory := metrics.NewMetricsFactory(meter, metricsRequestHeaderAttributes, metrics.GenAIOperationTranscription)
	translationMetricsFactory := metrics.NewMetricsFactory(meter, metricsRequestHeaderAttributes, metrics.GenAIOperationTranslation)
	rerankMetricsFactory := metrics.NewMetricsFactory(meter, metricsRequestHeaderAttributes, metrics.GenAIOperationRerank)
	tokenizeMetricsFactory := metrics.NewMetricsFactory(meter, metricsRequestHeaderAttributes, metrics.GenAIOperationTokenize)
	responsesInputTokensMetricsFactory := metrics.NewMetricsFactory(meter, metricsRequestHeaderAttributes, metrics.GenAIOperationResponsesInputTokens)
	countTokensMetricsFactory := metrics.NewMetricsFactory(meter, metricsRequestHeaderAttributes, metrics.GenAIOperationCountTokens)
	mcpMetrics := metrics.NewMCP(meter, metricsRequestHeaderAttributes)

	extproc.LogRequestHeaderAttributes = logRequestHeaderAttributes

	server, err := extproc.NewServer(l, flags.enableRedaction)
	if err != nil {
		return fmt.Errorf("failed to create external processor server: %w", err)
	}
	server.Register(path.Join(flags.rootPrefix, endpointPrefixes.OpenAI, "/v1/chat/completions"), extproc.NewFactory(
		chatCompletionMetricsFactory, tracing.ChatCompletionTracer(), endpointspec.ChatCompletionsEndpointSpec{}))
	server.Register(path.Join(flags.rootPrefix, endpointPrefixes.OpenAI, "/v1/completions"), extproc.NewFactory(
		completionMetricsFactory, tracing.CompletionTracer(), endpointspec.CompletionsEndpointSpec{}))
	server.Register(path.Join(flags.rootPrefix, endpointPrefixes.OpenAI, "/v1/embeddings"), extproc.NewFactory(
		embeddingsMetricsFactory, tracing.EmbeddingsTracer(), endpointspec.EmbeddingsEndpointSpec{}))
	server.Register(path.Join(flags.rootPrefix, endpointPrefixes.OpenAI, "/v1/responses"), extproc.NewFactory(
		responsesMetricsFactory, tracing.ResponsesTracer(), endpointspec.ResponsesEndpointSpec{}))
	server.Register(path.Join(flags.rootPrefix, endpointPrefixes.OpenAI, "/v1/responses/input_tokens"), extproc.NewFactory(
		responsesInputTokensMetricsFactory, tracing.ResponsesInputTokensTracer(), endpointspec.ResponsesInputTokensEndpointSpec{}))
	server.Register(path.Join(flags.rootPrefix, endpointPrefixes.OpenAI, "/v1/audio/speech"), extproc.NewFactory(
		speechMetricsFactory, tracing.SpeechTracer(), endpointspec.SpeechEndpointSpec{}))
	server.Register(path.Join(flags.rootPrefix, endpointPrefixes.OpenAI, "/v1/audio/transcriptions"), extproc.NewFactory(
		transcriptionMetricsFactory, tracing.TranscriptionTracer(), endpointspec.TranscriptionEndpointSpec{}))
	server.Register(path.Join(flags.rootPrefix, endpointPrefixes.OpenAI, "/v1/audio/translations"), extproc.NewFactory(
		translationMetricsFactory, tracing.TranslationTracer(), endpointspec.TranslationEndpointSpec{}))
	server.Register(path.Join(flags.rootPrefix, endpointPrefixes.OpenAI, "/v1/images/generations"), extproc.NewFactory(
		imageGenerationMetricsFactory, tracing.ImageGenerationTracer(), endpointspec.ImageGenerationEndpointSpec{}))
	server.Register(path.Join(flags.rootPrefix, endpointPrefixes.Cohere, "/v2/rerank"), extproc.NewFactory(
		rerankMetricsFactory, tracing.RerankTracer(), endpointspec.RerankEndpointSpec{}))
	server.Register(path.Join(flags.rootPrefix, endpointPrefixes.OpenAI, "/v1/models"), extproc.NewModelsProcessor)
	server.Register(path.Join(flags.rootPrefix, endpointPrefixes.Anthropic, "/v1/models"), extproc.NewAnthropicModelsProcessor)
	server.Register(path.Join(flags.rootPrefix, endpointPrefixes.Anthropic, "/v1/messages"), extproc.NewFactory(
		messagesMetricsFactory, tracing.MessageTracer(), endpointspec.MessagesEndpointSpec{}))
	// Use /tokenize to be consistent with vLLM: https://github.com/vllm-project/vllm/blob/344b50d5258d7cf3f136416e1dbcd9b5ee99bb00/vllm/entrypoints/serve/tokenize/api_router.py#L37
	server.Register(path.Join(flags.rootPrefix, endpointPrefixes.OpenAI, "/tokenize"), extproc.NewFactory(
		tokenizeMetricsFactory, tracing.TokenizeTracer(), endpointspec.TokenizeEndpointSpec{}))
	server.Register(path.Join(flags.rootPrefix, endpointPrefixes.Anthropic, "/v1/messages/count_tokens"), extproc.NewFactory(
		countTokensMetricsFactory, tracing.CountTokensTracer(), endpointspec.MessagesCountTokensEndpointSpec{}))

	// Create and register gRPC server with ExternalProcessorServer (the service Envoy calls).
	if err = filterapi.StartConfigBundleWatcher(ctx, flags.configBundlePath, server, l, time.Second*5); err != nil {
		return fmt.Errorf("failed to start config watcher: %w", err)
	}

	var mcpServer *http.Server
	if mcpLis != nil {
		mcpSessionCrypto := mcpproxy.NewPBKDF2AesGcmSessionCrypto(flags.mcpSessionEncryptionSeed, flags.mcpSessionEncryptionIterations)
		if flags.mcpFallbackSessionEncryptionSeed != "" {
			mcpSessionCrypto = &mcpproxy.FallbackEnabledSessionCrypto{
				Primary: mcpSessionCrypto,
				Fallback: mcpproxy.NewPBKDF2AesGcmSessionCrypto(
					flags.mcpFallbackSessionEncryptionSeed,
					flags.mcpFallbackSessionEncryptionIterations,
				),
			}
		}

		var mcpProxyMux *http.ServeMux
		var mcpProxyConfig *mcpproxy.ProxyConfig
		mcpProxyConfig, mcpProxyMux, err = mcpproxy.NewMCPProxy(l.With("component", "mcp-proxy"), mcpMetrics,
			tracing.MCPTracer(), mcpSessionCrypto, logRequestHeaderAttributes)
		if err != nil {
			return fmt.Errorf("failed to create MCP proxy: %w", err)
		}
		if err = filterapi.StartConfigBundleWatcher(ctx, flags.configBundlePath, mcpProxyConfig, l, time.Second*5); err != nil {
			return fmt.Errorf("failed to start config watcher: %w", err)
		}

		mcpServer = &http.Server{
			Handler:           mcpProxyMux,
			ReadHeaderTimeout: 120 * time.Second,
			WriteTimeout:      flags.mcpWriteTimeout,
		}
		go func() {
			l.Info("Starting mcp proxy", "addr", mcpLis.Addr())
			if err2 := mcpServer.Serve(mcpLis); err2 != nil && !errors.Is(err2, http.ErrServerClosed) {
				l.Error("mcp proxy failed", "error", err2)
			}
		}()
	}

	s := grpc.NewServer(grpc.MaxRecvMsgSize(flags.maxRecvMsgSize))
	extprocv3.RegisterExternalProcessorServer(s, server)
	grpc_health_v1.RegisterHealthServer(s, server)

	// Create a gRPC client connection for the above ExternalProcessorServer.
	// This ensures Docker HEALTHCHECK and Kubernetes readiness probes pass
	// only when Envoy considers this external processor healthy.
	healthCheckConn, err := newGrpcClient(extProcLis.Addr())
	if err != nil {
		return fmt.Errorf("failed to create health check client: %w", err)
	}
	healthClient := grpc_health_v1.NewHealthClient(healthCheckConn)

	// Start HTTP admin server for metrics and health checks.
	adminServer := startAdminServer(adminLis, l, promRegistry, healthClient)

	go func() {
		<-ctx.Done()
		s.GracefulStop()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := healthCheckConn.Close(); err != nil {
			l.Error("Failed to close health check client", "error", err)
		}
		if err := adminServer.Shutdown(shutdownCtx); err != nil {
			l.Error("Failed to shutdown admin server gracefully", "error", err)
		}
		if err := tracing.Shutdown(shutdownCtx); err != nil {
			l.Error("Failed to shutdown tracing gracefully", "error", err)
		}
		if err := metricsShutdown(shutdownCtx); err != nil {
			l.Error("Failed to shutdown metrics gracefully", "error", err)
		}
		if mcpServer != nil {
			if err := mcpServer.Shutdown(shutdownCtx); err != nil {
				l.Error("Failed to shutdown mcp proxy server gracefully", "error", err)
			}
		}
	}()

	// Emit startup message to stderr when all listeners are ready.
	// Intentionally not using slog for this to unconditionally emit to stderr. This is important
	// to avoid the deadlock in e2e tests where we wait for this message before proceeding, otherwise
	// it would be extremely hard to debug issues where the external processor fails to start.
	fmt.Fprintf(stderr, "AI Gateway External Processor is ready\n")
	return s.Serve(extProcLis)
}

func listen(ctx context.Context, name, network, address string) (net.Listener, error) {
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, network, address)
	if err != nil {
		return nil, fmt.Errorf("failed to listen for %s: %w", name, err)
	}
	return lis, nil
}

// listenAddress returns the network and address for the given address flag.
func listenAddress(addrFlag string) (string, string) {
	if after, ok := strings.CutPrefix(addrFlag, "unix://"); ok {
		p := after
		_ = os.Remove(p) // Remove the socket file if it exists.
		return "unix", p
	}
	return "tcp", addrFlag
}
