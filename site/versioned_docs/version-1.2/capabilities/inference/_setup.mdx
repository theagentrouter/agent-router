import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

## Prerequisites

Before starting, ensure you have:

1. **Kubernetes cluster** with Gateway API support
2. **Envoy Gateway** installed and configured

## Step 1: Install Gateway API Inference Extension CRDs

Install the Gateway API Inference Extension CRDs and controller:

<CodeBlock language="shell">
{`kubectl apply -f https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/download/${vars.gaieVersion}/manifests.yaml`}
</CodeBlock>

## Step 2: Configure Envoy Gateway for InferencePool

Install (or upgrade) Envoy Gateway with the InferencePool addon values file, and wait for it to be ready:

<CodeBlock language="shell">
{`helm upgrade -i eg oci://docker.io/envoyproxy/gateway-helm \\
  --version v${vars.egVersion} \\
  --namespace envoy-gateway-system \\
  --create-namespace \\
  -f https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/manifests/envoy-gateway-values.yaml \\
  -f https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/inference-pool/envoy-gateway-values-addon.yaml

kubectl wait --timeout=2m -n envoy-gateway-system deployment/envoy-gateway --for=condition=Available`}
</CodeBlock>

See the [Envoy Gateway Installation Guide](../../getting-started/prerequisites.md#additional-features-rate-limiting-inferencepool-etc) if you're combining this with other addons (like rate limiting), or already have Envoy Gateway installed and just need to add this values file.

## Step 3: Deploy InferencePool Base Resources

Deploy the sample inference backends, InferencePools, and Endpoint Picker Providers (EPP) that the rest of this guide builds on:

<CodeBlock language="shell">
{`kubectl apply -f https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/inference-pool/base.yaml`}
</CodeBlock>

This creates:

- The `vllm-llama3-8b-instruct` `InferencePool` (a simulated vLLM deployment) with its Endpoint Picker Provider (EPP)
- The `mistral` `InferencePool` with its own Endpoint Picker Provider (EPP)
- A standard `envoy-ai-gateway-basic-testupstream` `AIServiceBackend`/`Backend` used later for non-InferencePool (fallback) routing
