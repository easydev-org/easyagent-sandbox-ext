#!/usr/bin/env bash
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REGISTRY="${REGISTRY:-git.agent.svc.cluster.local}"
NAMESPACE="${NAMESPACE:-easyagent}"
NAME="${NAME:-sandbox-ext}"
TAG="${TAG:-$(date +%Y%m%d%H%M%S)}"
DEST="${REGISTRY}/${NAMESPACE}/${NAME}:${TAG}"
BUILDKIT="${BUILDKIT_ADDR:-tcp://buildkitd.temp.svc.cluster.local:1234}"
REGISTRY_USER="${REGISTRY_USER:-root}"
REGISTRY_PASS="${REGISTRY_PASS:-devpassword}"
PROXY="${PROXY:-http://mihomo.develop.svc.cluster.local:7890}"
WORK="$(mktemp -d)"; trap 'rm -rf "${WORK}"' EXIT
buildctl --addr "${BUILDKIT}" build --frontend dockerfile.v0 \
  --local "context=${DIR}" --local "dockerfile=${DIR}" \
  --opt "filename=Dockerfile" \
  --opt "build-arg:REGISTRY=${REGISTRY}" \
  --opt "build-arg:HTTP_PROXY=${PROXY}" --opt "build-arg:HTTPS_PROXY=${PROXY}" \
  --output "type=docker,name=${NAMESPACE}/${NAME}:${TAG},dest=${WORK}/image.tar" --progress plain
skopeo copy --dest-creds "${REGISTRY_USER}:${REGISTRY_PASS}" --dest-tls-verify=false \
  "docker-archive:${WORK}/image.tar:${NAMESPACE}/${NAME}:${TAG}" "docker://${DEST}"
