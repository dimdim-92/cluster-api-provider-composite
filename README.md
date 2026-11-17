# cluster-api-provider-composite

This project explores how a single CAPI-managed Kubernetes cluster could use
multiple native infrastructure providers, such as Outscale and OpenStack, while
keeping the existing providers and CAPI in charge of cloud operations and workers.

Composite is a Cluster API infrastructure meta-provider. One `CompositeCluster`
coordinates native InfraClusters and bootstrap templates behind CAPI's single
infrastructure reference.

![Cluster infrastructureRef points to CompositeCluster, which owns OscCluster, OpenStackCluster and per-attachment KubeadmConfigTemplates.](<docs/diagrams/cluster-infrastructure.svg>)

[Editable Archify source](<docs/diagrams/cluster-infrastructure.architecture.json>).

## What it provides

The provider installs four CRDs, all serving and storing
`infrastructure.cluster.x-k8s.io/v1beta2`:

| Resource | Scope | Purpose |
|---|---|---|
| `CompositeProviderTemplate` (`cpt`) | Cluster | Immutable reusable provider/account/region configuration and native template references |
| `CompositeProvider` (`cprov`) | Namespaced | A provider attachment selecting a cluster, provider template and namespace-local credential Secret |
| `CompositeCluster` (`cocl`) | Namespaced | Native resource ownership, endpoint propagation, bootstrap templates and readiness |
| `CompositeClusterTemplate` (`cct`) | Namespaced | Immutable infrastructure template for ClusterClass |

