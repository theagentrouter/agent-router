#!/usr/bin/env bash
# Copyright Envoy AI Gateway Authors
# SPDX-License-Identifier: Apache-2.0
# The full text of the Apache license is available in the LICENSE file at
# the root of the repo.

# Creates the "agents" atespace and the "llm-agent" ActorTemplate, then waits
# for the template's golden snapshot so actors start from a warm image.
#
# Usage: template.sh <actor-image@sha256:digest>
# Env:   KUBECTL_ATE (default: kubectl-ate on PATH), KUBE_CONTEXT
set -euo pipefail

ACTOR_IMAGE="${1:?usage: $0 <actor-image@sha256:digest>}"
ATESPACE="${ATESPACE:-agents}"
TEMPLATE="${TEMPLATE:-llm-agent}"
KUBECTL_ATE="${KUBECTL_ATE:-kubectl-ate}"
# kubectl-ate warns about acting as the shared identity on every call; that's
# expected on a dev cluster, so keep it out of the output.
ate() {
  "${KUBECTL_ATE}" ${KUBE_CONTEXT:+--context "${KUBE_CONTEXT}"} "$@" \
    2> >(grep -v '^Warning: no --token-file' >&2)
}

if [[ "${ACTOR_IMAGE}" != *@sha256:* ]]; then
  echo "error: Substrate requires the actor image pinned by digest, got ${ACTOR_IMAGE}" >&2
  exit 1
fi

if ! ate get atespace "${ATESPACE}" >/dev/null 2>&1; then
  ate create atespace "${ATESPACE}"
fi

if ate get actor-template "${TEMPLATE}" -a "${ATESPACE}" >/dev/null 2>&1; then
  echo "actor template ${ATESPACE}/${TEMPLATE} already exists; leaving it as is"
else
  # ActorTemplate is a Substrate API object (protojson), not a Kubernetes CRD.
  ate create actor-template -f - <<EOF
metadata:
  atespace: ${ATESPACE}
  name: ${TEMPLATE}
workerSelector:
  matchLabels:
    workload: llm-agents
containers:
  - name: actor
    image: ${ACTOR_IMAGE}
    command: ["/app"]
    env:
      - name: AGENT_ROUTER_URL
        value: http://agent-router.envoy-gateway-system.svc.cluster.local/v1/chat/completions
    wakeupProbe:
      httpGet:
        path: /readyz
        port: 80
resources:
  limits:
    cpuMillicores: 500
    memoryBytes: 268435456 # 256Mi
snapshotConfig:
  preferredFidelity: SNAPSHOT_FIDELITY_MEMORY
EOF
fi

echo "Waiting for the ${ATESPACE}/${TEMPLATE} golden snapshot..."
for _ in $(seq 1 120); do
  status="$(ate get actor-template "${TEMPLATE}" -a "${ATESPACE}" -o json 2>/dev/null)"
  if [[ -n "$(jq -r '.status.goldenSnapshotStatus.goldenTag.name // empty' <<<"${status}")" ]]; then
    echo "golden snapshot ready"
    exit 0
  fi
  if err="$(jq -r '.status.goldenSnapshotStatus.errorMessage // empty' <<<"${status}")" && [[ -n "${err}" ]]; then
    echo "error: golden snapshot failed: ${err}" >&2
    exit 1
  fi
  sleep 5
done
echo "error: timed out waiting for the golden snapshot" >&2
exit 1
