---
id: prerequisites
title: Prerequisites
sidebar_position: 2
---

import Link from '@docusaurus/Link';
import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';
import CodeBlock from '@theme/CodeBlock';
import vars from '../\_vars.json';

Before you begin using Agent Router, you'll need to ensure you have the following prerequisites in place:

## Required Tools

Make sure you have the following tools installed:

- `kubectl` - The Kubernetes command-line tool
- `helm` - The package manager for Kubernetes
- `curl` - For testing API endpoints (installed by default on most systems)

:::tip Verify Installation
Run these commands to verify your tools are properly installed:

Verify kubectl installation:

```shell
kubectl version --client
```

Verify helm installation:

```shell
helm version
```

Verify curl installation:

```shell
curl --version
```

:::

## Kubernetes Cluster

:::info Version Requirements
Agent Router requires Kubernetes version {vars.k8sMinVersion} or higher. We recommend using a recent stable version of Kubernetes for the best experience.
:::

You need a running Kubernetes cluster with your kubeconfig properly configured. You have several options:

<Tabs>
<TabItem value="existing" label="Existing Cluster" default>

If you already have a Kubernetes cluster, ensure your kubeconfig is properly configured to access it.

Verify your cluster meets the version requirements by running:

```shell
kubectl version --output=json
```

The server version in the output should show version {vars.k8sMinVersion} or higher:

```json
{
  "serverVersion": {
    "major": "1",
    "minor": "32+",
    ...
  }
}
```

:::caution

If your cluster is running a version lower than {vars.k8sMinVersion}, our recommendation is to upgrade your cluster to meet the minimum requirements.
The recommended Kubernetes versions are those that are actively tested and supported by the Agent Router team.

:::

</TabItem>
<TabItem value="docker-desktop" label="Docker Desktop">

If you're using Docker Desktop, you can enable its built-in Kubernetes cluster:

1. Open Docker Desktop
2. Click on the gear icon (Settings)
3. Select "Kubernetes" from the left sidebar
4. Check "Enable Kubernetes"
5. Click "Apply & Restart"

Wait for Docker Desktop to restart and for the Kubernetes cluster to be ready. You can verify the setup by running:

```shell
kubectl config use-context docker-desktop
kubectl cluster-info
```

The output should show that the Kubernetes control plane is running.

:::tip

Docker Desktop's Kubernetes is a great choice for local development as it:

- Comes pre-installed with Docker Desktop
- Runs locally on your machine
- Integrates well with Docker Desktop's UI
- Requires minimal setup

:::

</TabItem>
<TabItem value="kind" label="Local Kind Cluster">

If you don't have a Kubernetes cluster, you can quickly create a local one using [kind](https://kind.sigs.k8s.io/).

First, install kind if you haven't already (on macOS with Homebrew):

```shell
brew install kind
```

Then create a cluster:

```shell
kind create cluster
```

</TabItem>
</Tabs>

## Envoy Gateway

:::warning Important

Ensure you're using a clean Envoy Gateway deployment. If you have an existing Envoy Gateway installation with custom configurations, it may conflict with AI Gateway's requirements. We recommend:

- Using a fresh Kubernetes cluster, or
- Uninstalling any existing Envoy Gateway deployments before proceeding:
  ```shell
  helm uninstall eg -n envoy-gateway-system
  kubectl delete namespace envoy-gateway-system
  ```

:::

:::info Version Requirements

Agent Router requires Envoy Gateway version {vars.egMinVersion} or higher. For the best experience while trying out AI Gateway, we recommend using the latest version as shown in the commands below.

:::

Agent Router is built on top of Envoy Gateway. Install it using Helm and wait for the deployment to be ready.

<CodeBlock language="shell">
{`helm upgrade -i eg oci://docker.io/envoyproxy/gateway-helm \\
    --version v${vars.egVersion} \\
    --namespace envoy-gateway-system \\
    --create-namespace \\
    -f https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/manifests/envoy-gateway-values.yaml

kubectl wait --timeout=2m -n envoy-gateway-system deployment/envoy-gateway --for=condition=Available`}
</CodeBlock>

> If you are experiencing network issues with `docker.io` , you can follow the [Install from Source Code](https://github.com/envoyproxy/gateway/tree/main/charts/gateway-helm#install-from-source-code) instructions.

Note that we have included the AI Gateway-specific helm values file via the `-f` flag. This file contains the necessary configuration for AI Gateway integration.

### Additional Features (Rate Limiting, InferencePool, etc.)

Depending on the additional features you want (like rate limiting or InferencePool), you need to pass additional addon values files to modify the Envoy Gateway installation.
Currently, supported addons are:

- [**Rate Limiting**](../capabilities/traffic/usage-based-ratelimiting.md):
  <Link href={`https://github.com/theagentrouter/agent-router/blob/${vars.aigwGitRef}/examples/token_ratelimit/envoy-gateway-values-addon.yaml`}>
  {`https://github.com/theagentrouter/agent-router/blob/${vars.aigwGitRef}/examples/token_ratelimit/envoy-gateway-values-addon.yaml`}
  </Link>
- [**InferencePool**](../capabilities/inference/index.md):
  <Link href={`https://github.com/theagentrouter/agent-router/blob/${vars.aigwGitRef}/examples/inference-pool/envoy-gateway-values-addon.yaml`}>
  {`https://github.com/theagentrouter/agent-router/blob/${vars.aigwGitRef}/examples/inference-pool/envoy-gateway-values-addon.yaml`}
  </Link>

For example, to install with all addons enabled, run:

<CodeBlock language="shell">
{`helm upgrade -i eg oci://docker.io/envoyproxy/gateway-helm \\
    --version v${vars.egVersion} \\
    --namespace envoy-gateway-system \\
    --create-namespace \\
    -f https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/manifests/envoy-gateway-values.yaml \\
    -f https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/token_ratelimit/envoy-gateway-values-addon.yaml \\
    -f https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/inference-pool/envoy-gateway-values-addon.yaml`}
</CodeBlock>
