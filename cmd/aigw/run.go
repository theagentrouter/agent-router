// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/envoyproxy/gateway/cmd/envoy-gateway/root"
	egextension "github.com/envoyproxy/gateway/proto/extension"
	"github.com/go-logr/logr"
	"github.com/tetratelabs/func-e/api"
	"github.com/tetratelabs/func-e/experimental/admin"
	"github.com/tetratelabs/func-e/experimental/middleware"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	"sigs.k8s.io/yaml"

	"github.com/envoyproxy/ai-gateway/internal/controller"
	"github.com/envoyproxy/ai-gateway/internal/extensionserver"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/xdg"
)

// This is the template for the Envoy Gateway configuration where PLACEHOLDER_TMPDIR will be replaced with the temporary
// directory where the resources are written to.
//
//go:embed envoy-gateway-config.yaml
var envoyGatewayConfigTemplate string

const (
	substitutionEnvAnnotationPrefix  = "substitution.aigw.run/env/"
	substitutionFileAnnotationPrefix = "substitution.aigw.run/file/"
)

// errExtProcRun is returned when the external processor fails to run.
var errExtProcRun = fmt.Errorf("external processor run error")

type runCmdContext struct {
	// isDebug true if the original `agw run` command is run with debug mode. Using this to
	// set the log level of the external process currently. TODO: maybe simply expose the external process log level
	// at the command line, but that's an implementation detail, so I would rather not right now.
	isDebug bool
	// envoyGatewayResourcesOut is the output file for the envoy gateway resources.
	envoyGatewayResourcesOut io.Writer
	// stderrLogger is the logger for stderr.
	stderrLogger *slog.Logger
	// stderr is the writer for stderr output from subprocesses.
	stderr io.Writer
	// tmpdir is the temporary directory for the resources.
	tmpdir string
	// udsPath is the path to the UDS socket used by the AI Gateway extproc.
	udsPath string
	// adminPort is the HTTP port for the admin server.
	adminPort int
	// extProcLauncher is the function used to launch the external processor.
	extProcLauncher func(ctx context.Context, args []string, w io.Writer) error
	// fakeClientSet is the fake client set for the k8s resources. The objects are written to this client set and updated
	// during the translation.
	fakeClientSet *fake.Clientset
	// mcpSessionEncryptionIterations is the number of iterations for MCP session encryption key derivation.
	mcpSessionEncryptionIterations int
}

