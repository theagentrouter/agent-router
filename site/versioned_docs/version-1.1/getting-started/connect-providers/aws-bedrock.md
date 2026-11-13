---
id: aws-bedrock
title: Connect AWS Bedrock
sidebar_position: 3
---

import CodeBlock from '@theme/CodeBlock';
import vars from '../../\_vars.json';

# Connect AWS Bedrock

This guide will help you configure Agent Router to work with AWS Bedrock's foundation models, including Llama, Anthropic Claude, and other models available on AWS Bedrock.

## Prerequisites

- AWS credentials with access to Bedrock
- Basic setup completed from the [Basic Usage](../basic-usage.md) guide
- Basic configuration removed as described in the [Advanced Configuration](./index.md) overview
- Enabled model access to "Llama 3.2 1B Instruct" in the `us-east-1` region (see [AWS Bedrock Model Access](https://docs.aws.amazon.com/bedrock/latest/userguide/model-access.html))

## Authentication Methods

Agent Router supports the AWS SDK default credential chain, which includes:

1. **EKS Pod Identity** - Recommended for production on EKS (v1.24+)
2. **IRSA (IAM Roles for Service Accounts)** - Recommended for production on EKS (all versions)
3. **Static Credentials** - For development, testing, or non-EKS environments

## Setup Instructions

### Option 1: EKS Pod Identity (Recommended for EKS 1.24+)

EKS Pod Identity is the simplest way to grant AWS permissions to your pods without managing static credentials.

**Step 1: Set up AWS IAM**

Follow the official AWS documentation to configure EKS Pod Identity:

- [EKS Pod Identity Agent installation](https://docs.aws.amazon.com/eks/latest/userguide/pod-id-agent-setup.html)
- [Create IAM role and policy](https://docs.aws.amazon.com/eks/latest/userguide/pod-id-minimum-sdk.html)

Your IAM policy needs these permissions:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "bedrock:InvokeModel",
        "bedrock:InvokeModelWithResponseStream",
        "bedrock:ListFoundationModels",
        "aws-marketplace:ViewSubscriptions"
      ],
      "Resource": "*"
    }
  ]
}
```

The Pod Identity association should reference:

- **Namespace**: `envoy-gateway-system`
- **ServiceAccount**: `ai-gateway-dataplane-aws`

**Step 2: Apply AI Gateway configuration**

<CodeBlock language="shell">
{`kubectl apply -f https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/aws-pod-identity.yaml

kubectl wait pods --timeout=2m \\
-l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \\
-n envoy-gateway-system --for=condition=Ready`}
</CodeBlock>

**Step 3: Test**

```shell
export GATEWAY_URL=$(kubectl get gateway envoy-ai-gateway-basic -n default -o jsonpath='{.status.addresses[0].value}')

curl -H "Content-Type: application/json" -d '{
  "model": "us.meta.llama3-2-1b-instruct-v1:0",
  "messages": [{"role": "user", "content": "Hello!"}]
}' http://$GATEWAY_URL/v1/chat/completions
```

---

### Option 2: IRSA (IAM Roles for Service Accounts)

IRSA works on all EKS versions and uses OIDC federation for authentication.

**Step 1: Set up AWS IAM**

Follow the official AWS documentation to configure IRSA:

- [Enable OIDC provider](https://docs.aws.amazon.com/eks/latest/userguide/enable-iam-roles-for-service-accounts.html)
- [Create IAM role with trust policy](https://docs.aws.amazon.com/eks/latest/userguide/associate-service-account-role.html)

Your IAM policy needs the same Bedrock permissions as Pod Identity above.

The trust policy should allow the ServiceAccount `system:serviceaccount:envoy-gateway-system:ai-gateway-dataplane-aws`.

**Step 2: Download and configure**

<CodeBlock language="shell">
{`curl -O https://raw.githubusercontent.com/theagentrouter/agent-router/${vars.aigwGitRef}/examples/basic/aws-irsa.yaml`}
</CodeBlock>

