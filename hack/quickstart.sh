#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
state="$root/out/quickstart"
cluster=composite-quickstart
context="kind-$cluster"
workload=composite-docker
namespace=composite-quickstart
image=composite:quickstart
# Match CAPI and the Kubernetes minor version used by the repository's envtest suite.
capi_version=v1.12.4
node_image=kindest/node:v1.34.0@sha256:7416a61b42b1662ca6ca89f02028ac133a309a2a30ba309614e8ec94d976dc5a
calico_manifest=https://raw.githubusercontent.com/projectcalico/calico/v3.31.0/manifests/calico.yaml
export KUBECONFIG="$state/kubeconfig"
export KIND_EXPERIMENTAL_PROVIDER=docker

fail() {
  printf 'quickstart: %s\n' "$*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "install $1 first (see docs/development.md#docker-provider-quickstart)"
}

kube() {
  kubectl --kubeconfig "$KUBECONFIG" --context "$context" "$@"
}

workload_kube() {
  kubectl --kubeconfig "$state/workload.kubeconfig" --context "kind-$workload" "$@"
}

status() {
  need kubectl
  test -s "$KUBECONFIG" || fail 'no quickstart kubeconfig; run make quickstart first'
  kube -n composite-system get deployment,pods
  kube -n "$namespace" get cluster,compositecluster,compositeprovider,dockercluster
  kube -n "$namespace" get kubeadmcontrolplane,machinedeployment,machine,kubeadmconfigtemplate
  if test -s "$state/workload.kubeconfig"; then workload_kube get nodes; fi
}

publish_endpoint() {
  # CAPD discovers the LB address on DockerCluster, not on CompositeCluster.
  # Explicit, one-time test-harness handoff; Composite itself never infers native endpoints.
  kube -n "$namespace" wait --for=create "dockercluster/$workload-infra" --timeout=120s
  kube -n "$namespace" wait --for=jsonpath='{.spec.controlPlaneEndpoint.host}' \
    "dockercluster/$workload-infra" --timeout=5m
  local endpoint host port
  endpoint=$(kube -n "$namespace" get "dockercluster/$workload-infra" \
    -o jsonpath='{.spec.controlPlaneEndpoint.host} {.spec.controlPlaneEndpoint.port}')
  read -r host port <<< "$endpoint"
  [[ "$host" =~ ^[0-9a-fA-F:.]+$ && "$port" =~ ^[0-9]+$ ]] || fail 'CAPD did not publish a valid IP/port endpoint'
  kube -n "$namespace" patch "compositecluster/$workload-infra" --type=merge \
    -p "{\"spec\":{\"controlPlaneEndpoint\":{\"host\":\"$host\",\"port\":$port}}}"
}

up() {
  for tool in docker kind kubectl clusterctl helm; do need "$tool"; done
  docker buildx version >/dev/null
  local platform clusters reserved
  platform=$(docker info --format '{{.OSType}}/{{.Architecture}}')
  case "$platform" in
    linux/amd64|linux/x86_64) platform=linux/amd64 ;;
    linux/arm64|linux/aarch64) platform=linux/arm64 ;;
    *) fail "unsupported Docker daemon platform: $platform (use Linux amd64 or arm64 containers)" ;;
  esac
  clusters=$(kind get clusters)
  for reserved in "$cluster" "$workload"; do
    if [[ $'\n'"$clusters"$'\n' == *$'\n'"$reserved"$'\n'* ]]; then
      fail "$reserved already exists; refusing to reuse it. Use make quickstart-down only if it belongs to this quickstart"
    fi
  done

  # Build and check the binary before creating any Kubernetes resources. No host Go required.
  docker buildx build --load --platform "$platform" --file "$root/build/operator.Dockerfile" \
    --build-arg version=quickstart --tag "$image" "$root"
  local version
  version=$(docker run --rm --platform "$platform" --read-only --network none --cap-drop ALL \
    --security-opt no-new-privileges "$image" --version)
  test "$version" = quickstart || fail 'image version smoke test failed'

  mkdir -p "$state/config/cluster-api/overrides" "$state/cache"
  printf 'providers: []\n' > "$state/clusterctl.yaml"
  printf 'pending\n' > "$state/consumer"
  # Record the reserved name before creation so a partial startup can be cleaned up.
  printf '%s\n' "$cluster" > "$state/managed-cluster"
  trap 'printf "quickstart: startup failed; resources are retained for inspection. Run make quickstart-down to clean up.\n" >&2' ERR
  kind create cluster --name "$cluster" --image "$node_image" --config "$root/local/quickstart-kind.yaml" \
    --kubeconfig "$KUBECONFIG" --wait 5m
  kind load docker-image "$image" --name "$cluster"
  # CAPD requires ClusterTopology even for manually created DockerClusterTemplates.
  CLUSTER_TOPOLOGY=true XDG_CONFIG_HOME="$state/config" XDG_CACHE_HOME="$state/cache" clusterctl init \
    --config "$state/clusterctl.yaml" --kubeconfig "$KUBECONFIG" --kubeconfig-context "$context" \
    --core "cluster-api:$capi_version" --bootstrap "kubeadm:$capi_version" \
    --control-plane "kubeadm:$capi_version" --infrastructure "docker:$capi_version" --wait-providers

  # Use the shipped chart/RBAC, not the permissive Tilt-only local manifests.
  helm upgrade --install composite "$root/helm" --namespace composite-system --create-namespace \
    --kubeconfig "$KUBECONFIG" --kube-context "$context" \
    --set-string image.repository=composite --set-string image.tag=quickstart \
    --set-string image.digest= --set image.pullPolicy=Never --wait --timeout 5m
  kube -n composite-system rollout status deployment/composite --timeout=5m
  kube apply -f "$root/local/quickstart-templates.yaml"
  kube wait --for=condition=ConfigurationValid=True compositeprovidertemplate/docker-local-v1 --timeout=120s
  printf 'applied\n' > "$state/consumer"
  kube apply -f "$root/local/quickstart.yaml"
  publish_endpoint
  kube -n "$namespace" wait --for=condition=Ready=True compositeprovider/docker --timeout=5m
  kube -n "$namespace" wait --for=condition=Ready=True "compositecluster/$workload-infra" --timeout=120s
  kube -n "$namespace" wait --for=jsonpath='{.status.initialization.controlPlaneInitialized}'=true \
    "kubeadmcontrolplane/$workload-control-plane" --timeout=10m
  # kind generates a host-reachable CAPD kubeconfig, including Docker Desktop's mapped port.
  kind get kubeconfig --name "$workload" > "$state/workload.kubeconfig"
  workload_kube apply --server-side -f "$calico_manifest"
  kube -n "$namespace" wait --for=condition=Available=True "machinedeployment/$workload-workers" --timeout=10m
  workload_kube wait --for=condition=Ready node --all --timeout=5m
  trap - ERR
  status
  printf '\nDocker provider is Ready; one control-plane node and one native worker are running.\n'
  printf 'Workload: kubectl --kubeconfig "%s/workload.kubeconfig" get nodes\n' "$state"
  printf 'Cleanup: make quickstart-down (deletes workload first, then the local management cluster)\n'
}

down() {
  need docker
  need kind
  local managed='' consumer=''
  if test -f "$state/managed-cluster"; then IFS= read -r managed < "$state/managed-cluster"; fi
  test "$managed" = "$cluster" || fail 'no managed quickstart cluster recorded; refusing to delete an unrelated cluster'
  if test -f "$state/consumer"; then IFS= read -r consumer < "$state/consumer"; fi
  if test "$consumer" = applied; then
    need kubectl
    # CAPD nodes/LB are sibling Docker containers, not children of the kind node.
    # Never destroy the management cluster until normal CAPI/Composite teardown succeeds.
    kube -n "$namespace" delete "cluster/$workload" --ignore-not-found=true --wait=true --timeout=10m
  fi
  kind delete cluster --name "$cluster" --kubeconfig "$KUBECONFIG"
  printf 'absent\n' > "$state/managed-cluster"
  printf 'Deleted quickstart workload and management cluster. Local images and ignored output files are retained.\n'
}

if test "$#" -ne 1; then fail 'usage: bash hack/quickstart.sh {up|status|down}'; fi
case "$1" in
  up) up ;;
  status) status ;;
  down) down ;;
  *) fail 'usage: bash hack/quickstart.sh {up|status|down}' ;;
esac