// run starts the AI Gateway locally for a given configuration.
//
// All files are written to XDG Base Directory locations:
//  1. State: $AIGW_STATE_HOME/runs/{runID}/ - configs, resources, logs
//  2. Runtime: $AIGW_RUNTIME_DIR/{runID}/ - ephemeral sockets
func run(ctx context.Context, c *cmdRun, o *runOpts, stdout, stderr io.Writer) error {
	start := time.Now()

	// First, we need to create the self-signed certificates used for communication between the EG and Envoy.
	// Certificates will be placed at ~/.config/envoy-gateway/certs, which is the default location used by Envoy Gateway.
	certGenOut := &bytes.Buffer{}
	certGen := root.GetRootCommand(nil)
	certGen.SetOut(certGenOut)
	certGen.SetErr(certGenOut)
	certGen.SetArgs([]string{"certgen", "--local"})
	if err := certGen.ExecuteContext(ctx); err != nil {
		return fmt.Errorf("failed to execute certgen: %w: %s", err, certGenOut.String())
	}

	// Create aigw log file in run directory
	aigwLogFile, err := os.Create(o.logPath)
	if err != nil {
		return fmt.Errorf("failed to create aigw log file %s: %w", o.logPath, err)
	}
	defer aigwLogFile.Close()

	// Tee debug logger to both stderr and log file
	var logWriter io.Writer
	if c.Debug {
		logWriter = io.MultiWriter(stderr, aigwLogFile)
		// keep stdout visible in debug mode
	} else {
		logWriter = aigwLogFile
		stdout = io.Discard // until GW logs are reasonable
	}

	logHandler := slog.NewTextHandler(logWriter, &slog.HandlerOptions{})
	ctrl.SetLogger(logr.FromSlogHandler(logHandler))
	debugLogger := slog.New(logHandler)

	// Write the Envoy Gateway config which points to the resourcesDir to tell Envoy Gateway where to find the resources.
	resourcesDir := filepath.Dir(o.egResourcesPath)
	debugLogger.Info("Writing Envoy Gateway config", "path", o.egConfigPath)
	err = os.WriteFile(o.egConfigPath, []byte(strings.ReplaceAll(
		envoyGatewayConfigTemplate, "PLACEHOLDER_TMPDIR", resourcesDir),
	), 0o600)
	if err != nil {
		return fmt.Errorf("failed to write file %s: %w", o.egConfigPath, err)
	}

	// Write the Envoy Gateway resources into a file under resourcesDir.
	debugLogger.Info("Creating Envoy Gateway resource file", "path", o.egResourcesPath)

	// Do the translation of the given AI Gateway resources Yaml into Envoy Gateway resources and write them to the file.
	resourcesBuf := &bytes.Buffer{}
	runCtx := &runCmdContext{
		isDebug:                        c.Debug,
		envoyGatewayResourcesOut:       resourcesBuf,
		stderrLogger:                   debugLogger,
		stderr:                         stderr,
		tmpdir:                         filepath.Dir(o.logPath), // runDir
		udsPath:                        o.extprocUDSPath,
		adminPort:                      c.AdminPort,
		extProcLauncher:                o.extProcLauncher,
		mcpSessionEncryptionIterations: c.MCPSessionEncryptionIterations,
	}
	// If any of the configured MCP servers is using stdio, set up the streamable HTTP proxies for them
	if err = proxyStdioMCPServers(ctx, debugLogger, c.mcpConfig); err != nil {
		return fmt.Errorf("failed to proxy stdio for MCP servers: %w", err)
	}
	aiGatewayResourcesYaml, err := readConfig(o.configPath, c.mcpConfig, c.Debug)
	if err != nil {
		return err
	}
	fakeClient, extProxDone, listenerPort, err := runCtx.writeEnvoyResourcesAndRunExtProc(ctx, aiGatewayResourcesYaml)
	if err != nil {
		return fmt.Errorf("failed to write envoy resources and run extproc: %w", err)
	}
	err = os.WriteFile(o.egResourcesPath, resourcesBuf.Bytes(), 0o600)
	if err != nil {
		return fmt.Errorf("failed to write file %s: %w", o.egResourcesPath, err)
	}

	// Set up middleware with XDG directories and startup hook now that we know listenerPort
	redirectEnvoyStdio := newEnvoyRunMiddleware(&o.Directories, o.runID, start, listenerPort, stdout, stderr)
	ctx = middleware.WithRunMiddleware(ctx, redirectEnvoyStdio)

	lis, err := net.Listen("tcp", "localhost:1061")
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	s := grpc.NewServer()
	requestHeaderAttributes := envOptional("OTEL_AIGW_REQUEST_HEADER_ATTRIBUTES")
	logRequestHeaderAttributes := envOptional("OTEL_AIGW_LOG_REQUEST_HEADER_ATTRIBUTES")
	quotaRateLimitServiceAddr := "envoy-ai-gateway-ratelimit.envoy-gateway-system"
	const quotaRateLimitTimeout = 5
	const quotaRateLimitFailureModeDeny = false
	extSrv, err := extensionserver.New(fakeClient, ctrl.Log, o.extprocUDSPath, true, requestHeaderAttributes, logRequestHeaderAttributes, quotaRateLimitServiceAddr, quotaRateLimitTimeout, quotaRateLimitFailureModeDeny)
	if err != nil {
		return err
	}
	egextension.RegisterEnvoyGatewayExtensionServer(s, extSrv)
	grpc_health_v1.RegisterHealthServer(s, extSrv)

	serverCtx, serverCancel := context.WithCancel(ctx)

	var extProcErr error
	go func() {
		select {
		case <-ctx.Done():
		case extProcErr = <-extProxDone:
		}
		s.GracefulStop()
		serverCancel()
	}()
	go func() {
		if err := s.Serve(lis); err != nil {
			debugLogger.Error("Failed to run extension server", "error", err)
		}
	}()

	// At this point, we have two things prepared:
	//  1. The Envoy Gateway config in egConfigPath.
	//  2. The Envoy Gateway resources in resourceYamlPath pointed by the config at egConfigPath.
	//
	// Now running the `envoy-gateway` CLI alternative below by passing `--config-path` to `egConfigPath`.
	// Then the agent will read the resources from the file pointed inside the config and start the Envoy process.
	runnerErrorHandler := newRunnerErrorHandler(os.Stderr, serverCancel)
	server := root.GetRootCommand(runnerErrorHandler)

	// TODO: enable the log by default after the issue is resolved: https://github.com/envoyproxy/gateway/issues/6596
	if c.Debug {
		server.SetOut(stdout)
		server.SetErr(stderr)
	} else {
		server.SetOut(io.Discard)
		server.SetErr(io.Discard)
	}
	server.SetArgs([]string{"server", "--config-path", o.egConfigPath})

	// Start the gateway server. This will block until the server is stopped.
	// The startup hook (configured via middleware) will print the status message when Envoy is ready.
	if err := server.ExecuteContext(serverCtx); err != nil {
		return fmt.Errorf("failed to execute server: %w", err)
	}
	return extProcErr
}