Edit `aws-irsa.yaml` and replace `ACCOUNT_ID` with your AWS account ID in the ServiceAccount annotation:

```yaml
eks.amazonaws.com/role-arn: arn:aws:iam::YOUR_ACCOUNT_ID:role/AIGatewayBedrockRole
```

**Step 3: Apply and test**

```shell
kubectl apply -f aws-irsa.yaml

kubectl wait pods --timeout=2m \
  -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
  -n envoy-gateway-system --for=condition=Ready

# Test (use same curl command as Pod Identity above)
export GATEWAY_URL=$(kubectl get gateway envoy-ai-gateway-basic -n default -o jsonpath='{.status.addresses[0].value}')
curl -H "Content-Type: application/json" -d '{
  "model": "us.meta.llama3-2-1b-instruct-v1:0",
  "messages": [{"role": "user", "content": "Hello!"}]
}' http://$GATEWAY_URL/v1/chat/completions
```

---

### Option 3: Static Credentials

Use AWS access key ID and secret for authentication. Suitable for development, testing, or non-EKS environments.

:::caution Production Warning
Static credentials are not recommended for production. Use EKS Pod Identity or IRSA instead.
:::

**Step 1: Download and configure**

```shell
curl -O https://raw.githubusercontent.com/theagentrouter/agent-router/{vars.aigwGitRef}/examples/basic/aws.yaml
```

Edit `aws.yaml` and replace the credential placeholders:

- `AWS_ACCESS_KEY_ID`: Your AWS access key ID
- `AWS_SECRET_ACCESS_KEY`: Your AWS secret access key

**Step 2: Apply and test**

```shell
kubectl apply -f aws.yaml

kubectl wait pods --timeout=2m \
  -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
  -n envoy-gateway-system --for=condition=Ready

# Test (use same curl command as above)
export GATEWAY_URL=$(kubectl get gateway envoy-ai-gateway-basic -n default -o jsonpath='{.status.addresses[0].value}')
curl -H "Content-Type: application/json" -d '{
  "model": "us.meta.llama3-2-1b-instruct-v1:0",
  "messages": [{"role": "user", "content": "Hello!"}]
}' http://$GATEWAY_URL/v1/chat/completions
```

You can also access an Anthropic model with native Anthropic messages endpoint:

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "anthropic.claude-3-5-sonnet-20241022-v2:0",
    "messages": [
      {
        "role": "user",
        "content": "What is capital of France?"
      }
    ],
    "max_tokens": 100
  }' \
  $GATEWAY_URL/anthropic/v1/messages
```

Expected output:

```json
{
  "id": "msg_01XFDUDYJgAACzvnptvVoYEL",
  "type": "message",
  "role": "assistant",
  "content": [
    {
      "type": "text",
      "text": "The capital of France is Paris."
    }
  ],
  "model": "claude-3-5-sonnet-20241022",
  "stop_reason": "end_turn",
  "usage": {
    "input_tokens": 13,
    "output_tokens": 8
  }
}
```

## Troubleshooting

If you encounter issues:

1. **Verify authentication is configured correctly**
   - For **EKS Pod Identity**: Check Pod Identity association exists
     ```shell
     aws eks list-pod-identity-associations --cluster-name YOUR_CLUSTER_NAME
     ```
   - For **IRSA**: Check ServiceAccount annotation
     ```shell
     kubectl get sa ai-gateway-dataplane-aws -n envoy-gateway-system -o yaml
     ```
   - For **Static Credentials**: Verify secret exists
     ```shell
     kubectl get secret -n default
     ```

2. **Check pod logs**

   ```shell
   POD_NAME=$(kubectl get pod -n envoy-gateway-system \
     -l gateway.envoyproxy.io/owning-gateway-name=envoy-ai-gateway-basic \
     -o jsonpath='{.items[0].metadata.name}')
   kubectl logs -n envoy-gateway-system $POD_NAME -c extproc
   ```

3. **Common errors**
   - **401/403**: Invalid credentials or insufficient IAM permissions
   - **404**: Model not found or not available in the specified region
   - **AssumeRole errors**: Check IAM role trust policy

For more details on AWS authentication, see:

- [AWS SDK credential chain](https://docs.aws.amazon.com/sdkref/latest/guide/standardized-credentials.html)
- [EKS Pod Identity documentation](https://docs.aws.amazon.com/eks/latest/userguide/pod-identities.html)
- [IRSA documentation](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html)

## Configuring More Models

To use additional models, add more [AIGatewayRouteRule]s to your configuration with the [model ID] in the `value` field. For example, to use [Claude 3 Sonnet]:

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: envoy-ai-gateway-basic-aws
  namespace: default
spec:
  parentRefs:
    - name: envoy-ai-gateway-basic
      kind: Gateway
      group: gateway.networking.k8s.io
  rules:
    - matches:
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: anthropic.claude-3-sonnet-20240229-v1:0
      backendRefs:
        - name: envoy-ai-gateway-basic-aws
```

