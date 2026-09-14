// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2elib

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"

	"github.com/envoyproxy/ai-gateway/internal/json"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	testsinternal "github.com/envoyproxy/ai-gateway/tests/internal"
)

const (
	// EnvoyGatewayNamespace is the namespace where the Envoy Gateway is installed.
	EnvoyGatewayNamespace = "envoy-gateway-system"
	// EnvoyGatewayDefaultServicePort is the default service port for the Envoy Gateway.
	EnvoyGatewayDefaultServicePort = 80
	// EnvoyGatewayLatestVersion is the Envoy Gateway main build, installed by default. It has
	// features that aren't in a tagged release yet.
	EnvoyGatewayLatestVersion = "v0.0.0-latest"

	kindLogDir     = "./logs"
	metallbVersion = "v0.13.10"
)

// By default, kind logs are collected when the e2e tests fail. The TEST_KEEP_CLUSTER environment variable
// can be set to "true" to preserve the logs and the kind cluster even if the tests pass.
var KeepCluster = func() bool {
	v, _ := os.LookupEnv("TEST_KEEP_CLUSTER")
	return v == "true"
}()

func initLog(msg string) {
	fmt.Printf("\u001b[32m=== INIT LOG: %s\u001B[0m\n", msg)
}

func cleanupLog(msg string) {
	fmt.Printf("\u001b[32m=== CLEANUP LOG: %s\u001B[0m\n", msg)
}

// AIGatewayHelmOption contains options for installing the AI Gateway Helm chart.
type AIGatewayHelmOption struct {
	// ChartVersion is the version of the AI Gateway Helm chart to install. If empty, the locally built chart is used.
	// Otherwise, it should be a valid semver version string and the chart will be pulled from the Docker Hub OCI registry.
	//
	// For example, giving "v0.3.0" here will result in install oci://docker.io/envoyproxy/ai-gateway-helm --version v0.3.0.
	ChartVersion string
	// AdditionalArgs are additional arguments to pass to the Helm install/upgrade command.
	AdditionalArgs []string
	// Namespace where the AI Gateway will be installed. Default is "envoy-ai-gateway-system".
	Namespace string
}

func (a *AIGatewayHelmOption) GetNamespace() string {
	if a.Namespace == "" {
		return "envoy-ai-gateway-system"
	}
	return a.Namespace
}

