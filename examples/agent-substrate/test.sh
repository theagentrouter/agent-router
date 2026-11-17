#!/usr/bin/env bash
# Copyright Envoy AI Gateway Authors
# SPDX-License-Identifier: Apache-2.0
# The full text of the Apache license is available in the LICENSE file at
# the root of the repo.

# Checks the example end to end. Each check prints PASS or FAIL with evidence;
# the script exits non-zero if any check fails.
#
# It starts by clearing today's rate limit counters in Redis, so it can run any
# number of times in a row.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-agent-router-substrate}"
CTX="kind-${KIND_CLUSTER_NAME}"
WORK_DIR="${WORK_DIR:-${SCRIPT_DIR}/.work}"
KUBECTL_ATE="${KUBECTL_ATE:-${WORK_DIR}/bin/kubectl-ate}"
ATESPACE="agents"
NS="agent-router-demo"
AR_HOST="agent-router.envoy-gateway-system.svc.cluster.local"

k() { kubectl --context "${CTX}" "$@"; }
FAILED=0
pass() { printf '\033[32m[PASS]\033[0m %s\n' "$1"; shift; for e in "$@"; do printf '       %s\n' "${e}"; done; }
fail() { printf '\033[31m[FAIL]\033[0m %s\n' "$1"; shift; for e in "$@"; do printf '       %s\n' "${e}"; done; FAILED=$((FAILED + 1)); }
utc_now() { date -u +%Y-%m-%dT%H:%M:%S.000Z; }

