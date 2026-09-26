# Guardrails

These examples attach content-safety rules to an existing `AIServiceBackend` named `openai`.
Update `spec.targetRefs[].name` when your backend has a different name.

Apply the deterministic regex example:

```shell
kubectl apply -f regex.yaml
```

External providers require their service endpoint and, where applicable, a Kubernetes Secret:

```shell
kubectl apply -f providers.yaml
```

The expected Secret keys are:

- Presidio: optional `apiKey`
- Azure Content Safety: required `apiKey`
- AWS Bedrock Guardrails: optional `credentials` containing an AWS shared credentials file; when omitted, the ext-proc uses the standard AWS credential chain.

Provider failures block traffic by default. Set `failureMode: FailOpen` on a rule to continue traffic when the external provider is unavailable. Response guardrails force buffered response processing so blocked content is not partially delivered.