// newEnvoyRunMiddleware sets options for running Envoy and returns a middleware
// that configures Envoy I/O, XDG directories, and sets up a startup hook to print the ready message.
func newEnvoyRunMiddleware(dirs *xdg.Directories, runID string, start time.Time, listenerPort int, stdout, stderr io.Writer) func(next api.RunFunc) api.RunFunc {
	// Define startup hook that will be called when Envoy admin is ready
	startupHook := func(_ context.Context, adminClient admin.AdminClient, _ string) error {
		// Print a status message without any timestamp formatting
		startDuration := time.Since(start).Round(100 * time.Millisecond)
		_, _ = fmt.Fprintf(stderr, "Envoy AI Gateway listening on http://localhost:%d (admin http://localhost:%d) after %v\n",
			listenerPort, adminClient.Port(), startDuration)
		return nil
	}

	// Override func-e XDG paths to use aigw's directories and add startup hook
	overrides := []api.RunOption{
		api.EnvoyOut(stdout),
		api.EnvoyErr(stderr),
		// func-e will use these paths instead of its defaults
		api.ConfigHome(dirs.ConfigHome),
		api.DataHome(dirs.DataHome),
		api.StateHome(dirs.StateHome),
		api.RuntimeDir(dirs.RuntimeDir),
		api.RunID(runID),
		admin.WithStartupHook(startupHook),
	}
	return func(next api.RunFunc) api.RunFunc {
		return func(ctx context.Context, args []string, options ...api.RunOption) error {
			return next(ctx, args, append(options, overrides...)...)
		}
	}
}

// recreateDir removes the directory at the given path and creates a new one.
func recreateDir(path string) error {
	err := os.RemoveAll(path)
	if err != nil {
		return fmt.Errorf("failed to remove directory %s: %w", path, err)
	}
	err = os.MkdirAll(path, 0o755)
	if err != nil {
		return fmt.Errorf("failed to create directory %s: %w", path, err)
	}
	return nil
}