# ---- Actor access through Substrate ingress ------------------------------------
# kubectl ate proxy needs an explicit local port; pick free ones.
port_free() { ! (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }
NEXT_PORT="${ACTOR_PROXY_BASE_PORT:-28100}"
PIDS=()
trap 'kill ${PIDS[@]+"${PIDS[@]}"} 2>/dev/null || true' EXIT
# port_of <actor>: the local port proxying to that actor (bash 3.2 has no maps).
port_of() { local v="PORT_${1//-/_}"; echo "${!v}"; }
for actor in actor-a actor-b actor-c; do
  while ! port_free "${NEXT_PORT}"; do NEXT_PORT=$((NEXT_PORT + 1)); done
  printf -v "PORT_${actor//-/_}" '%s' "${NEXT_PORT}"
  "${KUBECTL_ATE}" --context "${CTX}" proxy actor "${actor}" "${NEXT_PORT}:80" -a "${ATESPACE}" >/dev/null 2>&1 &
  PIDS+=($!)
  NEXT_PORT=$((NEXT_PORT + 1))
done
for actor in actor-a actor-b actor-c; do
  for _ in $(seq 1 30); do
    [[ "$(curl -s -m 5 -o /dev/null -w '%{http_code}' "localhost:$(port_of "${actor}")/readyz")" == "200" ]] && break
    sleep 1
  done
done

# call <actor> [json]: one chat completion from inside the actor's sandbox.
call() {
  local body="${2:-}"
  curl -s -m 60 "localhost:$(port_of "$1")/" -H 'content-type: application/json' -d "${body:-"{}"}"
}
status() { jq -r '.status' <<<"$1"; }
header() { jq -r --arg h "$2" '.headers[$h] // "-"' <<<"$1"; }
tokens() { jq -r '.usage.total_tokens // "-"' <<<"$1"; }
summary() { jq -c '{status, total_tokens: .usage.total_tokens, limit: .headers["x-ratelimit-limit"], remaining: .headers["x-ratelimit-remaining"], error}' <<<"$1"; }

# counter <actor>: tokens charged today, read from the rate limit service's Redis.
counter() {
  local v
  v="$(k -n redis-system exec deploy/redis -- sh -c \
    "redis-cli --scan --pattern '*_actor/${ATESPACE}/$1_*' | head -1 | xargs -r redis-cli get" 2>/dev/null)"
  echo "${v:-0}"
}
# wait_counter <actor> <expected>: response costs are charged when the stream
# ends, a moment after the client has its response.
wait_counter() {
  local v
  for _ in $(seq 1 20); do v="$(counter "$1")"; [[ "${v}" == "$2" ]] && break; sleep 0.25; done
  echo "${v}"
}
access_log() {
  k -n envoy-gateway-system logs -l gateway.envoyproxy.io/owning-gateway-name=agent-router \
    -c envoy --since-time="$1" --tail=-1 2>/dev/null | grep '^{"' || true
}
# ---- Reset ---------------------------------------------------------------------
k -n redis-system exec deploy/redis -- redis-cli FLUSHALL >/dev/null
echo "Cleared today's rate limit counters. Every actor has its own budget of 500 tokens"
echo "per day; the mock LLM reports 200 total tokens per response."
echo

# ---- 1. Each actor is metered separately ---------------------------------------
T1="$(utc_now)"
a1="$(call actor-a)"; b1="$(call actor-b)"
ca="$(wait_counter actor-a 200)"; cb="$(wait_counter actor-b 200)"
logs=""
for _ in $(seq 1 30); do # Envoy flushes file access logs periodically.
  logs="$(access_log "${T1}" | jq -c 'select(.path=="/v1/chat/completions") | {actor, total_tokens, response_code}')"
  grep -q 'actor-a' <<<"${logs}" && grep -q 'actor-b' <<<"${logs}" && break
  sleep 1
done
log_a="$(grep '"actor/agents/actor-a"' <<<"${logs}" | head -1)"
log_b="$(grep '"actor/agents/actor-b"' <<<"${logs}" | head -1)"
# The model must see Agent Router's provider key, never the actor's JWT.
auth_seen="actor-a=$(header "${a1}" x-upstream-auth) actor-b=$(header "${b1}" x-upstream-auth)"
if [[ "$(status "${a1}")" == 200 && "$(status "${b1}")" == 200 && "${ca}" == 200 && "${cb}" == 200 &&
      "$(jq -r .total_tokens <<<"${log_a}")" == 200 && "$(jq -r .total_tokens <<<"${log_b}")" == 200 &&
      "${auth_seen}" == "actor-a=provider-key actor-b=provider-key" ]]; then
  pass "1. Actor A and actor B each complete a call; tokens are metered per actor" \
    "actor-a: $(summary "${a1}")" "actor-b: $(summary "${b1}")" \
    "access log: ${log_a}" "access log: ${log_b}" "rate limit counters: actor-a=${ca} actor-b=${cb}" \
    "credential the model received: ${auth_seen}"
else
  fail "1. Actor A and actor B each complete a call; tokens are metered per actor" \
    "actor-a: $(summary "${a1}")" "actor-b: $(summary "${b1}")" \
    "access log: ${log_a:-<none>} / ${log_b:-<none>}" "rate limit counters: actor-a=${ca} actor-b=${cb}" \
    "credential the model received: ${auth_seen}"
fi

# ---- 2. A forged x-actor-id is ignored ------------------------------------------
ca_before="$(counter actor-a)"; cb_before="$(counter actor-b)"
a2="$(call actor-a '{"headers":{"x-actor-id":"actor/agents/actor-b"}}')"
ca_after="$(wait_counter actor-a $((ca_before + 200)))"; cb_after="$(counter actor-b)"
if [[ "$(status "${a2}")" == 200 && "$(header "${a2}" x-upstream-saw-actor-id)" == "actor/agents/actor-a" &&
      "${ca_after}" == $((ca_before + 200)) && "${cb_after}" == "${cb_before}" ]]; then
  pass "2. Actor A sends x-actor-id: actor/agents/actor-b and is still charged as actor A" \
    "upstream saw x-actor-id: $(header "${a2}" x-upstream-saw-actor-id)" \
    "actor-a counter ${ca_before} -> ${ca_after}; actor-b counter ${cb_before} -> ${cb_after}"
else
  fail "2. Actor A sends x-actor-id: actor/agents/actor-b and is still charged as actor A" \
    "response: $(summary "${a2}"), upstream saw: $(header "${a2}" x-upstream-saw-actor-id)" \
    "actor-a counter ${ca_before} -> ${ca_after}; actor-b counter ${cb_before} -> ${cb_after}"
fi

# ---- 3. Actor A runs out of budget; actor B doesn't notice ----------------------
# Tokens are charged after each response, and a request is admitted while the
# bucket isn't over the limit: 200, 400, 600 tokens, then 429.
a3="$(call actor-a)"; wait_counter actor-a 600 >/dev/null
a4="$(call actor-a)"
b2="$(call actor-b)"
if [[ "$(status "${a3}")" == 200 && "$(status "${a4}")" == 429 && "$(status "${b2}")" == 200 &&
      "$(header "${a4}" x-ratelimit-limit)" == "500, 500;w=86400" ]]; then
  pass "3. Actor A's 4th call gets 429 (500 tokens/day, 200 per call); actor B still gets 200" \
    "actor-a call 3: $(summary "${a3}")" "actor-a call 4: $(summary "${a4}")" \
    "actor-b:        $(summary "${b2}")"
else
  fail "3. Actor A's 4th call gets 429 (500 tokens/day, 200 per call); actor B still gets 200" \
    "actor-a call 3: $(summary "${a3}")" "actor-a call 4: $(summary "${a4}")" \
    "actor-b:        $(summary "${b2}")"
fi

# ---- 4. An actor without an egress rule can't reach Agent Router -----------------
# utc_now has one-second resolution (macOS date has no %N): wait a second so the
# window starts after every earlier request.
sleep 1
T4_START="$(utc_now)"
lookup="$(curl -s -m 30 "localhost:$(port_of actor-c)/lookup?host=${AR_HOST}")"
c1="$(call actor-c)"
ar_ip="$(k -n envoy-gateway-system get service agent-router -o jsonpath='{.spec.clusterIP}')"
c2="$(call actor-c "{\"url\":\"http://${ar_ip}/v1/chat/completions\"}")"
sleep 1
T4_END="$(utc_now)"
denied="$(k -n ate-system logs deploy/atenet-egress -c ext-proc --since-time="${T4_START}" 2>/dev/null |
  grep -c '"egress denied: actor has no egress policy".*"name":"actor-c"' || true)"