// TestMain is the entry point for the e2e tests. It sets up the kind cluster, installs the Envoy Gateway,
// and installs the AI Gateway. It can be called with additional flags for the AI Gateway Helm chart.
//
// When the inferenceExtension flag is set to true, it also installs the Inference Extension and the
// Inference Pool resources, and the Envoy Gateway configuration which are required for the tests.
func TestMain(m *testing.M, aigwOpts AIGatewayHelmOption, inferenceExtension, needPrometheus bool) {
	const defaultKindClusterName = "envoy-ai-gateway"
	err := SetupAll(context.Background(), defaultKindClusterName, aigwOpts, inferenceExtension, needPrometheus)
	if err != nil {
		CleanupKindCluster(true, defaultKindClusterName)
		fmt.Printf("Failed to set up the test environment: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	CleanupKindCluster(code != 0, defaultKindClusterName)
	os.Exit(code) // nolint: gocritic
}

// SetupAll sets up the kind cluster, installs the Envoy Gateway, and installs the AI Gateway.
func SetupAll(ctx context.Context, clusterName string, aigwOpts AIGatewayHelmOption, inferenceExtension, needPrometheus bool) error {
	var cancel context.CancelFunc
	ctx, cancel = context.WithDeadline(ctx, time.Now().Add(5*time.Minute))
	defer cancel()
	// The following code sets up the kind cluster, installs the Envoy Gateway, and installs the AI Gateway.
	// They must be idempotent and can be run multiple times so that we can run the tests multiple times on
	// failures.
	if err := initKindCluster(ctx, clusterName, inferenceExtension); err != nil {
		return fmt.Errorf("failed to initialize kind cluster: %w", err)
	}
	if err := initMetalLB(ctx); err != nil {
		return fmt.Errorf("failed to initialize MetalLB: %w", err)
	}
	if inferenceExtension {
		if err := installInferencePoolEnvironment(ctx); err != nil {
			return fmt.Errorf("failed to install inference pool environment: %w", err)
		}
	}
	if err := initEnvoyGateway(ctx, aigwOpts.GetNamespace(), inferenceExtension); err != nil {
		return fmt.Errorf("failed to initialize Envoy Gateway: %w", err)
	}

	if err := InstallOrUpgradeAIGateway(ctx, aigwOpts); err != nil {
		return fmt.Errorf("failed to install or upgrade AI Gateway: %w", err)
	}

	if needPrometheus {
		if err := initPrometheus(ctx); err != nil {
			return fmt.Errorf("failed to initialize Prometheus: %w", err)
		}
	}
	return nil
}

func initKindCluster(ctx context.Context, clusterName string, inferenceExtension bool) (err error) {
	initLog("Setting up the kind cluster")
	start := time.Now()
	defer func() {
		elapsed := time.Since(start)
		initLog(fmt.Sprintf("\tdone (took %.2fs in total)", elapsed.Seconds()))
	}()

	args := []string{"create", "cluster", "--name", clusterName}
	// If K8S_VERSION is set, use the specified Kubernetes version for the kind node image.
	if k8sVersion := os.Getenv("K8S_VERSION"); k8sVersion != "" {
		args = append(args, "--image", "kindest/node:"+k8sVersion)
		initLog(fmt.Sprintf("\tUsing Kubernetes version %s for kind cluster", k8sVersion))
	}
	initLog(fmt.Sprintf("\tCreating kind cluster named %s", clusterName))
	out, err := testsinternal.RunGoToolContext(ctx, "kind", args...)
	if err != nil && !strings.Contains(err.Error(), "already exist") {
		fmt.Printf("Error creating kind cluster: %s\n", out)
		return
	}

	initLog(fmt.Sprintf("\tSwitching kubectl context to %s", clusterName))
	cmd := testsinternal.GoToolCmdContext(ctx, "kind", "export", "kubeconfig", "--name", clusterName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err = cmd.Run(); err != nil {
		return
	}

	initLog("\tLoading Docker images into kind cluster")
	loadImages := []string{
		"docker.io/envoyproxy/ai-gateway-controller:latest",
		"docker.io/envoyproxy/ai-gateway-extproc:latest",
		"docker.io/envoyproxy/ai-gateway-testupstream:latest",
		"docker.io/envoyproxy/ai-gateway-testmcpserver:latest",
		"docker.io/envoyproxy/ai-gateway-testextauthserver:latest",
	}
	if inferenceExtension {
		loadImages = append(loadImages,
			// TODO: remvoe this after upstream issue fixed.
			// see https://github.com/kubernetes-sigs/gateway-api-inference-extension/issues/3035
			"registry.k8s.io/gateway-api-inference-extension/lwepp:v1.6.0")
	}
	for _, image := range loadImages {
		cmd := testsinternal.GoToolCmdContext(ctx, "kind", "load", "docker-image", image, "--name", clusterName)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err = cmd.Run(); err != nil {
			return
		}
	}
	return nil
}

func initMetalLB(ctx context.Context) (err error) {
	initLog("Installing MetalLB")
	start := time.Now()
	defer func() {
		elapsed := time.Since(start)
		initLog(fmt.Sprintf("\tdone (took %.2fs in total)", elapsed.Seconds()))
	}()

	// Install MetalLB manifests.
	initLog("\tApplying MetalLB manifests")
	manifestURL := fmt.Sprintf("https://raw.githubusercontent.com/metallb/metallb/%s/config/manifests/metallb-native.yaml", metallbVersion)
	if err = KubectlApplyManifest(ctx, manifestURL); err != nil {
		return fmt.Errorf("failed to apply MetalLB manifests: %w", err)
	}

	// Create memberlist secret if it doesn't exist.
	initLog("\tCreating memberlist secret if needed")
	cmd := Kubectl(ctx, "get", "secret", "-n", "metallb-system", "memberlist", "--no-headers", "--ignore-not-found", "-o", "custom-columns=NAME:.metadata.name")
	cmd.Stdout = nil
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check memberlist secret: %w", err)
	}

	if strings.TrimSpace(string(out)) == "" {
		// Generate random secret key.
		cmd = exec.CommandContext(ctx, "openssl", "rand", "-base64", "128")
		cmd.Stderr = os.Stderr
		var secretKey []byte
		secretKey, err = cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to generate secret key: %w", err)
		}

		cmd = Kubectl(ctx, "create", "secret", "generic", "-n", "metallb-system", "memberlist", "--from-literal=secretkey="+strings.TrimSpace(string(secretKey)))
		if err = cmd.Run(); err != nil {
			return fmt.Errorf("failed to create memberlist secret: %w", err)
		}
	}

	// Wait for MetalLB deployments to be ready.
	initLog("\tWaiting for MetalLB controller deployment to be ready")
	if err = kubectlWaitForDeploymentReady(ctx, "metallb-system", "controller"); err != nil {
		return fmt.Errorf("failed to wait for MetalLB controller: %w", err)
	}

	initLog("\tWaiting for MetalLB speaker daemonset to be ready")
	if err = kubectlWaitForDaemonSetReady(ctx, "metallb-system", "speaker"); err != nil {
		return fmt.Errorf("failed to wait for MetalLB speaker: %w", err)
	}

	// Configure IP address pools based on Docker network IPAM.
	initLog("\tConfiguring IP address pools")
	if err = configureMetalLBAddressPools(ctx); err != nil {
		return fmt.Errorf("failed to configure MetalLB address pools: %w", err)
	}

	return nil
}

func configureMetalLBAddressPools(ctx context.Context) error {
	// Get Docker network information for kind cluster.
	cmd := exec.CommandContext(ctx, "docker", "network", "inspect", "kind")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to inspect docker network: %w", err)
	}

	// Parse JSON output.
	var networks []struct {
		IPAM struct {
			Config []struct {
				Subnet string `json:"Subnet"`
			} `json:"Config"`
		} `json:"IPAM"`
	}

	if err := json.Unmarshal(out, &networks); err != nil {
		return fmt.Errorf("failed to parse docker network info: %w", err)
	}

	if len(networks) == 0 || len(networks[0].IPAM.Config) == 0 {
		return fmt.Errorf("no IPAM config found in docker network")
	}

	// Find IPv4 subnet and calculate address range.
	var addressRanges []string
	for _, config := range networks[0].IPAM.Config {
		subnet := config.Subnet
		if !strings.Contains(subnet, ":") { // IPv4.
			// Extract network prefix (e.g., "172.18.0.0/16" -> "172.18.0").
			parts := strings.Split(subnet, ".")
			if len(parts) >= 3 {
				addressPrefix := strings.Join(parts[:3], ".")
				addressRange := fmt.Sprintf("%s.200-%s.250", addressPrefix, addressPrefix)
				addressRanges = append(addressRanges, fmt.Sprintf("    - %s", addressRange))
			}
		}
	}

	if len(addressRanges) == 0 {
		return fmt.Errorf("no valid IPv4 address ranges found")
	}

	// Create MetalLB configuration manifest.
	manifest := fmt.Sprintf(`apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  namespace: metallb-system
  name: kube-services
spec:
  addresses:
%s
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: kube-services
  namespace: metallb-system
spec:
  ipAddressPools:
  - kube-services`, strings.Join(addressRanges, "\n"))

	// Apply configuration with retries.
	const retryInterval = 5 * time.Second
	const timeout = 2 * time.Minute

	retryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error
	attempt := 1

	for {
		select {
		case <-retryCtx.Done():
			if lastErr != nil {
				return fmt.Errorf("timeout applying MetalLB configuration after %d attempts, last error: %w", attempt-1, lastErr)
			}
			return fmt.Errorf("timeout applying MetalLB configuration after %d attempts", attempt-1)
		default:
			if err := KubectlApplyManifestStdin(ctx, manifest); err == nil {
				return nil
			} else {
				lastErr = err
				if strings.Contains(err.Error(), "webhook") && strings.Contains(err.Error(), "connection refused") {
					// This is expected during MetalLB startup, continue retrying.
					fmt.Printf("\t\tAttempt %d: MetalLB webhook not ready yet, retrying in %v...\n", attempt, retryInterval)
				} else {
					// Other errors might be more serious, but still retry.
					fmt.Printf("\t\tAttempt %d: Error applying MetalLB config: %v, retrying in %v...\n", attempt, err, retryInterval)
				}
				attempt++
				time.Sleep(retryInterval)
			}
		}
	}
}