// writeEnvoyResourcesAndRunExtProc reads all resources from the given string, writes them to the output file, and runs
// external processes for EnvoyExtensionPolicy resources.
func (runCtx *runCmdContext) writeEnvoyResourcesAndRunExtProc(ctx context.Context, original string) (client.Client, <-chan error, int, error) {
	aigwRoutes, mcpRoutes, aigwBackends, backendSecurityPolicies, backendTLSPolicies, gateways, secrets, _, err := collectObjects(original, runCtx.envoyGatewayResourcesOut, runCtx.stderrLogger)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("error collecting: %w", err)
	}
	if len(gateways) > 1 {
		return nil, nil, 0, fmt.Errorf("multiple gateways are not supported: %s", gateways[0].Name)
	}
	for _, bsp := range backendSecurityPolicies {
		spec := bsp.Spec
		if spec.AWSCredentials != nil && spec.AWSCredentials.OIDCExchangeToken != nil {
			// TODO: We can make it work by generalizing the rotation logic.
			return nil, nil, 0, fmt.Errorf("OIDC exchange token is not supported: %s", bsp.Name)
		}
	}

	// Do the substitution for the secrets.
	for _, s := range secrets {
		if err = runCtx.rewriteSecretWithAnnotatedLocation(s); err != nil {
			return nil, nil, 0, fmt.Errorf("failed to rewrite secret %s: %w", s.Name, err)
		}
	}

	var secretList *corev1.SecretList
	fakeClient, _fakeClientSet, httpRoutes, eps, httpRouteFilters, backends, secretList, backendTrafficPolicies, securityPolicies, err := translateCustomResourceObjects(ctx, aigwRoutes, mcpRoutes, aigwBackends, backendSecurityPolicies, backendTLSPolicies, gateways, secrets, runCtx.stderrLogger)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("error translating: %w", err)
	}
	runCtx.fakeClientSet = _fakeClientSet

	for i := range httpRouteFilters.Items {
		hrf := &httpRouteFilters.Items[i]
		runCtx.mustClearSetOwnerReferencesAndStatusAndWriteObj(&hrf.TypeMeta, hrf)
	}
	for i := range httpRoutes.Items {
		hr := &httpRoutes.Items[i]
		runCtx.mustClearSetOwnerReferencesAndStatusAndWriteObj(&hr.TypeMeta, hr)
	}
	for i := range backends.Items {
		b := &backends.Items[i]
		runCtx.mustClearSetOwnerReferencesAndStatusAndWriteObj(&b.TypeMeta, b)
	}
	for i := range secretList.Items {
		s := &secretList.Items[i]
		runCtx.mustClearSetOwnerReferencesAndStatusAndWriteObj(&s.TypeMeta, s)
	}
	for i := range backendTrafficPolicies.Items {
		btp := &backendTrafficPolicies.Items[i]
		runCtx.mustClearSetOwnerReferencesAndStatusAndWriteObj(&btp.TypeMeta, btp)
	}
	for i := range securityPolicies.Items {
		sp := &securityPolicies.Items[i]
		runCtx.mustClearSetOwnerReferencesAndStatusAndWriteObj(&sp.TypeMeta, sp)
	}
	gw := gateways[0]
	if len(gw.Spec.Listeners) == 0 {
		return nil, nil, 0, fmt.Errorf("gateway %s has no listeners configured", gw.Name)
	}
	runCtx.mustClearSetOwnerReferencesAndStatusAndWriteObj(&gw.TypeMeta, gw)
	for i := range eps.Items {
		ep := &eps.Items[i]
		runCtx.mustClearSetOwnerReferencesAndStatusAndWriteObj(&ep.TypeMeta, ep)
	}

	// Get the filter config from the sharded config secrets.
	filterConfigIndexSecret, err := runCtx.fakeClientSet.CoreV1().
		Secrets("").Get(ctx,
		controller.FilterConfigBundleIndexSecretName(gw.Name, gw.Namespace), metav1.GetOptions{})
	if err != nil {
		return nil, nil, 0, fmt.Errorf("failed to get filter config secret: %w", err)
	}

	filterConfigIndexRaw, ok := filterConfigIndexSecret.StringData[controller.FilterConfigBundleIndexKey]
	if !ok {
		return nil, nil, 0, fmt.Errorf("failed to get filter config index from secret %s", filterConfigIndexSecret.Name)
	}

	filterConfigIndex, err := filterapi.UnmarshalConfigBundleIndex([]byte(filterConfigIndexRaw))
	if err != nil {
		return nil, nil, 0, fmt.Errorf("failed to parse filter config index: %w", err)
	}

	fc, err := filterapi.ReassembleBundleConfig(filterConfigIndex, func(part filterapi.ConfigBundlePart) ([]byte, error) {
		partSecret, getErr := runCtx.fakeClientSet.CoreV1().Secrets("").Get(ctx, part.Name, metav1.GetOptions{})
		if getErr != nil {
			return nil, getErr
		}
		if b, exists := partSecret.Data[controller.FilterConfigBundlePartKey]; exists {
			return b, nil
		}
		return nil, fmt.Errorf("missing key %q in part secret %s", controller.FilterConfigBundlePartKey, part.Name)
	})
	if err != nil {
		return nil, nil, 0, fmt.Errorf("failed to reassemble filter config bundle: %w", err)
	}

	runCtx.stderrLogger.Info("Running external process", "config", *fc)
	done := runCtx.mustStartExtProc(ctx, fc)
	return fakeClient, done, runCtx.tryFindEnvoyListenerPort(gw), nil
}

