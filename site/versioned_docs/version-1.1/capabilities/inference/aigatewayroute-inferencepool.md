---
id: aigatewayroute-inferencepool
title: AIGatewayRoute + InferencePool Guide
sidebar_position: 3
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

# AIGatewayRoute + InferencePool Guide

This guide demonstrates how to use InferencePool with AIGatewayRoute for advanced AI-specific inference routing. This approach provides enhanced features like model-based routing, token rate limiting, and advanced observability.

## Prerequisites

Before starting, ensure you have:

1. **Kubernetes cluster** with Gateway API support
2. **Agent Router** installed and configured

## Step 1: Install Gateway API Inference Extension

Install the Gateway API Inference Extension CRDs and controller:

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/download/v1.0.1/manifests.yaml
```

After installing InferencePool CRD, enable InferencePool support in Envoy Gateway, restart the deployment, and wait for it to be ready:

<CodeBlock language="shell">
{`kubectl apply -f https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/inference-pool/config.yaml

kubectl rollout restart -n envoy-gateway-system deployment/envoy-gateway

kubectl wait --timeout=2m -n envoy-gateway-system deployment/envoy-gateway --for=condition=Available`}
</CodeBlock>

## Step 2: Ensure Envoy Gateway is configured for InferencePool

See [Envoy Gateway Installation Guide](../../getting-started/prerequisites.md#additional-features-rate-limiting-inferencepool-etc)

## Step 3: Deploy Inference Backends

Deploy sample inference backends and related resources:

```bash
# Deploy vLLM simulation backend
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api-inference-extension/raw/v1.0.1/config/manifests/vllm/sim-deployment.yaml

# Deploy InferenceObjective
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/gateway-api-inference-extension/refs/tags/v1.0.1/config/manifests/inferenceobjective.yaml

# Deploy InferencePool resources
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api-inference-extension/raw/v1.0.1/config/manifests/inferencepool-resources.yaml
```

> **Note**: These deployments create the `vllm-llama3-8b-instruct` InferencePool and related resources that are referenced in the AIGatewayRoute configuration below.

## Step 4: Create Custom InferencePool Resources

Create additional inference backends with custom EndpointPicker configuration:

```yaml
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Service
metadata:
  name: mistral-upstream
  namespace: default
spec:
  selector:
    app: mistral-upstream
  ports:
    - protocol: TCP
      port: 8080
      targetPort: 8080
  # The headless service allows the IP addresses of the pods to be resolved via the Service DNS.
  clusterIP: None
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mistral-upstream
  namespace: default
spec:
  replicas: 3
  selector:
    matchLabels:
      app: mistral-upstream
  template:
    metadata:
      labels:
        app: mistral-upstream
    spec:
      containers:
        - name: testupstream
          image: docker.io/envoyproxy/ai-gateway-testupstream:latest
          imagePullPolicy: IfNotPresent
          ports:
            - containerPort: 8080
          env:
            - name: TESTUPSTREAM_ID
              value: test
          readinessProbe:
            httpGet:
              path: /health
              port: 8080
            initialDelaySeconds: 1
            periodSeconds: 1
---
apiVersion: inference.networking.k8s.io/v1
kind: InferencePool
metadata:
  name: mistral
  namespace: default
spec:
  targetPorts:
    - number: 8080
  selector:
    matchLabels:
      app: mistral-upstream
  endpointPickerRef:
    name: mistral-epp
    port:
      number: 9002
---
apiVersion: inference.networking.x-k8s.io/v1alpha2
kind: InferenceObjective
metadata:
  name: mistral
  namespace: default
spec:
  priority: 10
  poolRef:
    # Bind the InferenceObjective to the InferencePool.
    name: mistral
---
apiVersion: v1
kind: Service
metadata:
  name: mistral-epp
  namespace: default
spec:
  selector:
    app: mistral-epp
  ports:
    - protocol: TCP
      port: 9002
      targetPort: 9002
      appProtocol: http2
  type: ClusterIP
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: mistral-epp
  namespace: default
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mistral-epp
  namespace: default
  labels:
    app: mistral-epp
spec:
  replicas: 1
  selector:
    matchLabels:
      app: mistral-epp
  template:
    metadata:
      labels:
        app: mistral-epp
    spec:
      serviceAccountName: mistral-epp
      # Conservatively, this timeout should mirror the longest grace period of the pods within the pool
      terminationGracePeriodSeconds: 130
      containers:
        - name: epp
          image: registry.k8s.io/gateway-api-inference-extension/epp:v1.0.1
          imagePullPolicy: IfNotPresent
          args:
            - --pool-name
            - "mistral"
            - "--pool-namespace"
            - "default"
            - --v
            - "4"
            - --zap-encoder
            - "json"
            - --grpc-port
            - "9002"
            - --grpc-health-port
            - "9003"
            - "--config-file"
            - "/config/default-plugins.yaml"
          ports:
            - containerPort: 9002
            - containerPort: 9003
            - name: metrics
              containerPort: 9090
          livenessProbe:
            grpc:
              port: 9003
              service: inference-extension
            initialDelaySeconds: 5
            periodSeconds: 10
          readinessProbe:
            grpc:
              port: 9003
              service: inference-extension
            initialDelaySeconds: 5
            periodSeconds: 10
          volumeMounts:
            - name: plugins-config-volume
              mountPath: "/config"
      volumes:
        - name: plugins-config-volume
          configMap:
            name: plugins-config
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: plugins-config
  namespace: default