// CleanupKindCluster cleans up the kind cluster if the test succeeds and the
// TEST_KEEP_CLUSTER environment variable is not set to "true".
//
// Also, if the tests failed or TEST_KEEP_CLUSTER is "true", it collects the kind logs
// into the ./logs directory.
func CleanupKindCluster(testsFailed bool, clusterName string) {
	if testsFailed || KeepCluster {
		cleanupLog("Collecting logs from the kind cluster")
		cmd := testsinternal.GoToolCmd("kind", "export", "logs", "--name", clusterName, kindLogDir)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	}
	if !testsFailed && !KeepCluster {
		cleanupLog("Destroying the kind cluster")
		cmd := testsinternal.GoToolCmd("kind", "delete", "cluster", "--name", clusterName)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	}
}

func inferenceExtensionVersion() string {
	goMod := path.Join(internaltesting.FindProjectRoot(), "go.mod")
	data, err := os.ReadFile(goMod)
	if err != nil {
		panic(fmt.Sprintf("failed to read go.mod: %v", err))
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 && parts[0] == "sigs.k8s.io/gateway-api-inference-extension" {
			return strings.TrimSpace(parts[1])
		}
	}
	panic("failed to find extension version in go.mod")
}

func installInferencePoolEnvironment(ctx context.Context) (err error) {
	infExtVersion := inferenceExtensionVersion()
	if err = KubectlApplyManifest(ctx,
		fmt.Sprintf("https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/download/%s/manifests.yaml", infExtVersion),
	); err != nil {
		return fmt.Errorf("failed to install inference extension CRDs: %w", err)
	}
	return nil
}