// mustStartExtProc starts the external process with the given working directory, port, and filter configuration.
func (runCtx *runCmdContext) mustStartExtProc(
	ctx context.Context,
	filterCfg *filterapi.Config,
) <-chan error {
	marshaled, err := yaml.Marshal(filterCfg)
	if err != nil {
		panic(fmt.Sprintf("BUG: failed to marshal filter config: %v", err))
	}
	configBundlePath := filepath.Join(runCtx.tmpdir, "extproc-config-bundle")
	if err = writeExtProcConfigBundle(configBundlePath, marshaled); err != nil {
		panic(fmt.Sprintf("BUG: failed to write extension proc config bundle: %v", err))
	}
	args := []string{
		"--configBundlePath", configBundlePath,
		"--extProcAddr", fmt.Sprintf("unix://%s", runCtx.udsPath),
		"--adminPort", fmt.Sprintf("%d", runCtx.adminPort),
		"--mcpAddr", ":" + strconv.Itoa(internalapi.MCPProxyPort),
		"--mcpSessionEncryptionIterations", strconv.Itoa(runCtx.mcpSessionEncryptionIterations),
	}
	if runCtx.isDebug {
		args = append(args, "--logLevel", "debug")
	} else {
		args = append(args, "--logLevel", "warn")
	}

	if value, ok := os.LookupEnv("OTEL_AIGW_REQUEST_HEADER_ATTRIBUTES"); ok {
		args = append(args, "-requestHeaderAttributes", value)
	}
	if value, ok := os.LookupEnv("OTEL_AIGW_SPAN_REQUEST_HEADER_ATTRIBUTES"); ok {
		args = append(args, "-spanRequestHeaderAttributes", value)
	}
	if value, ok := os.LookupEnv("OTEL_AIGW_METRICS_REQUEST_HEADER_ATTRIBUTES"); ok {
		args = append(args, "-metricsRequestHeaderAttributes", value)
	}
	if value, ok := os.LookupEnv("OTEL_AIGW_LOG_REQUEST_HEADER_ATTRIBUTES"); ok {
		args = append(args, "-logRequestHeaderAttributes", value)
	}

	done := make(chan error)
	go func() {
		if err := runCtx.extProcLauncher(ctx, args, runCtx.stderr); err != nil {
			runCtx.stderrLogger.Error("Failed to run external processor", "error", err)
			done <- fmt.Errorf("%w: %w", errExtProcRun, err)
		}
		close(done)
	}()
	return done
}