## Using Anthropic Native API

When using Anthropic models on AWS Bedrock, you have two options:

1. **OpenAI-compatible format** (`/v1/chat/completions`) - Works with most models but may not support all Anthropic-specific features. Note that `max_tokens` is required by the Anthropic API — if your clients do not include it, you must inject it via a request body mutation.
2. **Native Anthropic API** (`/anthropic/v1/messages`) - Provides full access to Anthropic-specific features (only for Anthropic models)

### Streaming with Native Anthropic API

The native Anthropic API also supports streaming responses:

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "anthropic.claude-3-5-sonnet-20241022-v2:0",
    "messages": [
      {
        "role": "user",
        "content": "Count from 1 to 5."
      }
    ],
    "max_tokens": 100,
    "stream": true
  }' \
  $GATEWAY_URL/anthropic/v1/messages
```

## Advanced Features with Anthropic Models

Since the gateway supports the native Anthropic API, you have full access to Anthropic-specific features:

### Extended Thinking

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "anthropic.claude-3-5-sonnet-20241022-v2:0",
    "messages": [
      {
        "role": "user",
        "content": "Solve this puzzle: A farmer needs to cross a river with a fox, chicken, and bag of grain. The boat can only hold the farmer and one item. How does the farmer get everything across safely?"
      }
    ],
    "max_tokens": 1000,
    "thinking": {
      "type": "enabled",
      "budget_tokens": 5000
    }
  }' \
  $GATEWAY_URL/anthropic/v1/messages
```

### Prompt Caching

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "anthropic.claude-3-5-sonnet-20241022-v2:0",
    "system": [
      {
        "type": "text",
        "text": "You are an AI assistant specialized in Python programming. You help users write clean, efficient Python code.",
        "cache_control": {"type": "ephemeral"}
      }
    ],
    "messages": [
      {
        "role": "user",
        "content": "Write a function to calculate fibonacci numbers."
      }
    ],
    "max_tokens": 500
  }' \
  $GATEWAY_URL/anthropic/v1/messages
```

### Tool Use (Function Calling)

```shell
curl -H "Content-Type: application/json" \
  -d '{
    "model": "anthropic.claude-3-5-sonnet-20241022-v2:0",
    "messages": [
      {
        "role": "user",
        "content": "What is the weather in San Francisco?"
      }
    ],
    "max_tokens": 500,
    "tools": [
      {
        "name": "get_weather",
        "description": "Get the current weather in a given location",
        "input_schema": {
          "type": "object",
          "properties": {
            "location": {
              "type": "string",
              "description": "The city and state, e.g. San Francisco, CA"
            }
          },
          "required": ["location"]
        }
      }
    ]
  }' \
  $GATEWAY_URL/anthropic/v1/messages
```

[AIGatewayRouteRule]: ../../api/api.mdx#aigatewayrouterule
[model ID]: https://docs.aws.amazon.com/bedrock/latest/userguide/models-supported.html
[Claude 3 Sonnet]: https://docs.anthropic.com/en/docs/about-claude/models#model-comparison-table