// EnvoyGatewayVersion is the Envoy Gateway version the e2e suite installs (EG_VERSION, else latest).
func EnvoyGatewayVersion() string {
	return cmp.Or(os.Getenv("EG_VERSION"), EnvoyGatewayLatestVersion)
}

// EnvoyGatewaySupportsLimitFromMetadata reports whether the installed Envoy Gateway has
// BackendTrafficPolicy limit.fromMetadata (envoyproxy/gateway#9216), which only landed on main.
//
// TODO: this only recognizes the "latest" main build. Relax it to a version comparison once a tagged
// EG release includes envoyproxy/gateway#9216, otherwise it will keep skipping on that release.
func EnvoyGatewaySupportsLimitFromMetadata() bool {
	return EnvoyGatewayVersion() == EnvoyGatewayLatestVersion
}

// initEnvoyGateway initializes the Envoy Gateway in the kind cluster following the quickstart guide:
// https://gateway.envoyproxy.io/latest/tasks/quickstart/
func initEnvoyGateway(ctx context.Context, namespace string, inferenceExtension bool) (err error) {
	egVersion := EnvoyGatewayVersion()
	initLog("Installing Envoy Gateway")
	start := time.Now()
	defer func() {
		elapsed := time.Since(start)
		initLog(fmt.Sprintf("\tdone (took %.2fs in total)", elapsed.Seconds()))
	}()
	initLog("\tHelm Install")
	// Build helm command with base values + addons based on what features are needed
	helmArgs := []string{
		"upgrade", "-i", "eg",
		"oci://docker.io/envoyproxy/gateway-helm", "--version", egVersion,
		"-n", "envoy-gateway-system", "--create-namespace",
		"-f", "../../manifests/envoy-gateway-values.yaml",
		"-f", "../../examples/token_ratelimit/envoy-gateway-values-addon.yaml",
		"--set", fmt.Sprintf("config.envoyGateway.extensionManager.service.fqdn.hostname=ai-gateway-controller.%s.svc.cluster.local", namespace),
	}
	if inferenceExtension {
		helmArgs = append(helmArgs, "-f", "../../examples/inference-pool/envoy-gateway-values-addon.yaml")
	}
	helm := testsinternal.GoToolCmdContext(ctx, "helm", helmArgs...)
	helm.Stdout = os.Stdout
	helm.Stderr = os.Stderr
	if err = helm.Run(); err != nil {
		return
	}

	// EG v1.8.1+ gates readiness on cache sync (cacheReadyCheck), which requires all
	// informer caches—including those for backendResources—to complete an initial List.
	// The EG Helm chart RBAC doesn't cover extension-managed CRDs like InferencePool,
	// so the informer gets 403s and the pod never becomes ready. Grant access explicitly.
	if inferenceExtension {
		initLog("\tGranting Envoy Gateway RBAC for InferencePool resources")
		if err = KubectlApplyManifestStdin(ctx, `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: eg-inference-pool-access
rules:
- apiGroups:
  - inference.networking.k8s.io
  resources:
  - inferencepools
  verbs:
  - get
  - list
  - watch
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: eg-inference-pool-access
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: eg-inference-pool-access
subjects:
- kind: ServiceAccount
  name: envoy-gateway
  namespace: envoy-gateway-system
`); err != nil {
			return fmt.Errorf("failed to apply InferencePool RBAC: %w", err)
		}
	}

	initLog("\tWaiting for Envoy Gateway deployment to be ready")
	return kubectlWaitForDeploymentReady(ctx, "envoy-gateway-system", "envoy-gateway")
}

