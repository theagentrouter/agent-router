---
id: basic-usage
title: Basic Usage
sidebar_position: 4
---

import Tabs from '@theme/Tabs';
import TabItem from '@theme/TabItem';
import CodeBlock from '@theme/CodeBlock';
import vars from '../\_vars.json';

This guide will help you set up a basic AI Gateway configuration and make your first request.

For Windows users, note that you are able to use Windows Subsystem for Linux (WSL) to run the commands below if they do not work on the Windows command prompt.

## Setting Up Your Environment

### Deploy Basic Configuration

Let's start by deploying a basic AI Gateway setup that includes a test backend:

<CodeBlock language="shell">
{`kubectl apply -f https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/basic.yaml`}
</CodeBlock>

Wait for the Gateway pod to be ready:

```shell
kubectl wait pods --timeout=2m \
  -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
  -n envoy-gateway-system \
  --for=condition=Ready
```

:::tip

In the `basic.yaml` file, you will find "ClientTrafficPolicy" resource that configures the buffer limit on the client to the gateway connection.
By default, Envoy Gateway configures 32KB as the buffer limit, which is not enough for most AI model responses, hence it's configured to 50MB in the example.
Envoy Gateway provides more advanced policies, not limited to the buffer limit. Please refer to the [Client Traffic Policies](https://gateway.envoyproxy.io/latest/tasks/traffic/client-traffic-policy/) documentation for more details.

**HTTP/2 window size settings:** If you are sending requests over HTTP/2, Envoy imposes additional
flow-control limits via `initialStreamWindowSize` and `initialConnectionWindowSize`.
These act as a secondary cap on request body size, independent of `bufferLimit`, and can cause
`413 request_payload_too_large` errors even after the buffer limit is raised.
To avoid this, set both values to the Envoy defaults (`16Mi` and `24Mi`) in the `ClientTrafficPolicy`:

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: ClientTrafficPolicy
metadata:
  name: client-buffer-limit
  namespace: default
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: envoy-ai-gateway-basic
  connection:
    bufferLimit: 50Mi
  http2:
    initialStreamWindowSize: 16Mi
    initialConnectionWindowSize: 24Mi
```

See the Envoy API reference for
[`initial_stream_window_size`](https://www.envoyproxy.io/docs/envoy/latest/api-v3/config/core/v3/protocol.proto#envoy-v3-api-field-config-core-v3-http2protocoloptions-initial-stream-window-size)
and
[`initial_connection_window_size`](https://www.envoyproxy.io/docs/envoy/latest/api-v3/config/core/v3/protocol.proto#envoy-v3-api-field-config-core-v3-http2protocoloptions-initial-connection-window-size)
for more details.

:::

## Configure `$GATEWAY_URL`

First, check if your Gateway has an external IP address assigned:

```shell
kubectl get svc -n envoy-gateway-system \
  --selector=gateway.envoyproxy.io/owning-gateway-namespace=default,gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic
```

You'll see output similar to this:

```
NAME                    TYPE           CLUSTER-IP      EXTERNAL-IP      PORT(S)
eg-envoy-ai-gateway    LoadBalancer   10.96.61.234    <pending/IP>     80:31234/TCP
```

Choose one of these options based on the EXTERNAL-IP status:

<Tabs>
<TabItem value="external-ip" label="Using External IP">

If the EXTERNAL-IP shows an actual IP address (not `<pending>`), you can access the gateway directly:

First, save the external IP and set the gateway URL:

```shell
export GATEWAY_URL=$(kubectl get gateway/envoy-ai-gateway-basic -o jsonpath='{.status.addresses[0].value}')
```

Verify the URL is available:

```shell
echo $GATEWAY_URL
```

</TabItem>
<TabItem value="port-forward" label="Using Port Forwarding">

If the EXTERNAL-IP shows `<pending>` or your cluster doesn't support LoadBalancer services, use port forwarding.

First, set the gateway URL:

```shell
export GATEWAY_URL="http://localhost:8080"
```

Then set up port forwarding (this will block the terminal):

```shell
export ENVOY_SERVICE=$(kubectl get svc -n envoy-gateway-system \
  --selector=gateway.envoyproxy.io/owning-gateway-namespace=default,gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
  -o jsonpath='{.items[0].metadata.name}')

kubectl port-forward -n envoy-gateway-system svc/$ENVOY_SERVICE 8080:80
```

</TabItem>
</Tabs>

## Testing the Gateway

### Making Test Requests

Open a new terminal and send test requests to the AI Gateway using the `GATEWAY_URL` we set up:

#### Test Chat Completions

```shell
curl -H "Content-Type: application/json" \
  -d '{
        "model": "some-cool-self-hosted-model",
        "messages": [
            {
                "role": "system",
                "content": "Hi."
            }
        ]
    }' \
  $GATEWAY_URL/v1/chat/completions
