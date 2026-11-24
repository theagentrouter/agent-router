This contains the basic example manifests to create an Envoy AI Gateway that handles
traffic for various AI providers.

## Examples

- `basic.yaml` - Basic configuration without any backends
- `openai.yaml` - OpenAI integration
- `scaleway.yaml` - Scaleway Generative APIs integration (project-scoped OpenAI-compatible endpoint)
- `openai-spiffe.yaml` - OpenAI integration without a static API key, using SPIFFE and OAuth 2.0 Token Exchange
- `aws.yaml` - AWS Bedrock with static credentials
- `aws-irsa.yaml` - AWS Bedrock with IRSA (IAM Roles for Service Accounts)
- `aws-pod-identity.yaml` - AWS Bedrock with EKS Pod Identity
- `azure_openai.yaml` - Azure OpenAI integration
- `gcp_vertex.yaml` - GCP Vertex AI integration
- `tars.yaml` - TARS integration
- `cohere.yaml` - Cohere integration
- `typesafe.yaml` - TypeSafe AI (Jev) integration

For AWS Bedrock, we recommend using either `aws-pod-identity.yaml` (EKS 1.24+) or
`aws-irsa.yaml` (all EKS versions) for production deployments instead of static credentials. [Docs](https://docs.aws.amazon.com/eks/latest/best-practices/identity-and-access-management.html#_identities_and_credentials_for_eks_pods)

## Scaleway Generative APIs

The `scaleway.yaml` example connects Envoy AI Gateway to Scaleway
Generative APIs using the project-scoped OpenAI-compatible endpoint:

`https://api.scaleway.ai/<project_id>/v1`

1. Set your Scaleway project ID and API secret key in your environment.
2. Retrieve the available models using the project-scoped endpoint:

   `GET https://api.scaleway.ai/<project_id>/v1/models`

   ```bash
   curl "https://api.scaleway.ai/${SCW_PROJECT_ID}/v1/models" \
     -H "Authorization: Bearer ${SCW_SECRET_KEY}"
   ```

3. Replace `SCW_PROJECT_ID`, `SCW_MODEL_ID`, and `SCW_SECRET_KEY`
   in `scaleway.yaml`. Choose a model ID returned by the API.
   Never commit real credentials to version control.
4. Apply `basic.yaml` and the configured `scaleway.yaml` to a
   Kubernetes cluster with Envoy AI Gateway installed.

Export the same model ID configured in `scaleway.yaml` and discover the Gateway address:

```bash
export SCW_MODEL_ID="llama-3.3-70b-instruct"
export GATEWAY_URL="http://$(kubectl get gateway envoy-ai-gateway-basic -n default -o jsonpath='{.status.addresses[0].value}')"
```

Send a chat completion request to the gateway using the configured model:

```bash
curl -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "x-ai-eg-model: ${SCW_MODEL_ID}" \
  -H "Content-Type: application/json" \
  -d "{\"model\":\"${SCW_MODEL_ID}\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello!\"}]}"
```
