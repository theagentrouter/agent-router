# Proposal: Optimize MCP Initialize Phase via Backend-Only Authorization Pre-Check

## 1. Motivation

In the current architecture of the Envoy AI Gateway (AIGW) MCP Proxy, when a client connects and sends an `initialize` JSON-RPC request, the proxy establishes an upstream SSE (Server-Sent Events) connection to **all** backends defined in the route. It then asks each backend for its list of available tools. 

Authorization checks (via Common Expression Language, or CEL) are only performed *after* initialization, when the client sends a `tools/list` or `tools/call` request. 

Consider the following route configuration (represented as JSON for dynamic injection):

```json
{
  "name": "tenant-abc",
  "backends": [
    {
      "name": "github",
      "host": "api.githubcopilot.com",
      "backendPath": "/mcp",
      "auth": "Bearer ghp_xxx..."
    },
    {
      "name": "servicenow",
      "host": "instance.service-now.com",
      "backendPath": "/mcp/v1",
      "auth": "Bearer sn_token_xxx..."
    }
  ],
  "authorization": {
    "defaultAction": "Deny",
    "rules": [
      {
        "action": "Allow",
        "source": { "jwt": { "scopes": ["mcp:admin"] } },
        "target": { "tools": [{ "backend": "*", "tool": "*" }] }
      },
      {
        "action": "Allow",
        "source": { "jwt": { "scopes": ["mcp:snuser"] } },
        "target": { "tools": [{ "backend": "servicenow", "tool": "*" }] }
      }
    ]
  }
}
```

