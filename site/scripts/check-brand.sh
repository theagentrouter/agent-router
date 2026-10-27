#!/usr/bin/env bash
# Brand-rename safety checks (run in site root, wired to `npm run check:brand`).
#
# The rebrand renames the product name in prose ONLY. Kubernetes identifiers
# are frozen by the continuity promise: the `aigateway.envoyproxy.io` API
# group, all CRD kinds, the `aigw` CLI, `envoyproxy/ai-gateway` repo/image/
# chart paths, and the `envoy-ai-gateway-system` namespace.
set -euo pipefail

CURRENT="docs versioned_docs/version-1.1"
fail=0

note() { printf '%s\n' "$*"; }
err()  { printf 'FAIL: %s\n' "$*"; fail=1; }

# 1. The catastrophic false substitution: the API group must never become
#    the new domain.
if grep -rn 'theagentrouter\.ai/v1' $CURRENT src blog 2>/dev/null; then
  err "found 'theagentrouter.ai/v1*' — the K8s API group was renamed"
else
  note "OK: no 'theagentrouter.ai/v1*' anywhere"
fi

# 2. No old-site URLs remain in current docs or site code (api/api.mdx is
#    generated from Go doc comments — see check 4).
if grep -rn 'https://aigateway\.envoyproxy\.io' $CURRENT src 2>/dev/null | grep -v 'api/api\.mdx'; then
  err "old-domain URLs remain in current docs/src"
else
  note "OK: no https://aigateway.envoyproxy.io in current docs/src"
fi

# 3. The API group is still present (it must NOT have been swept away).
group_count=$(grep -rho 'aigateway\.envoyproxy\.io' $CURRENT | wc -l | tr -d ' ')
if [ "$group_count" -lt 100 ]; then
  err "API-group occurrences dropped to $group_count (expected >=100) — identifiers may have been renamed"
else
  note "OK: $group_count aigateway.envoyproxy.io identifier occurrences preserved"
fi

# 4. 'Envoy AI Gateway' in current docs only where intentional:
#    the "formerly" callout on the docs home, literal CLI output, and the
#    generated API reference (api/api.mdx is produced by `make apidoc` from
#    Go doc comments in api/v1alpha1 — rename it there, not here).
allowed='(index\.md|cli/installation\.md|api/api\.mdx)'
stray=$(grep -rln 'Envoy AI Gateway' $CURRENT | grep -Ev "$allowed" || true)
if [ -n "$stray" ]; then
  err "unexpected 'Envoy AI Gateway' occurrences in: $stray"
else
  note "OK: old name appears only in intentional 'formerly' mentions"
fi

# 5. GitHub web links point at the new org (theagentrouter/agent-router).
#    Only https://github.com / raw.githubusercontent.com URLs move — OCI image
#    and Helm chart paths (docker.io/envoyproxy/ai-gateway-*) are frozen.
if grep -rn 'github\.com/envoyproxy/ai-gateway\|raw\.githubusercontent\.com/envoyproxy/ai-gateway' $CURRENT src/data/home src/pages/index.tsx src/components docusaurus.config.ts 2>/dev/null | grep -v 'api/api\.mdx'; then
  err "old GitHub org URLs remain in current docs/chrome"
else
  note "OK: GitHub links use theagentrouter/agent-router"
fi

# 6. Frozen identifiers spot-check: these must still exist in current docs.
for token in 'aigw' 'AIGatewayRoute' 'envoy-ai-gateway-system' 'envoyproxy/ai-gateway'; do
  if ! grep -rq "$token" $CURRENT; then
    err "frozen identifier '$token' not found in current docs — over-renamed?"
  fi
done
note "OK: frozen identifiers (aigw, AIGatewayRoute, namespace, repo path) present"

if [ "$fail" -ne 0 ]; then
  echo "check:brand FAILED"
  exit 1
fi
echo "check:brand passed"