```

#### Test Completions (Legacy)

```shell
curl -H "Content-Type: application/json" \
  -d '{
        "model": "some-cool-self-hosted-model",
        "prompt": "def fib(n):\n    if n <= 1:\n        return n\n    else:\n        return fib(n-1) + fib(n-2)",
        "max_tokens": 25,
        "temperature": 0.4,
        "top_p": 0.9
    }' \
  $GATEWAY_URL/v1/completions
```

#### Test Embeddings

```shell
curl -H "Content-Type: application/json" \
  -d '{
        "model": "some-cool-self-hosted-model",
        "input": "Hello, world!"
    }' \
  $GATEWAY_URL/v1/embeddings
```

#### Test Rerank

```shell
curl -H "Content-Type: application/json" \
  -d '{
        "model": "some-cool-self-hosted-model",
        "query": "What is the capital of France?",
        "documents": [
            "Paris is the capital of France.",
            "Berlin is the capital of Germany."
        ]
    }' \
  $GATEWAY_URL/cohere/v2/rerank
```

:::tip

If you're opening a new terminal, you'll need to set the `GATEWAY_URL` variable again.

:::

### Expected Responses

#### Chat Completions Response

You should receive a response like:

```json
{
  "choices": [
    {
      "message": {
        "content": "I'll be back."
      }
    }
  ]
}
```

#### Completions Response

For legacy completions requests, you should receive a response like:

```json
{
  "choices": [
    {
      "text": " + fib(n-3) + fib(n-4)\n\ndef fib(n):\n    if n <= 1:\n        return",
      "index": 0,
      "finish_reason": "length"
    }
  ]
}
```

#### Embeddings Response

For embeddings requests, you should receive a response like:

```json
{
  "object": "list",
  "data": [
    {
      "object": "embedding",
      "embedding": [0.1, 0.2, 0.3, 0.4, 0.5],
      "index": 0
    }
  ],
  "model": "some-cool-self-hosted-model",
  "usage": {
    "prompt_tokens": 3,
    "total_tokens": 3
  }
}
```

#### Rerank Response

You should receive a response like:

```json
{
  "results": [
    { "index": 0, "relevance_score": 0.98 },
    { "index": 1, "relevance_score": 0.12 }
  ]
}
```

:::note

These responses come from a mock backend. The model `some-cool-self-hosted-model` is configured to return test responses.
For real AI model responses, see the [Connect Providers](./connect-providers) guide.

:::

### Understanding the Response Formats

The basic setup includes a mock backend that demonstrates the API structure but doesn't provide real AI responses. The response formats follow the standard OpenAI API formats:

**Chat Completions:**

- A `choices` array containing responses
- Each message having a `role` and `content`

**Embeddings:**

- A `data` array containing embedding objects
- Each embedding having a vector array and index
- Usage information showing token consumption

**Rerank:**

- A `results` array containing ranked results for the provided documents
- Each result includes the original document `index` and a `relevance_score`
- Higher `relevance_score` indicates higher relevance to the query

## Next Steps

Now that you've tested the basic setup, you can:

- Configure [real AI model backends](./connect-providers) like OpenAI or AWS Bedrock
- Explore the [API Reference](../api/) for more details about available endpoints