data:
  default-plugins.yaml: |
    apiVersion: inference.networking.x-k8s.io/v1alpha1
    kind: EndpointPickerConfig
    plugins:
    - type: queue-scorer
    - type: kv-cache-utilization-scorer
    - type: prefix-cache-scorer
    schedulingProfiles:
    - name: default
      plugins:
      - pluginRef: queue-scorer
      - pluginRef: kv-cache-utilization-scorer
      - pluginRef: prefix-cache-scorer
---
kind: Role
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: pod-read
  namespace: default
rules:
  - apiGroups: ["inference.networking.x-k8s.io"]
    resources: ["inferenceobjectives", "inferencepools"]
    verbs: ["get", "watch", "list"]
  - apiGroups: ["inference.networking.k8s.io"]
    resources: ["inferencepools"]
    verbs: ["get", "watch", "list"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "watch", "list"]
---
kind: RoleBinding
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: pod-read-binding
  namespace: default
subjects:
  - kind: ServiceAccount
    name: mistral-epp
    namespace: default
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: pod-read
---
kind: ClusterRole
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: auth-reviewer
rules:
  - apiGroups:
      - authentication.k8s.io
    resources:
      - tokenreviews
    verbs:
      - create
  - apiGroups:
      - authorization.k8s.io
    resources:
      - subjectaccessreviews
    verbs:
      - create
---
kind: ClusterRoleBinding
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: auth-reviewer-binding
subjects:
  - kind: ServiceAccount
    name: mistral-epp
    namespace: default
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: auth-reviewer
EOF
```

## Step 5: Create AIServiceBackend for Mixed Routing

Create an AIServiceBackend for traditional backend routing alongside InferencePool:

```yaml
cat <<EOF | kubectl apply -f -
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIServiceBackend
metadata:
  name: envoy-ai-gateway-basic-testupstream
  namespace: default
spec:
  schema:
    name: OpenAI
  backendRef:
    name: envoy-ai-gateway-basic-testupstream
    kind: Backend
    group: gateway.envoyproxy.io
---
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: Backend
metadata:
  name: envoy-ai-gateway-basic-testupstream
  namespace: default
spec:
  endpoints:
    - fqdn:
        hostname: envoy-ai-gateway-basic-testupstream.default.svc.cluster.local
        port: 80
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: envoy-ai-gateway-basic-testupstream
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: envoy-ai-gateway-basic-testupstream
  template:
    metadata:
      labels:
        app: envoy-ai-gateway-basic-testupstream
    spec:
      containers:
        - name: testupstream
          image: docker.io/envoyproxy/ai-gateway-testupstream:latest
          imagePullPolicy: IfNotPresent
          ports:
            - containerPort: 8080
          env:
            - name: TESTUPSTREAM_ID
              value: test
          readinessProbe:
            httpGet:
              path: /health
              port: 8080
            initialDelaySeconds: 1
            periodSeconds: 1
---
apiVersion: v1
kind: Service
metadata:
  name: envoy-ai-gateway-basic-testupstream
  namespace: default
spec:
  selector:
    app: envoy-ai-gateway-basic-testupstream
  ports:
    - protocol: TCP
      port: 80
      targetPort: 8080
  type: ClusterIP
EOF
```

## Step 6: Configure Gateway and AIGatewayRoute

Create a Gateway and AIGatewayRoute with multiple InferencePool backends:

```yaml
cat <<EOF | kubectl apply -f -
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: inference-pool-with-aigwroute
spec:
  controllerName: gateway.envoyproxy.io/gatewayclass-controller
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: inference-pool-with-aigwroute
  namespace: default
spec:
  gatewayClassName: inference-pool-with-aigwroute
  listeners:
    - name: http
      protocol: HTTP
      port: 80
---
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: inference-pool-with-aigwroute
  namespace: default
spec:
  parentRefs:
    - name: inference-pool-with-aigwroute
      kind: Gateway
      group: gateway.networking.k8s.io
  rules:
    # Route for vLLM Llama model via InferencePool
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: meta-llama/Llama-3.1-8B-Instruct
      backendRefs:
        - group: inference.networking.k8s.io
          kind: InferencePool
          name: vllm-llama3-8b-instruct
    # Route for Mistral model via InferencePool
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: mistral:latest
      backendRefs:
        - group: inference.networking.k8s.io
          kind: InferencePool
          name: mistral
    # Route for traditional backend (non-InferencePool)
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: some-cool-self-hosted-model
      backendRefs:
        - name: envoy-ai-gateway-basic-testupstream