The [usage guide](docs/usage.md#api-reference) describes their fields and validation
rules. [Generated schemas](api/config/crd/bases/) and [Go API types](api/v1beta2/)
are the authoritative API definitions.

Start a hosted control plane with `CompositeCluster.spec: {}`. Attach providers
only through namespaced `CompositeProvider` resources:

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: CompositeProvider
metadata:
  name: outscale-main
  namespace: demo
spec:
  clusterRef: {name: demo-infra}
  templateRef: {name: outscale-account-a-r1}
  infraSpec:
    network:
      clusterName: demo
      internetService:
        name: demo
      net:
        ipRange: 10.42.0.0/16
```

The selected template supplies native configuration and credential references.
CAPI and native providers manage workers; Composite never creates them. See the
[single-cloud example](local/compositecluster-sample.yaml),
[spanning example](local/compositecluster-spanning-sample.yaml) and
[usage guide](docs/usage.md#create-a-cluster-attachment).

## Current boundaries

Attachments are append-only. Each native GroupKind retains one canonical child
named after the CompositeCluster. Additional profile-backed regions can select
distinct immutable `infraClusterName` values; see
[regional infrastructure](docs/usage.md#same-provider-regional-infrastructure).
Worker placement remains in native MachineTemplates and depends on the provider's
regional configuration capabilities. Composite does not create MachineDeployments,
replace cloud SDKs or provide cross-cloud networking/storage.

This is a development starting point. Tests cover API admission, ownership,
recovery, policy and operator behavior; native worker integration must be verified
for the selected provider/control-plane stack. Candidate versions do not imply
published images or production support.

## Docker-provider quickstart

Test Composite with the real Cluster API Docker provider (CAPD), not cloud VMs.
With Docker, kind, kubectl, clusterctl and Helm installed, run from this checkout:

```sh
make quickstart          # Build/install Composite; create one control plane + one worker
make quickstart-status   # Inspect the provider, native resources and workload nodes
make quickstart-down     # Delete the workload safely, then its kind management cluster
```

No host Go, registry push or cloud credentials are needed. CAPD gets access to the
Docker socket, so use a trusted, disposable local environment. The quickstart uses
an isolated kubeconfig and explicitly publishes CAPD's discovered endpoint on
CompositeCluster. See [prerequisites, verification and cleanup](docs/development.md#docker-provider-quickstart).

## Start here

- [Usage](docs/usage.md): profiles, clusters, overrides, workers and cleanup.
- [Operations](docs/operations.md): Helm/clusterctl installation, metrics, upgrades and moves.
- [Design](docs/design.md): practical decisions and lifecycle constraints.
- [Project foundations](docs/architecture/decisions/0001-project-foundation.md): rationale and bootstrap decisions.
- [CNCF/SIG conventions](docs/architecture/decisions/0002-cncf-sigs-conventions.md): ecosystem, contribution and distribution conventions.
- [Development](docs/development.md): tooling, tests, generation and local artifacts.
- [Helm values](helm/README.md).

## Build, publish and install

GitHub automation separates CI, security checks and gated release publication.
See [CI and checks](docs/development.md#github-automation) and
[release setup](docs/operations.md#automated-releases) for App secrets, required
checks, GHCR permissions and the coupled image/chart/clusterctl release version.
Prepare a reviewed draft, then push its annotated version tag to start the gated
pipeline. Assets stay in the draft until final publication verifies immutability;
ordinary PR checks never publish.

For CAPI-managed installation after a successful release, use the checked-in
[clusterctl provider config](clusterctl.yaml):

```sh
KUBECONFIG=/path/to/management.yaml
clusterctl init --infrastructure composite:v0.1.0 \
  --config ./clusterctl.yaml --kubeconfig "$KUBECONFIG"
```

This installs the Composite controller/CRDs and records clusterctl provider
inventory; it does not create native workers or configure cloud credentials.
Composite is currently a custom provider, not an upstream built-in: keep `--config`
and follow the [clusterctl prerequisites and local preview](docs/operations.md#clusterctl).
Do not also install the same controller with Helm.

For a manually built image, from the repository root choose a registry/repository
you can push to and the management nodes' architecture. This example builds a commit-tagged `linux/amd64`
image; use `linux/arm64` for ARM nodes.

```sh
REGISTRY=registry.example.com
IMAGE_REPOSITORY="$REGISTRY/team/cluster-api-provider-composite"
VERSION="v0.1.0-dev.git$(git rev-parse --short=12 HEAD)"
IMAGE="$IMAGE_REPOSITORY:$VERSION"

docker login "$REGISTRY"
make docker-build PLATFORM=linux/amd64 VERSION="$VERSION" IMAGE="$IMAGE"
docker push "$IMAGE"
```

Install compatible CAPI, kubeadm bootstrap and the intended native/control-plane
providers on the management cluster. For a fresh Composite installation:

```sh
KUBECONFIG=/path/to/management.yaml
kubectl --kubeconfig "$KUBECONFIG" apply -f api/config/crd/bases
helm upgrade --install composite ./helm \
  --kubeconfig "$KUBECONFIG" --namespace composite-system --create-namespace \
  --set-string image.repository="$IMAGE_REPOSITORY" \
  --set-string image.tag="$VERSION" --wait --timeout 5m
kubectl --kubeconfig "$KUBECONFIG" rollout status \
  deployment/composite -n composite-system --timeout=5m
```

Private registries also need a pull Secret in the operator namespace, referenced
through `imagePullSecrets`. Existing installations should follow the
[upgrade instructions](docs/operations.md#upgrade-and-uninstall), including matching
CRDs and RBAC. [Operations](docs/operations.md#crds-only) also covers CRD-only
installation; [development](docs/development.md#build-and-publish-an-image) covers
image digests and multi-architecture builds.

## Develop

Use the local tool versions in [hack/tools.env](hack/tools.env), as described in
[development](docs/development.md#prerequisites). The container builder has its
own Go image pin in [build/operator.Dockerfile](build/operator.Dockerfile).

```sh
make envtest-download
make verify
make verify-hygiene
```

Verification includes both modules, race-enabled tests, API lint, generated
consistency, packaging and vulnerability scanning. Docker is required for image
builds; the build and push steps above are separate from the verification targets.

## Contribute

See [CONTRIBUTING](CONTRIBUTING.md), [maintainers](MAINTAINERS.md) and the
[Code of Conduct](CODE_OF_CONDUCT.md). Report vulnerabilities privately using
[SECURITY.md](SECURITY.md).

Licensed under [Apache-2.0](LICENSE).
