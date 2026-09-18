This contains the basic example manifests to create an Envoy AI Gateway that handles
traffic for various AI providers.

## Examples

- `basic.yaml` - Basic configuration without any backends
- `openai.yaml` - OpenAI integration
- `aws.yaml` - AWS Bedrock with static credentials
- `aws-irsa.yaml` - AWS Bedrock with IRSA (IAM Roles for Service Accounts)
- `aws-pod-identity.yaml` - AWS Bedrock with EKS Pod Identity
- `azure_openai.yaml` - Azure OpenAI integration
- `azure_openai_managed_identity.yaml` - Azure OpenAI with Azure Managed Identity (system- or user-assigned)
- `gcp_vertex.yaml` - GCP Vertex AI integration
- `tars.yaml` - TARS integration
- `cohere.yaml` - Cohere integration

For AWS Bedrock, we recommend using either `aws-pod-identity.yaml` (EKS 1.24+) or
`aws-irsa.yaml` (all EKS versions) for production deployments instead of static credentials. [Docs](https://docs.aws.amazon.com/eks/latest/best-practices/identity-and-access-management.html#_identities_and_credentials_for_eks_pods)