EOF
```

## Step 7: Test the Configuration

Test different model routing scenarios:

```bash
# Get the Gateway external IP
GATEWAY_IP=$(kubectl get gateway inference-pool-with-aigwroute -o jsonpath='{.status.addresses[0].value}')
```

Test vLLM Llama model (routed via InferencePool):

```bash
curl -H "Content-Type: application/json" \
  -d '{
        "model": "meta-llama/Llama-3.1-8B-Instruct",
        "messages": [
            {
                "role": "user",
                "content": "Hi. Say this is a test"
            }
        ]
    }' \
  http://$GATEWAY_IP/v1/chat/completions
```

Test Mistral model (routed via InferencePool):

```bash
curl -H "Content-Type: application/json" \
  -d '{
        "model": "mistral:latest",
        "messages": [
            {
                "role": "user",
                "content": "Hi. Say this is a test"
            }
        ]
    }' \
  http://$GATEWAY_IP/v1/chat/completions
```

Test AIService backend (non-InferencePool):

```bash
curl -H "Content-Type: application/json" \
  -d '{
        "model": "some-cool-self-hosted-model",
        "messages": [
            {
                "role": "user",
                "content": "Hi. Say this is a test"
            }
        ]
    }' \
  http://$GATEWAY_IP/v1/chat/completions
```

## Advanced Features

### Model-Based Routing

AIGatewayRoute automatically extracts the model name from the request body and routes to the appropriate backend:

- **Automatic Extraction**: No need to manually set headers
- **Dynamic Routing**: Different models can use different InferencePools
- **Mixed Backends**: Combine InferencePool and AIServiceBackend in the same route based on model name by request Body.

### Token Rate Limiting

Configure token-based rate limiting for InferencePool backends:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: inference-pool-with-rate-limiting
spec:
  # ... other configuration ...
  llmRequestCosts:
    - metadataKey: llm_input_token
      type: InputToken
    - metadataKey: llm_output_token
      type: OutputToken
    - metadataKey: llm_total_token
      type: TotalToken
```

### Enhanced Observability

AIGatewayRoute provides rich metrics for InferencePool usage:

- **Model-specific metrics**: Track usage per model
- **Token consumption**: Monitor token usage and costs
- **Endpoint performance**: Detailed metrics per inference endpoint

## InferencePool Configuration Annotations

InferencePool supports configuration annotations to customize the external processor behavior:

### Processing Body Mode

Configure how the external processor handles request and response bodies:

```yaml
apiVersion: inference.networking.k8s.io/v1
kind: InferencePool
metadata:
  name: my-pool
  namespace: default
  annotations:
    # Configure processing body mode: "duplex" (default) or "buffered"
    aigateway.envoyproxy.io/processing-body-mode: "buffered"
spec:
  # ... other configuration ...
```

**Available values:**

- `"duplex"` (default): Uses `FULL_DUPLEX_STREAMED` mode for streaming processing
- `"buffered"`: Uses `BUFFERED` mode for buffered processing

### Allow Mode Override

Configure whether the external processor can override the processing mode:

```yaml
apiVersion: inference.networking.k8s.io/v1
kind: InferencePool
metadata:
  name: my-pool
  namespace: default
  annotations:
    # Configure allow mode override: "false" (default) or "true"
    aigateway.envoyproxy.io/allow-mode-override: "true"
spec:
  # ... other configuration ...
```

**Available values:**

- `"false"` (default): External processor cannot override the processing mode
- `"true"`: External processor can override the processing mode

### Combined Configuration

You can use both annotations together:

```yaml
apiVersion: inference.networking.k8s.io/v1
kind: InferencePool
metadata:
  name: my-pool
  namespace: default
  annotations:
    aigateway.envoyproxy.io/processing-body-mode: "buffered"
    aigateway.envoyproxy.io/allow-mode-override: "true"
spec:
  # ... other configuration ...
```

## Key Advantages over HTTPRoute

### Advanced OpenAI Routing

- Built-in OpenAI API schema validation
- Seamless integration with OpenAI SDKs
- Route multiple models in a single listener
- Mix InferencePool and traditional backends
- Automatic model extraction from request body

### AI-Specific Features

- Token-based rate limiting
- Model performance metrics
- Cost tracking and management
- Request/response transformation

## Next Steps

- Explore [token rate limiting](../traffic/usage-based-ratelimiting.md) in detail
- Review [observability best practices](../observability/) for AI workloads
- Configure [backend security policies](../security/upstream-auth.mdx) for your inference endpoints
- Learn more about the [Gateway API Inference Extension](https://gateway-api-inference-extension.sigs.k8s.io/) for advanced endpoint picker configurations