// InstallOrUpgradeAIGateway installs or upgrades the AI Gateway using Helm.
func InstallOrUpgradeAIGateway(ctx context.Context, aigw AIGatewayHelmOption) (err error) {
	initLog("Installing AI Gateway")
	start := time.Now()
	defer func() {
		elapsed := time.Since(start)
		initLog(fmt.Sprintf("\tdone (took %.2fs in total)\n", elapsed.Seconds()))
	}()
	initLog("\tHelm Install")
	cdrChartArgs := []string{"upgrade", "-i", "ai-eg-crd"}
	if aigw.ChartVersion != "" {
		cdrChartArgs = append(cdrChartArgs, "oci://docker.io/envoyproxy/ai-gateway-crds-helm", "--version", aigw.ChartVersion)
	} else {
		cdrChartArgs = append(cdrChartArgs, "../../manifests/charts/ai-gateway-crds-helm")
	}
	cdrChartArgs = append(cdrChartArgs, "-n", aigw.GetNamespace(), "--create-namespace")
	crdChart := testsinternal.GoToolCmdContext(ctx, "helm", cdrChartArgs...)
	crdChart.Stdout = os.Stdout
	crdChart.Stderr = os.Stderr
	if err = crdChart.Run(); err != nil {
		return
	}

	mainChartArgs := []string{"upgrade", "-i", "ai-eg"}
	if aigw.ChartVersion != "" {
		mainChartArgs = append(mainChartArgs, "oci://docker.io/envoyproxy/ai-gateway-helm", "--version", aigw.ChartVersion)
	} else {
		mainChartArgs = append(mainChartArgs, "../../manifests/charts/ai-gateway-helm")
	}
	mainChartArgs = append(mainChartArgs, "-n", aigw.GetNamespace(), "--create-namespace")
	mainChartArgs = append(mainChartArgs, aigw.AdditionalArgs...)

	helm := testsinternal.GoToolCmdContext(ctx, "helm", mainChartArgs...)
	helm.Stdout = os.Stdout
	helm.Stderr = os.Stderr
	if err = helm.Run(); err != nil {
		return
	}
	// Restart the controller to pick up the new changes in the AI Gateway.
	initLog("\tRestart AI Gateway controller")
	if err = KubectlRestartDeployment(ctx, aigw.GetNamespace(), "ai-gateway-controller"); err != nil {
		return
	}
	return kubectlWaitForDeploymentReady(ctx, aigw.GetNamespace(), "ai-gateway-controller")
}

func initPrometheus(ctx context.Context) (err error) {
	initLog("Installing Prometheus")
	start := time.Now()
	defer func() {
		elapsed := time.Since(start)
		initLog(fmt.Sprintf("\tdone (took %.2fs in total)\n", elapsed.Seconds()))
	}()
	initLog("\tapplying manifests")
	if err = KubectlApplyManifest(ctx, "../../examples/monitoring/monitoring.yaml"); err != nil {
		return
	}
	initLog("\twaiting for deployment")
	return kubectlWaitForDeploymentReady(ctx, "monitoring", "prometheus")
}

