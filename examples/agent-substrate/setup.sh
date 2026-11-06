#!/usr/bin/env bash
# Copyright Envoy AI Gateway Authors
# SPDX-License-Identifier: Apache-2.0
# The full text of the Apache license is available in the LICENSE file at
# the root of the repo.

# Builds the whole example on a new kind cluster:
#   kind + Agent Substrate (built from a pinned commit) + Envoy Gateway with
#   global rate limiting and Redis + Agent Router + the example's own pieces.
#
# Usage: ./setup.sh
# Re-running needs a fresh cluster: run ./cleanup.sh first.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# ---- Pinned versions (keep in sync with README.md) --------------------------
SUBSTRATE_REPO="${SUBSTRATE_REPO:-https://github.com/agent-substrate/substrate.git}"
SUBSTRATE_SHA="${SUBSTRATE_SHA:-404c7851ddb1f6a9a8d886870f66cd4ad251dfbd}"
ENVOY_GATEWAY_VERSION="${ENVOY_GATEWAY_VERSION:-v1.9.3}"
AGENT_ROUTER_VERSION="${AGENT_ROUTER_VERSION:-v1.2.1}"

# ---- Settings ----------------------------------------------------------------
export KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-agent-router-substrate}"
CTX="kind-${KIND_CLUSTER_NAME}"
WORK_DIR="${WORK_DIR:-${SCRIPT_DIR}/.work}"
SUBSTRATE_DIR="${SUBSTRATE_DIR:-${WORK_DIR}/substrate}"
KUBECTL_ATE="${WORK_DIR}/bin/kubectl-ate"
REGISTRY="localhost:5001"
IMAGE_PREFIX="${REGISTRY}/agent-substrate-demo"
ATESPACE="agents"

START=$(date +%s)
step() { printf '\n\033[1;36m==> [%ss] %s\033[0m\n' "$(($(date +%s) - START))" "$*"; }
die() { echo "error: $*" >&2; exit 1; }
k() { kubectl --context "${CTX}" "$@"; }
# kubectl-ate prints a warning about the shared identity on every call; it is
# expected on a dev cluster, so keep it out of the output.
ate() { "${KUBECTL_ATE}" --context "${CTX}" "$@" 2> >(grep -v '^Warning: no --token-file' >&2); }

# ---- Prerequisites -----------------------------------------------------------
for bin in docker kubectl helm go git jq curl; do
  command -v "${bin}" >/dev/null || die "${bin} is required"
done
docker info >/dev/null 2>&1 || die "docker is not running"
# Substrate's cluster script deletes a same-named cluster; refuse instead.
if docker ps -a --filter "label=io.x-k8s.kind.cluster=${KIND_CLUSTER_NAME}" -q | grep -q .; then
  die "a kind cluster named ${KIND_CLUSTER_NAME} already exists. Run ./cleanup.sh first, or set KIND_CLUSTER_NAME."
fi
# Substrate builds its images with docker buildx and pushes them to the local
# registry. A docker-container builder can't reach localhost:5001, so default
# to the docker-driver builder of the current context.
export DOCKER_BUILD_FLAGS="${DOCKER_BUILD_FLAGS:---builder=$(docker context show)}"

# ---- 1. Agent Substrate source at the pinned commit ----------------------------
step "Fetching Agent Substrate ${SUBSTRATE_SHA:0:12}"
if [[ ! -d "${SUBSTRATE_DIR}/.git" ]]; then
  git clone --quiet --filter=blob:none "${SUBSTRATE_REPO}" "${SUBSTRATE_DIR}"
fi
git -C "${SUBSTRATE_DIR}" fetch --quiet origin "${SUBSTRATE_SHA}" 2>/dev/null || true
git -C "${SUBSTRATE_DIR}" -c advice.detachedHead=false checkout --quiet "${SUBSTRATE_SHA}"

# ---- 2. kind cluster + local registry (Substrate's script) ----------------------
step "Creating kind cluster ${KIND_CLUSTER_NAME} (Substrate's hack/create-kind-cluster.sh)"
(cd "${SUBSTRATE_DIR}" && hack/create-kind-cluster.sh)

# ---- 3. Agent Substrate ---------------------------------------------------------
step "Installing Agent Substrate (builds images from source; the first run takes a while)"
(
  cd "${SUBSTRATE_DIR}"
  export ATE_INSTALL_KIND=true
  # Actor JWTs need no credential provider, so egress credential injection
  # from Secrets is left off.
  go run ./cmd/ate-setup deploy ate-system \
    --credential-provider='{"enabled":false}' \
    --ingress-allowed-spiffe-ids=spiffe://cluster.local/ns/ate-system/sa/atenet-client
)
mkdir -p "${WORK_DIR}/bin"
(cd "${SUBSTRATE_DIR}" && go build -o "${KUBECTL_ATE}" ./cmd/kubectl-ate)

# ---- 4. Envoy Gateway (global rate limit + Redis) and Agent Router -------------
step "Installing Redis, Envoy Gateway ${ENVOY_GATEWAY_VERSION} and Agent Router ${AGENT_ROUTER_VERSION}"
k apply -f "${REPO_ROOT}/examples/token_ratelimit/redis.yaml"
helm upgrade -i eg oci://docker.io/envoyproxy/gateway-helm \
  --version "${ENVOY_GATEWAY_VERSION}" --kube-context "${CTX}" \
  --namespace envoy-gateway-system --create-namespace \
  -f "${REPO_ROOT}/manifests/envoy-gateway-values.yaml" \
  -f "${REPO_ROOT}/examples/token_ratelimit/envoy-gateway-values-addon.yaml"