if [[ "$(jq -r .not_found <<<"${lookup}")" == "true" && "$(status "${c1}")" == 0 &&
      "$(jq -r .error <<<"${c1}")" == *"no such host"* && "$(status "${c2}")" != 200 && "${denied}" -gt 0 ]]; then
  C4_OK=1
else
  C4_OK=0
fi
C4_EVIDENCE=("DNS lookup of ${AR_HOST}: $(jq -c '{not_found, error}' <<<"${lookup}")"
  "call by name: $(jq -c '{status, error}' <<<"${c1}")"
  "call by ClusterIP ${ar_ip}: $(jq -c '{status, error}' <<<"${c2}")"
  "Substrate egress gateway logged ${denied} denial(s) for actor-c")

# ---- 5. No valid actor JWT, no entry ---------------------------------------------
b64url() { base64 | tr '+/' '-_' | tr -d '=\n'; }
forged="$(printf '%s' '{"alg":"ES256","typ":"JWT"}' | b64url).$(printf '%s' \
  '{"iss":"https://idp.ate-system.svc","sub":"actor/agents/actor-b","aud":["agent-router"],"exp":4102444800}' |
  b64url).$(head -c 64 /dev/zero | b64url)"
probe="agent-router-test-probe"
k -n "${NS}" delete pod "${probe}" --ignore-not-found --wait=true >/dev/null
k -n "${NS}" run "${probe}" --restart=Never --image=curlimages/curl:8.16.0 --command -- sh -c "
  u=http://${AR_HOST}/v1/chat/completions; b='{\"model\":\"demo-model\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}'
  echo no_token=\$(curl -s -o /dev/null -w '%{http_code}' -H 'content-type: application/json' -d \"\$b\" \$u)
  echo forged_jwt=\$(curl -s -o /dev/null -w '%{http_code}' -H 'authorization: Bearer ${forged}' -H 'content-type: application/json' -d \"\$b\" \$u)
  echo forged_header=\$(curl -s -o /dev/null -w '%{http_code}' -H 'x-actor-id: actor/agents/actor-a' -H 'content-type: application/json' -d \"\$b\" \$u)
" >/dev/null
k -n "${NS}" wait pod "${probe}" --for=jsonpath='{.status.phase}'=Succeeded --timeout=120s >/dev/null
probe_out="$(k -n "${NS}" logs "${probe}" | tr '\n' ' ')"
k -n "${NS}" delete pod "${probe}" --wait=false >/dev/null
if [[ "${probe_out}" == *"no_token=401"*"forged_jwt=401"*"forged_header=401"* ]]; then
  pass "5. A non-actor pod without a valid actor JWT gets 401" "${probe_out}"
else
  fail "5. A non-actor pod without a valid actor JWT gets 401" "${probe_out}"
fi

# ---- 6. Streaming works and is charged ----------------------------------------------
cb_before="$(counter actor-b)"
s1="$(call actor-b '{"stream":true}')"
cb_after="$(wait_counter actor-b $((cb_before + 200)))"
if [[ "$(status "${s1}")" == 200 && "$(jq -r .chunks <<<"${s1}")" -gt 1 && "$(tokens "${s1}")" == 200 &&
      "${cb_after}" == $((cb_before + 200)) ]]; then
  pass "6. A streamed call from actor B succeeds and is charged when the stream ends" \
    "$(jq -c '{status, chunks, content, usage}' <<<"${s1}")" "actor-b counter ${cb_before} -> ${cb_after}"
else
  fail "6. A streamed call from actor B succeeds and is charged when the stream ends" \
    "$(jq -c '{status, chunks, content, usage, error}' <<<"${s1}")" "actor-b counter ${cb_before} -> ${cb_after}"
fi

# ---- 4 (continued): Agent Router saw nothing from actor C --------------------------
# Checked last so Envoy has flushed its access log for the actor C window: wait
# until entries from checks 5 and 6, which came after the window, show up.
for _ in $(seq 1 30); do
  [[ -n "$(access_log "${T4_END}")" ]] && break
  sleep 1
done
in_window="$(access_log "${T4_START}" | jq -c --arg s "${T4_START}" --arg e "${T4_END}" \
  'select(.start_time >= $s and .start_time <= $e)' | wc -l | tr -d ' ')"
C4_EVIDENCE+=("Agent Router access log entries during actor C's attempts: ${in_window}")
if [[ "${C4_OK}" == 1 && "${in_window}" == 0 ]]; then
  pass "4. Actor C, with no egress rule for Agent Router, can't resolve or reach it" "${C4_EVIDENCE[@]}"
else
  fail "4. Actor C, with no egress rule for Agent Router, can't resolve or reach it" "${C4_EVIDENCE[@]}"
fi

echo
if [[ "${FAILED}" -eq 0 ]]; then
  echo "All 6 checks passed."
else
  echo "${FAILED} check(s) failed."
fi
exit "${FAILED}"