// Kubectl runs the kubectl command with the given context and arguments.
func Kubectl(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func KubectlApplyManifest(ctx context.Context, manifest string) (err error) {
	cmd := Kubectl(ctx, "apply", "--server-side", "-f", manifest, "--force-conflicts")
	return cmd.Run()
}

// KubectlApplyManifestStdin applies the given manifest using kubectl, reading from stdin.
func KubectlApplyManifestStdin(ctx context.Context, manifest string) (err error) {
	cmd := Kubectl(ctx, "apply", "--server-side", "-f", "-")
	cmd.Stdin = bytes.NewReader([]byte(manifest))
	return cmd.Run()
}

func KubectlDeleteManifestStdin(ctx context.Context, manifest string) (err error) {
	cmd := Kubectl(ctx, "delete", "-f", "-", "--force=true")
	cmd.Stdin = bytes.NewReader([]byte(manifest))
	return cmd.Run()
}

// KubectlDeleteManifest deletes the given manifest using kubectl.
func KubectlDeleteManifest(ctx context.Context, manifest string) (err error) {
	cmd := Kubectl(ctx, "delete", "-f", manifest, "--force=true")
	return cmd.Run()
}

func KubectlRestartDeployment(ctx context.Context, namespace, deployment string) error {
	cmd := Kubectl(ctx, "rollout", "restart", "deployment/"+deployment, "-n", namespace)
	return cmd.Run()
}

func kubectlWaitForDeploymentReady(ctx context.Context, namespace, deployment string) (err error) {
	cmd := Kubectl(ctx, "wait", "--timeout=2m", "-n", namespace,
		"deployment/"+deployment, "--for=create")
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("error waiting for deployment %s in namespace %s: %w", deployment, namespace, err)
	}

	cmd = Kubectl(ctx, "wait", "--timeout=2m", "-n", namespace,
		"deployment/"+deployment, "--for=condition=Available")
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("error waiting for deployment %s in namespace %s: %w", deployment, namespace, err)
	}
	return
}

func kubectlWaitForDaemonSetReady(ctx context.Context, namespace, daemonset string) (err error) {
	// Wait for daemonset to be created.
	cmd := Kubectl(ctx, "wait", "--timeout=2m", "-n", namespace,
		"daemonset/"+daemonset, "--for=create")
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("error waiting for daemonset %s in namespace %s: %w", daemonset, namespace, err)
	}

	// Wait for daemonset pods to be ready using jsonpath.
	cmd = Kubectl(ctx, "wait", "--timeout=2m", "-n", namespace,
		"daemonset/"+daemonset, "--for=jsonpath={.status.numberReady}=1")
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("error waiting for daemonset %s pods to be ready in namespace %s: %w", daemonset, namespace, err)
	}
	return
}

// RequireWaitForGatewayPodReady waits for the Envoy Gateway pod with the given selector to be ready.
func RequireWaitForGatewayPodReady(t *testing.T, selector string) {
	RequireWaitForGatewayPodReadyWithNamespace(t, EnvoyGatewayNamespace, selector)
}

func RequireWaitForGatewayPodReadyWithNamespace(t *testing.T, namespace, selector string) {
	requireWaitForGatewayPod(t, namespace, selector)
	RequireWaitForPodReady(t, namespace, selector)
}

// RequireGatewayListenerAddressViaMetalLB gets the external IP address of the Gateway via MetalLB.
func RequireGatewayListenerAddressViaMetalLB(t *testing.T, namespace, name string) (addr string) {
	cmd := Kubectl(t.Context(), "get", "gateway", "-n", namespace, name,
		"-o", "jsonpath={.status.addresses[0].value}")
	cmd.Stdout = nil
	cmd.Stderr = nil
	out, err := cmd.Output()
	require.NoError(t, err, "failed to get gateway address")
	addr = strings.TrimSpace(string(out))
	return
}

// requireWaitForGatewayPod waits for the Envoy Gateway pod containing the
// extproc container.
func requireWaitForGatewayPod(t *testing.T, namespace, selector string) {
	waitUntilKubectl(t, 2*time.Minute, 1*time.Second, func(output string) error {
		if !strings.Contains(output, "ai-gateway-extproc") {
			return fmt.Errorf("container not found, output: %s", output)
		}
		return nil
	}, "get", "pod", "-n", namespace,
		"--selector="+selector, "-o", "jsonpath='{.items[0].spec.initContainers[*].name} {.items[0].spec.containers[*].name}'")
}

// RequireWaitForPodReady waits for the pod with the given selector to be ready.
func RequireWaitForPodReady(t *testing.T, namespace, selector string) {
	waitUntilKubectl(t, 3*time.Minute, 5*time.Second, func(_ string) error {
		return nil // Success if the command exited 0, ignore output.
	}, "wait", "--timeout=2s", "-n", namespace,
		"pods", "--for=condition=Ready", "-l", selector)
}

