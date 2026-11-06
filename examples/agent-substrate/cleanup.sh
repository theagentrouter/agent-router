#!/usr/bin/env bash
# Copyright Envoy AI Gateway Authors
# SPDX-License-Identifier: Apache-2.0
# The full text of the Apache license is available in the LICENSE file at
# the root of the repo.

# Deletes the example's kind cluster. The local registry (kind-registry on
# port 5001) is kept so a re-run reuses the images; pass --all to remove it
# and the .work directory (Substrate checkout and kubectl-ate) as well.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-agent-router-substrate}"
WORK_DIR="${WORK_DIR:-${SCRIPT_DIR}/.work}"
SUBSTRATE_DIR="${SUBSTRATE_DIR:-${WORK_DIR}/substrate}"

if docker ps -a --filter "label=io.x-k8s.kind.cluster=${KIND_CLUSTER_NAME}" -q | grep -q .; then
  echo "Deleting kind cluster ${KIND_CLUSTER_NAME}..."
  if [[ -x "${SUBSTRATE_DIR}/hack/kind.sh" ]]; then
    (cd "${SUBSTRATE_DIR}" && hack/kind.sh delete cluster --name "${KIND_CLUSTER_NAME}")
  else
    kind delete cluster --name "${KIND_CLUSTER_NAME}"
  fi
else
  echo "No kind cluster named ${KIND_CLUSTER_NAME}."
fi

if [[ "${1:-}" == "--all" ]]; then
  # Only remove the registry if Substrate's script created it and no other
  # kind cluster still uses it.
  if [[ "$(docker inspect -f '{{index .Config.Labels "created-by"}}' kind-registry 2>/dev/null)" == "agent-substrate" ]]; then
    others="$(docker ps -a --filter label=io.x-k8s.kind.cluster -q | while read -r id; do
      docker exec "${id}" test -d /etc/containerd/certs.d/localhost:5001 2>/dev/null && echo "${id}"; done || true)"
    if [[ -z "${others}" ]]; then
      echo "Removing the kind-registry container..."
      docker rm -f kind-registry >/dev/null
    else
      echo "Keeping kind-registry: another kind cluster uses it."
    fi
  fi
  # Only ever delete the default work directory, never a path from the environment.
  if [[ -d "${SCRIPT_DIR}/.work" ]]; then
    echo "Removing ${SCRIPT_DIR}/.work..."
    rm -rf "${SCRIPT_DIR}/.work"
  fi
fi