helm upgrade -i aieg-crd oci://docker.io/envoyproxy/ai-gateway-crds-helm \
  --version "${AGENT_ROUTER_VERSION}" --kube-context "${CTX}" \
  --namespace envoy-ai-gateway-system --create-namespace
helm upgrade -i aieg oci://docker.io/envoyproxy/ai-gateway-helm \
  --version "${AGENT_ROUTER_VERSION}" --kube-context "${CTX}" \
  --namespace envoy-ai-gateway-system --create-namespace
k wait --timeout=5m -n envoy-gateway-system deployment/envoy-gateway --for=condition=Available
k wait --timeout=5m -n envoy-ai-gateway-system deployment/ai-gateway-controller --for=condition=Available
k wait --timeout=5m -n redis-system deployment/redis --for=condition=Available

# ---- 5. Example images -------------------------------------------------------------
step "Building the mock LLM and actor images"
for c in mock-llm substrate/actor; do
  name="$(basename "${c}")"
  docker build --quiet -f "${SCRIPT_DIR}/${c}/Dockerfile" -t "${IMAGE_PREFIX}/${name}:dev" "${SCRIPT_DIR}" >/dev/null
  docker push --quiet "${IMAGE_PREFIX}/${name}:dev" >/dev/null
  echo "  ${IMAGE_PREFIX}/${name}:dev"
done
# Substrate only runs actor images pinned by digest.
ACTOR_IMAGE="$(docker inspect --format '{{index .RepoDigests 0}}' "${IMAGE_PREFIX}/actor:dev")"

# ---- 6. Agent Router configuration -------------------------------------------------
step "Applying the Agent Router configuration"
k apply -f "${SCRIPT_DIR}/agent-router/gateway.yaml"
k apply -f "${SCRIPT_DIR}/mock-llm/deploy.yaml"
k apply -f "${SCRIPT_DIR}/agent-router/route.yaml" \
  -f "${SCRIPT_DIR}/agent-router/idp-trust.yaml" \
  -f "${SCRIPT_DIR}/agent-router/security.yaml" \
  -f "${SCRIPT_DIR}/agent-router/ratelimit.yaml"
k -n agent-router-demo rollout status deployment/mock-llm --timeout=3m
k -n agent-router-demo wait gateway/agent-router --for=condition=Programmed --timeout=5m
for p in securitypolicy/agent-router backendtrafficpolicy/actor-token-budget; do
  k -n agent-router-demo wait "${p}" --timeout=2m \
    --for=jsonpath='{.status.ancestors[0].conditions[?(@.type=="Accepted")].status}'=True
done
k -n agent-router-demo wait backendtlspolicy/substrate-idp --timeout=2m \
  --for=jsonpath='{.status.ancestors[0].conditions[?(@.type=="Accepted")].status}'=True
k -n envoy-gateway-system get service agent-router >/dev/null \
  || die "expected Envoy Gateway to create Service envoy-gateway-system/agent-router"

# ---- 7. Substrate actors -------------------------------------------------------
step "Creating the worker pool, actor template and actors"
(
  cd "${SUBSTRATE_DIR}"
  KO_DOCKER_REPO="${REGISTRY}" KO_DEFAULTPLATFORMS="linux/$(go env GOARCH)" \
    hack/run-tool.sh ko resolve --base-import-paths -f "${SCRIPT_DIR}/substrate/workerpool.yaml"
) | k apply -f -
k -n agents rollout status deployment/llm-agents --timeout=5m
KUBECTL_ATE="${KUBECTL_ATE}" KUBE_CONTEXT="${CTX}" "${SCRIPT_DIR}/substrate/template.sh" "${ACTOR_IMAGE}"
for actor in actor-a actor-b actor-c; do
  ate get actor "${actor}" -a "${ATESPACE}" >/dev/null 2>&1 \
    || ate create actor "${actor}" -a "${ATESPACE}" --template llm-agent >/dev/null
  ate resume actor "${actor}" -a "${ATESPACE}" >/dev/null
done
# actor-c deliberately gets no egress policy.
ate create egress-policy actor-a -a "${ATESPACE}" -f "${SCRIPT_DIR}/substrate/egress-policy-a.yaml" >/dev/null
ate create egress-policy actor-b -a "${ATESPACE}" -f "${SCRIPT_DIR}/substrate/egress-policy-b.yaml" >/dev/null
ate get actor -a "${ATESPACE}"

# ---- 8. First call ---------------------------------------------------------------
step "Waiting for the first successful call (actor-b -> Substrate egress -> Agent Router -> mock LLM)"
PORT=28199
"${KUBECTL_ATE}" --context "${CTX}" proxy actor actor-b "${PORT}:80" -a "${ATESPACE}" >/dev/null 2>&1 &
PROXY_PID=$!
trap 'kill ${PROXY_PID} 2>/dev/null || true' EXIT
# The egress gateway caches policies for about 10 seconds.
for _ in $(seq 1 60); do
  result="$(curl -s -m 10 "localhost:${PORT}/" -H 'content-type: application/json' -d '{}' || true)"
  if [[ "$(jq -r '.status // empty' <<<"${result}" 2>/dev/null)" == "200" ]]; then
    jq -c '{status, usage, headers}' <<<"${result}"
    step "Done: first successful call $(($(date +%s) - START))s after setup.sh started"
    # Start test.sh and the walkthrough with unspent budgets.
    k -n redis-system exec deploy/redis -- redis-cli FLUSHALL >/dev/null
    echo
    echo "Next: ./test.sh, or follow the walkthrough in README.md."
    exit 0
  fi
  sleep 2
done
die "no successful call after 2 minutes; last result: ${result}"