(`tool: "*"` above is illustrative shorthand for "every tool on this backend." The implementation's `Target.Tools` matching only wildcards the backend, not the tool - expressing "all tools on `servicenow`" for real requires a CEL condition like `request.mcp.backend == "servicenow"` instead, which is exactly the kind of expression Section 3 optimizes for.)

If a client connects with a JWT containing only the `mcp:snuser` scope, they are mathematically barred from accessing any tools on the `github` backend. However, the MCP Proxy will **still establish a connection to `github`** during the `initialize` phase.

**The Problem:**
This behavior causes several issues depending on the authentication configuration:

1. **Service Account Overhead:** If the proxy uses a hardcoded outbound credential (`auth: "Bearer ghp_xxx"`), the connection succeeds silently. The proxy wastes network I/O, memory, and compute to fetch and parse the GitHub tool list, only to completely hide it from the user during `tools/list`.
2. **Pass-Through Authentication Failures:** If the proxy relies on forwarding the client's JWT to authenticate with the upstream backend, the `initialize` call to GitHub will fail (e.g., 401 Unauthorized) because the ServiceNow token is invalid for GitHub. While AIGW gracefully recovers from single-backend connection failures, relying on network-level 401s as a secondary form of access control generates unnecessary error logs, false-positive alerts, and wastes network resources.
3. **Latency:** The client's initialization phase blocks until all backend connections (or their failure timeouts) resolve.

## 2. Why it behaves this way currently

Currently, the MCP Proxy (`internal/mcpproxy/authorization.go`) compiles authorization rules into a single CEL expression per rule. These expressions evaluate properties from the client request against both the backend and the specific tool name.

For example, a rule allowing a user to access a specific tool on GitHub is compiled to something like:
```cel
(request.mcp.backend == "github") && (request.mcp.tool == "list_issues") && ...
```

During the `initialize` phase, the client has not requested a specific tool yet. If the proxy tries to evaluate the CEL expression without a tool name, the expression either fails or correctly evaluates to `false`. Because the proxy cannot evaluate the rule without the tool name, it defers all authorization checks until the `tools/list` or `tools/call` phase, meaning it must connect to all backends initially.

## 3. Proposed Solution

We propose optimizing the `initialize` phase entirely within the `mcpproxy` component, using [cel-go's partial evaluation support](https://pkg.go.dev/github.com/google/cel-go/cel#OptPartialEval) rather than generating a second, textually-stripped CEL expression per rule.

`compileAuthorization` in `mcpproxy/authorization.go` compiles each rule's expression twice from the same AST:
1. **The Full Program (Existing):** Compiled with `cel.OptOptimize`. Evaluates the backend, tool, and client context. Used for `tools/list` and `tools/call`.
2. **A Backend-Only Program (New):** The *same* expression, compiled with `cel.OptPartialEval`. During the `initialize`-phase pre-check, it's evaluated with `request.mcp.tool` marked as an **unknown** CEL attribute rather than a concrete value (via a `cel.AttributePattern`) - not omitted, and not a placeholder like an empty string.

This distinction matters. cel-go's partial evaluation correctly decides any part of the expression that doesn't depend on the tool (backend checks, headers, JWT claims - including when they're combined with a tool check via `&&`/`||`, since CEL's logical short-circuiting resolves each side independently) while reporting the tool-dependent part as genuinely **ambiguous**, rather than silently resolving it to a specific - and potentially wrong - `true`/`false`. Naively evaluating the expression with the tool field omitted (e.g. treated as an empty string) is not safe: a `Deny` rule using `!=` against the tool (`request.mcp.tool != "safe_tool"`) would evaluate to `true` for an empty tool value and wrongly block the entire backend; an `Allow` rule that's the only path granting access, gated on `request.mcp.tool == "X"`, would evaluate to `false` and wrongly prune a backend the client should have been allowed to reach.

### 3.1. Compilation Updates

In `internal/mcpproxy/authorization.go`, each compiled rule holds two programs derived from the same expression:

```go
type compiledAuthorizationRule struct {
    Action        filterapi.AuthorizationAction
    celExpression string

    celProgram         cel.Program // Compiled with cel.OptOptimize. Used for tools/list and tools/call.
    backendOnlyProgram cel.Program // Same expression, compiled with cel.OptPartialEval. Used during initialize.
    // ...
}
```

There is no separate "stripped" expression string - both programs compile the exact same `celExpression`. Only the evaluation-time activation differs.

### 3.2. Evaluation Updates

We introduce a pre-check function that evaluates `backendOnlyProgram` with `request.mcp.tool` marked unknown:

```go
// authorizeBackendOnly evaluates whether the client has potential access to a backend,
// ignoring specific tool constraints.
func (m *mcpRequestContext) authorizeBackendOnly(auth *compiledAuthorization, backend string, headers http.Header, claims jwt.MapClaims, scopes sets.Set[string]) bool {
    // For each rule: evaluate rule.backendOnlyProgram against a partial activation
    // with request.mcp.tool unknown.
    //
    //   - A concrete true/false result is used directly.
    //   - An Unknown result means the rule's outcome genuinely depends on the tool,
    //     which isn't known yet. This is resolved asymmetrically:
    //       - An ambiguous Deny rule is skipped: it can't be proven to fire for
    //         whichever tool ends up being requested, so it must not block the
    //         backend.
    //       - An ambiguous Allow rule is not treated as a non-match either: it
    //         falls through to the rule's Target/Source checks instead of being
    //         excluded outright.
    //
    // This preserves the invariant that the pre-check may end up more permissive
    // than the real per-tool check (attempting a connection that the full check
    // later denies for a specific tool) but must never be more restrictive
    // (skipping a backend the client would actually have been allowed to use).
}
```

### 3.3. Proxy `newSession` Optimization

Finally, we update the initialization loop in `mcpproxy.go`:

```go
func (m *mcpRequestContext) newSession(...) (*session, error) {
    // ...
    // JWT claims/scopes are extracted once per request - they're identical for every backend.
    claims, scopes := m.extractClaimsAndScopes(m.requestHeaders, "authorizeBackendOnly")

    for _, backend := range backends.backends {
        if backends.authorization != nil {
            // Pre-check the backend!
            if !m.authorizeBackendOnly(backends.authorization, backend.Name, m.requestHeaders, claims, scopes) {
                m.l.Debug("skipping backend connection due to authorization rules", slog.String("backend", backend.Name))
                continue // Prune the backend connection!
            }
        }

        // Only connect to backends the user actually has permission to see
        initResult, err := m.initializeSession(ctx, routeName, &backend, p, startAt)
    }
}
```

## 4. Dynamic Multi-Tenancy & Subsetting via CEL

In enterprise architectures where client authentication and tenant-level authorization are handled by a separate, specialized process (such as a JWT Auth filter, an Envoy `ext_authz` service, or a custom External Processor), the allowed scope of backends and tools can be computed externally and passed as metadata headers to the MCP Proxy. 

By leveraging the pre-check engine outlined in Section 3, the MCP Proxy can natively enforce these multi-tenant constraints dynamically using declarative, CEL-based SecurityPolicies.

### 4.1. Implementation Design: Unified Fully-Qualified Tool Names (FQN)
In this design, the trusted external orchestrator computes a list of allowed tools formatted as `backendName__toolName` and injects them into a single header: `x-ai-eg-mcp-tool-subset`.

#### Example Header Value:
```http
x-ai-eg-mcp-tool-subset: database-mcp__read_record,email-mcp__send_alert
```

#### Declarative MCPRoute YAML Configuration:

This is a single rule with three `&&`-joined conditions, not two separate rules keyed off `request.mcp.method`. `authorizeBackendOnly` never populates `request.mcp.method`, so a rule gated on `request.mcp.method == "initialize"` would never fire during the pre-check - and even if it did, two independent `Allow` rules don't compose safely here: rules are evaluated independently, so an ambiguous second rule with no `Target`/`Source` would unconditionally permit every backend at `initialize`, silently defeating whatever the first rule decided, regardless of rule order (see 3.2).

```yaml
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: MCPRoute
metadata:
  name: dynamic-multi-tenant-route
spec:
  parentRefs:
    - name: ai-gateway
  path: /mcp
  backendRefs:
    - name: database-mcp
    - name: email-mcp
    - name: analytics-mcp
  securityPolicy:
    authorization:
      defaultAction: Deny
      rules:
        - action: Allow
          cel: |
            ("x-ai-eg-mcp-tool-subset" in request.headers) &&
            ("," + request.headers["x-ai-eg-mcp-tool-subset"]).contains("," + request.mcp.backend + "__") &&
            ("," + request.headers["x-ai-eg-mcp-tool-subset"] + ",").contains("," + request.mcp.backend + "__" + request.mcp.tool + ",")
```

How the three clauses behave at each phase:

- The leading `"x-ai-eg-mcp-tool-subset" in request.headers` clause keeps a missing header decidably `false` rather than a CEL evaluation error (indexing an absent header key errors, not `""`, and an error is treated the same as ambiguous - see 3.2 - which would otherwise let this `Allow` rule fall through unconditionally when the header is simply missing). Note also that `has(request.headers["..."])` does not work as a substitute here: the `has()` macro only accepts a plain field selection, not an index expression.
- The second clause checks only `request.mcp.backend + "__"` - a prefix that never touches the tool. At `initialize`, this is fully decidable per backend: for a backend with zero entries in the subset, `&&` short-circuits on this concrete `false` before the third, tool-dependent clause is even reached, correctly pruning the backend.
- For a backend with at least one entry, the second clause is `true` and the third clause comes back ambiguous (tool not known yet), so the rule falls through to attempting the backend - deferring the exact-tool decision to `tools/list`/`tools/call`, where all three clauses are concrete and the third enforces the exact FQN match.

### 4.2. Architectural & Security Advantages
1. **Decoupled Architecture**: All tenant-lookup and permission databases remain encapsulated within the identity management process. The AI Gateway proxy remains a high-performance, stateless data-plane.
2. **Dynamic yet Declarative**: We avoid writing custom, rigid Go code or hardcoding proxy behavior. All multi-tenant routing decisions and scoping rules are handled elegantly through standard CEL expressions.
3. **Multi-layer Safeguards, but Envoy has to actually enforce the trust boundary**: This CEL rule trusts `x-ai-eg-mcp-tool-subset` completely - the proxy has no way to distinguish a header set by a trusted upstream filter from one forged by the client. Envoy must be configured to strip client-supplied headers of this name and only ever set it from a trusted `ext_authz`/ExtProc response (e.g. via `HeaderMutation`/header sanitization). This proposal does not add any such enforcement on its own; it only makes the header usable once it's trustworthy.
4. **Defense-in-Depth**: Because the same CEL rule gates `tools/call` at runtime in addition to filtering `tools/list`, if a client attempts to forge a request to invoke an unauthorized tool, the proxy rejects the execution natively - a client can't bypass subsetting by calling a tool it was never shown.

---

## 5. Benefits

1. **Efficiency:** Reduces upstream connection overhead and memory usage in the proxy.
2. **Latency:** Faster initialization for clients, especially in multi-tenant environments where routes map to dozens of backends.
3. **Security Posture:** Enforces backend isolation earlier in the connection lifecycle (Defense in Depth).
4. **Decoupled Control**: Perfectly supports enterprise multi-tenancy where routing and permissions are driven dynamically by separate authorization services.