// RequireNewHTTPPortForwarder creates a new port forwarder for the given namespace and selector.
// Returns a PortForwarder that automatically handles connection recovery during pod rolling updates.
func RequireNewHTTPPortForwarder(t *testing.T, namespace string, selector string, servicePort int) PortForwarder {
	f, err := newServicePortForwarder(t.Context(), newKubectlPortForward, namespace, selector, 0, servicePort)
	require.NoError(t, err)
	return f
}

// PortForwarder provides HTTP access to services in a Kubernetes cluster via kubectl port-forward.
// Thread-safe for concurrent use. Automatically recovers from connection failures during pod rolling updates.
type PortForwarder interface {
	// Post sends an HTTP POST request and returns the response body.
	// Automatically retries on stale connections during serviceURL rollouts.
	Post(ctx context.Context, path, body string) ([]byte, error)
	// Kill terminates the port-forward process.
	Kill()
	// Address returns the local address (e.g., "http://127.0.0.1:12345").
	Address() string
}

// newServicePortForwarder creates a new local port forwarder for the namespace and selector.
func newServicePortForwarder(ctx context.Context, newPortForward newPortForwardFn, namespace, selector string, localPort, servicePort int) (PortForwarder, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", fmt.Sprintf("localhost:%d", localPort))
	if err != nil {
		return nil, fmt.Errorf("failed to get a local available port for Pod %q: %w", selector, err)
	}
	err = l.Close()
	if err != nil {
		return nil, err
	}

	localPort = l.Addr().(*net.TCPAddr).Port

	f := &portForwarder{
		localURL: &url.URL{
			Scheme: "http",
			Host:   fmt.Sprintf("127.0.0.1:%d", localPort),
		},
		portForward: newPortForward(namespace, selector, localPort, servicePort),
	}

	if err = f.portForward.start(ctx); err != nil {
		return nil, err
	} else if err = f.waitReady(ctx); err != nil {
		f.portForward.kill()
		return nil, err
	}
	return f, nil
}

// portForward starts a port forwarding process.
type portForward interface {
	start(ctx context.Context) error
	kill()
}

// kubectlPortForward implements portForward using kubectl.
type kubectlPortForward struct {
	namespace   string
	selector    string
	localPort   int
	servicePort int
	cmd         *exec.Cmd
	cmdMu       sync.Mutex
}

type newPortForwardFn func(namespace, selector string, localPort, servicePort int) portForward

func newKubectlPortForward(namespace, selector string, localPort, servicePort int) portForward {
	return &kubectlPortForward{
		namespace:   namespace,
		selector:    selector,
		localPort:   localPort,
		servicePort: servicePort,
	}
}

type serviceList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	} `json:"items"`
}

func (k *kubectlPortForward) start(ctx context.Context) error {
	cmd := Kubectl(ctx, "get", "svc", "-n", k.namespace,
		"--selector="+k.selector, "-o", "json")
	cmd.Stdout = nil
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get service name: %w", err)
	}

	var svcs serviceList
	if err := json.Unmarshal(out, &svcs); err != nil {
		return fmt.Errorf("failed to parse service list: %w", err)
	}
	if len(svcs.Items) == 0 {
		return fmt.Errorf("no service found for selector %q", k.selector)
	}
	serviceName := svcs.Items[0].Metadata.Name

	cmd = Kubectl(ctx, "port-forward",
		"-n", k.namespace, "svc/"+serviceName,
		fmt.Sprintf("%d:%d", k.localPort, k.servicePort),
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start port-forward: %w", err)
	}
	k.cmdMu.Lock()
	k.cmd = cmd
	k.cmdMu.Unlock()
	return nil
}

func (k *kubectlPortForward) kill() {
	k.cmdMu.Lock()
	defer k.cmdMu.Unlock()
	if k.cmd != nil && k.cmd.Process != nil {
		_ = k.cmd.Process.Kill()
	}
}

// portForwarder implements PortForwarder.
type portForwarder struct {
	localURL      *url.URL
	portForward   portForward
	restartFlight singleflight.Group
}