func writeExtProcConfigBundle(bundlePath string, raw []byte) error {
	part := filterapi.ConfigBundlePart{
		Name:      "extproc-config",
		Path:      filterapi.ConfigBundlePartPath(0),
		SizeBytes: len(raw),
	}
	partPath := filepath.Join(bundlePath, filepath.FromSlash(part.Path))
	if err := os.MkdirAll(filepath.Dir(partPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(partPath, raw, 0o600); err != nil {
		return err
	}
	indexRaw, err := filterapi.MarshalConfigBundleIndex(&filterapi.ConfigBundleIndex{
		Checksum: filterapi.ConfigBundleChecksum(raw),
		Parts:    []filterapi.ConfigBundlePart{part},
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(bundlePath, filterapi.ConfigBundleIndexFileName), indexRaw, 0o600)
}

func envOptional(name string) *string {
	if value, ok := os.LookupEnv(name); ok {
		return &value
	}
	return nil
}

// mustClearSetOwnerReferencesAndStatusAndWriteObj clears the owner references and status of the given object, marshals it
// to YAML, and writes it to the output file.
//
// The resources must not have these fields set to be run by the Envoy Gateway agent.
//
// All operation here are done in a panic if an error occurs since the error should not happen in practice.
func (runCtx *runCmdContext) mustClearSetOwnerReferencesAndStatusAndWriteObj(typedMeta *metav1.TypeMeta, obj client.Object) {
	obj.SetOwnerReferences(nil)
	mustSetGroupVersionKind(typedMeta, obj)
	marshaled, err := yaml.Marshal(obj)
	if err != nil {
		panic(err)
	}
	var raw map[string]any
	err = yaml.Unmarshal(marshaled, &raw)
	if err != nil {
		panic(err)
	}
	delete(raw, "status")
	marshaled, err = yaml.Marshal(raw)
	if err != nil {
		panic(err)
	}
	_, err = runCtx.envoyGatewayResourcesOut.Write(append([]byte("---\n"), marshaled...))
	if err != nil {
		panic(err)
	}
}

func (runCtx *runCmdContext) rewriteSecretWithAnnotatedLocation(s *corev1.Secret) (err error) {
	allData := s.Data
	if allData == nil {
		allData = make(map[string][]byte)
	}
	for k, v := range s.StringData {
		allData[k] = []byte(v)
	}
	for k, v := range allData {
		envSubKeyAnnotation := substitutionEnvAnnotationPrefix + k
		fileSubKeyAnnotation := substitutionFileAnnotationPrefix + k
		if envSubKey, ok := s.Annotations[envSubKeyAnnotation]; ok {
			// If this is an environment variable, substitute it.
			envVal := os.Getenv(envSubKey)
			if envVal == "" {
				runCtx.stderrLogger.Warn("Missing environment variable, skipping substitution",
					"annotation_key", envSubKey, "env_key", k, "env_substitution_key", envSubKey)
				continue
			}
			runCtx.stderrLogger.Info("Substituting environment variable", "key", k, "value", envSubKey)
			v = []byte(envVal)
		} else if fileSubKey, ok := s.Annotations[fileSubKeyAnnotation]; ok {
			fileSubPath := maybeResolveHome(fileSubKey)
			// Check the target file exists.
			v, err = os.ReadFile(fileSubPath)
			if err != nil {
				runCtx.stderrLogger.Error("Failed to read substitution file. Skipping substitution", "path", fileSubPath, "error", err)
				continue
			}
			runCtx.stderrLogger.Info("Substituting file", "key", k, "value", fileSubKey)
		}
		allData[k] = v
	}
	s.Data = allData
	s.StringData = nil
	return nil
}

// tryFindEnvoyListenerPort tries to find the port where Envoy is listening for Gateway traffic.
// This returns the first listener's port from the Gateway spec, or 0 if no listeners are configured.
func (runCtx *runCmdContext) tryFindEnvoyListenerPort(gw *gwapiv1.Gateway) int {
	if len(gw.Spec.Listeners) == 0 {
		return 0
	}
	return int(gw.Spec.Listeners[0].Port)
}

// newRunnerErrorHandler returns a callback that triggers a graceful shutdown
// (via cancelFunc) when a critical Envoy Gateway runner fails.
func newRunnerErrorHandler(stderr io.Writer, cancelFunc context.CancelFunc) func(string, error) {
	return func(runner string, err error) {
		if runner == string(egv1a1.LogComponentProviderRunner) ||
			runner == string(egv1a1.LogComponentInfrastructureRunner) {
			_, _ = fmt.Fprintf(stderr, "exiting on %s runner error: %v\n", runner, err)
			cancelFunc()
		}
	}
}

func maybeResolveHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return filepath.Join(home, p[2:])
	}
	return p
}
