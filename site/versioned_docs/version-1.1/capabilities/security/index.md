---
id: security
title: Security
---

# Security

As Agent Router is built on Envoy Gateway, you can leverage the Envoy Gateway Security Policy by attaching them to the Gateway and/or generated HTTPRoutes.

:::tip
View all **[Envoy Gateway Security Docs](https://gateway.envoyproxy.io/docs/tasks/security/)** to learn more what security configurations are available to you.
:::

## Common Security Docs

Below are a list of common security configurations that can be useful when securing your gateway leveraging Envoy Gateway configurations.

### Access Control

- [JWT Validation](https://gateway.envoyproxy.io/docs/tasks/security/jwt-authentication/) - _Validate signed JWT tokens_
- [JWT Claim Based Authorization](https://gateway.envoyproxy.io/docs/tasks/security/jwt-claim-authorization/) - _Assert values of claims in JWT tokens_
- [Mutual TLS](https://gateway.envoyproxy.io/docs/tasks/security/mutual-tls/) - _Certificate based access control_
- [Integrate with External Authorization Service](https://gateway.envoyproxy.io/docs/tasks/security/ext-auth/) - _Useful for custom logic to your business_
- [OIDC Provider Integration](https://gateway.envoyproxy.io/docs/tasks/security/oidc/) - _When you want to have integration with a login flow for an end-user_
- [Require Basic Auth](https://gateway.envoyproxy.io/docs/tasks/security/basic-auth/)
- [Require API Key](https://gateway.envoyproxy.io/docs/tasks/security/apikey-auth/)
- [IP Allowlist/Denylist](https://gateway.envoyproxy.io/docs/tasks/security/restrict-ip-access/)

### TLS

- [Setup TLS Certificate](https://gateway.envoyproxy.io/docs/tasks/security/secure-gateways/)
- [Using TLS cert-manager](https://gateway.envoyproxy.io/docs/tasks/security/tls-cert-manager/)