// poll repeatedly checks a condition until it succeeds or the context is done.
func poll(ctx context.Context, interval time.Duration, check func() error) error {
	for {
		if err := check(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// restart recreates the port-forward connection on the same local port.
func (f *portForwarder) restart(ctx context.Context) error {
	f.Kill()

	// Wait for port to actually be released by the dying process.
	err := poll(ctx, 50*time.Millisecond, func() error {
		dialer := net.Dialer{Timeout: 100 * time.Millisecond}
		conn, err := dialer.DialContext(ctx, "tcp", f.localURL.Host)
		if err != nil {
			// Connection refused = port is free.
			return nil
		}
		_ = conn.Close()
		return errors.New("port still in use")
	})
	if err != nil {
		return err
	}

	if err = f.portForward.start(ctx); err != nil {
		return err
	}
	return f.waitReady(ctx)
}

// waitReady polls until port-forward is ready to accept HTTP traffic.
func (f *portForwarder) waitReady(ctx context.Context) error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	req, err := http.NewRequestWithContext(ctx, "GET", f.localURL.String()+"/", nil)
	if err != nil {
		return err
	}

	return poll(ctx, 100*time.Millisecond, func() error {
		// Try actual HTTP request to verify port-forward is fully functional.
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		// Any response (even 404) means port-forward is working.
		return nil
	})
}

// handleStaleConnection coordinates restart across concurrent goroutines using singleflight.
// Returns (restarted=true, err) if this goroutine performed the restart.
// Returns (restarted=false, err=nil) if another goroutine restarted and this one waited.
func (f *portForwarder) handleStaleConnection(ctx context.Context) (bool, error) {
	_, err, shared := f.restartFlight.Do("restart", func() (interface{}, error) {
		return nil, f.restart(ctx)
	})
	// shared=false means we performed the restart, shared=true means we waited
	return !shared, err
}

// Post sends an HTTP POST request with automatic retry on stale connections.
func (f *portForwarder) Post(ctx context.Context, path, body string) ([]byte, error) {
	addr := f.localURL.String() + path

	const maxRestarts = 5
	restarts := 0
	for {
		if restarts > 0 {
			time.Sleep(time.Duration(restarts*100) * time.Millisecond)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, addr, strings.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		var resp *http.Response
		var respBody []byte
		var doErr error
		resp, doErr = http.DefaultClient.Do(req)
		isStale := isStaleConnectionError(doErr)
		if doErr == nil {
			respBody, err = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				return nil, fmt.Errorf("failed to read response body: %w", err)
			}
			// Empty 500 indicates broken upstream (stale forward during rollout).
			if resp.StatusCode == http.StatusInternalServerError && len(respBody) == 0 {
				isStale = true
			}
		}

		if isStale {
			restarted, restartErr := f.handleStaleConnection(ctx)
			if restartErr != nil {
				return nil, fmt.Errorf("failed to restart port-forward: %w", restartErr)
			}
			if restarted {
				restarts++
			}
			if restarts >= maxRestarts {
				if doErr != nil {
					return nil, doErr
				}
				return nil, fmt.Errorf("request failed with status %s: %s", resp.Status, string(respBody))
			}
			continue
		}

		if doErr != nil {
			return nil, doErr
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("request failed with status %s: %s", resp.Status, string(respBody))
		}
		return respBody, nil
	}
}

// Kill stops the port forwarder.
func (f *portForwarder) Kill() {
	f.portForward.kill()
}

// Address returns the address of the port forwarder.
func (f *portForwarder) Address() string {
	return f.localURL.String()
}

// isStaleConnectionError checks if an error indicates a stale port-forward connection.
func isStaleConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "connection") ||
		strings.Contains(errStr, "closed") ||
		strings.Contains(errStr, "reset") ||
		strings.Contains(errStr, "refused")
}

// waitUntilKubectl polls by running a kubectl command with the given args
// until the verifyOut predicate returns nil or the timeout is reached.
//
// Unlike require.Eventually, this retains the output of the last mismatch.
func waitUntilKubectl(t *testing.T, timeout time.Duration, pollInterval time.Duration, verifyOut func(output string) error, args ...string) {
	var lastErr error
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := Kubectl(t.Context(), args...)
		cmd.Stdout = nil // To ensure that we can capture the output by Output().
		out, err := cmd.Output()
		if err != nil {
			lastErr = err
			time.Sleep(pollInterval)
			continue
		}
		err = verifyOut(string(out))
		if err == nil {
			return
		}
		lastErr = err
		time.Sleep(pollInterval)
	}
	require.Fail(t, "timed out waiting", "last error: %v", lastErr)
}
