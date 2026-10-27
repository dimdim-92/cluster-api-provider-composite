# Development

## Prerequisites

Install Go **1.26.10**, golangci-lint **2.12.2** and Helm **4.2.0**. The module
toolchain directives select the Go patch version automatically when supported.
[hack/tools.env](../hack/tools.env) is the source of truth for local verification
and generation tools, including controller-gen **v0.22.0**. Both Go modules require
Go 1.26.0 or newer and select the pinned local toolchain. Container builds use the
separate digest-pinned Go **1.27.3-alpine** image in
[build/operator.Dockerfile](../build/operator.Dockerfile).
Git, Bash and jq are needed for the offline release-policy tests; public network
access is needed for tools/modules and vulnerability data. Docker is required for
image builds; kind/kubectl/clusterctl are optional for local installation work.

```sh
make envtest-download
make verify
make verify-hygiene
```

`make verify` checks tools, Go headers, API dependency isolation, generated output,
contract labels, both-module lint, API lint, race-enabled tests, packaging renders
and vulnerabilities. `verify-hygiene` checks workflow syntax and scans deliverable
source for secrets/private endpoints. GitHub Actions splits these checks into CI
and Security workflows rather than hiding test results behind a vulnerability scan.

## GitHub automation

The CI layout follows Numspot's longue-vue repository, adapted to one operator,
the separate API module and native provider packaging. Release publication uses
reusable verification followed by promotion of the same tested artifacts. There
is no UI bundle, deployment step or private workflow/chart dependency.

| Workflow | Purpose |
|---|---|
| [CI](../.github/workflows/verify.yml) | Independent Go, Helm/Kustomize and Docker jobs; source/API/generated checks, vet, build, race/envtest tests, coverage and restricted image smoke tests; reusable for releases |
| [Commit Lint](../.github/workflows/commit-lint.yml) | Conventional Commits and PR titles, including the eventual squash subject |
| [Security](../.github/workflows/security.yml) | Both-module govulncheck/gosec, source hygiene and Trivy filesystem/IaC/image scans; weekly and manually dispatchable as well as PR/main runs |
| [release-please](../.github/workflows/release-please.yml) | Maintains an alpha release PR from Conventional Commits on main once GitHub App credentials are configured |
| [Release](../.github/workflows/release.yml) | On published GitHub Releases, validates immutable tag/commit identity and series, reuses CI/Security gates, then promotes tested images and shared digest-pinned installation assets |

Actions are pinned to immutable SHAs. The shared [tool setup](../.github/actions/setup-tools/action.yml)
reads [tool pins](../hack/tools.env); Go caching covers both module checksums.
CI checks generated output in scratch space and never repairs the working source.
`make test COVERPROFILE=coverage.out` writes separate root/API coverage profiles,
which CI uploads even after a test failure. Docker jobs use the existing operator
Dockerfile, not longue-vue's frontend/database build.

The release caller supplies an exact `release-version`. Reusable CI builds and
smoke-tests amd64/arm64 images with that version, then retains image archives,
commit/version records and image IDs. Reusable Security validates and scans those
same images. Publication validates their identities again and assembles the
multiarch index without a rebuild. One final digest-pinned chart archive feeds
both GitHub and OCI; `make helm-package VERSION=... IMAGE=...` stages image defaults
in scratch, without modifying source values or CRDs. Offline tests in
[hack](../hack/) cover event/ref gates, artifact identity and packaging behavior.

Security scan findings are not suppressed: govulncheck and fixable high/critical
image findings fail their jobs. Gosec and filesystem/IaC findings are advisory
reports, matching the upstream workflow. SARIF is always retained as workflow
artifacts when available. The [SARIF normalizer](../hack/normalize-sarif/) rebases
API-module source locations to repository-relative `api/...` paths, preserving
report fields and exact JSON numbers so Code Scanning links/fingerprints work. Code Scanning upload is opt-in through
`ENABLE_CODE_SCANNING=true`, requires repository support, and runs only for standalone
trusted non-PR events. Reusable release scans retain per-architecture reports without
Code Scanning uploads. No release App credentials are exposed to pull requests. Secret
scanning uses the existing pinned gitleaks CLI and needs no action license.

See [release setup](operations.md#automated-releases) before enabling publishing.
A workflow definition does not prove a GitHub run or a publication has succeeded.
Update required branch checks when replacing the former single verification job.

## Working loop

```sh
make test TEST=TestProfileAttachments   # root selection; API tests also run
make test TEST=TestOperatorRuntime     # actual operator against isolated envtest
make lint
make fmt                              # explicit source formatting
make gen                              # after changing generated inputs
make verify-generated                 # compare against scratch generation
make verify-vulnerabilities
```

Use failing regression tests before behavioral fixes. Prefer table-driven Go
tests and existing helpers. Test ownership/retry/error paths as well as success.
Envtest covers API admission and reconciliation, and runtime tests execute the
operator under its shipped RBAC. Runtime checks wait for current-generation Ready
observations on the provider and CompositeCluster independently; one resource's
readiness or latched initialization does not establish the other's current health.
These tests do not inherit workstation kubeconfigs or provision cloud VMs. Real
native worker/network/storage integration is separate.

Direct root `go test ./...` needs `KUBEBUILDER_ASSETS`; `make test` resolves installed
assets. `SKIP_ENVTEST=1` is a local-only shortcut, rejected in CI. Run API-module
checks separately when using Go commands directly.

## Layout and conventions

| Path | Responsibility |
|---|---|
| `api/` | Standalone CRD types and API machinery |
| `internal/controller/` | Reconciliation, profile planning, bindings, status and deletion |
| `internal/capi/` | Native contracts, ownership-safe writes and status reading |
| `internal/mint/` | Pure native/bootstrap rendering and merge rules |
| `internal/profile/` | Exact JSON handling and allowed/protected overrides over resolved template payloads |
| `configuration/`, `cmd/operator/` | Flags/environment and manager wiring |
| `config/`, `helm/` | clusterctl/Kustomize and Helm installation inputs |
| `tests/envtest/` | API-server, lifecycle and actual-operator regressions |

Keep `api/` free of CAPI, controller-runtime and generator/linter dependencies.
Use straightforward Go, errors with preserved causes, and safe diagnostic categories.
Current [design decisions](design.md) describe the invariants that tests must retain.

First-party Go files use the Apache-2.0 header with holder **Numspot**, from
[hack/boilerplate.go.txt](../hack/boilerplate.go.txt). Preserve third-party notices.
`make headers-fix` is an explicit helper; generated files get headers through their
generator. YAML/Markdown/shell files are covered by the repository LICENSE.

Generation produces API deepcopy code, all four `v1beta2` CRDs, contract labels,
RBAC and Helm fragments from shared inputs:

1. Edit types/markers in `api/v1beta2/`, controller RBAC markers, or the generator
   pin in `hack/tools.env`.
2. Run `make gen`. Canonical schemas go to `api/config/crd/bases/`; generation
   copies them into `helm/crds/` and refreshes the shared RBAC output.
3. Run `make verify-generated` and update the [API reference](usage.md#api-reference),
   relevant guides, and examples for any field, validation or lifecycle change.

The generated resources are `CompositeCluster`, `CompositeClusterTemplate` and
`CompositeProvider` (namespaced), plus `CompositeProviderTemplate` (cluster-scoped).
Only the cluster and its template advertise
the v1beta2 InfraCluster contract. API module dependency versions are distinct from
the CRDs' served API version and from the workload Kubernetes version.

Include all generated output in changes. Verification regenerates in scratch
space and does not repair working files; do not run generation first in CI to hide
stale output. Historical cluster provider-bindings formats are not a supported
migration path; use fresh resource-based attachments.

Named infrastructure regressions cover same-kind regions, canonical lookup,
immutable names, source UID/generation drift, status recovery, ownership rejection
and source-independent teardown. Current attachment/source pins and name rules
remain covered without silently rebinding recreated providers or templates.

`make test TEST=TestManagedService` checks the static
[managed-service examples](usage.md#managed-service-manifests). Its fixtures validate hosted-control-plane-only topology patches, empty CompositeCluster specs,
separate CompositeProvider references, effective override policy and deterministic
native/bootstrap naming. Provider selection is not a topology variable. The private MSC module is not a dependency
of either Go module; render MSClass expressions with the intended MSC version
before a live integration trial.

Node-pool requests are ManagedServiceSupplementalAddon CRs referencing the two
reusable pool MSClasses. Each renders native worker resources and reuses Composite's
generated bootstrap templates. Both cluster instances start without provider
attachments; apply separate providers or supplemental provider addons afterward.
Local examples have no Kustomization bundles; the usage guide lists explicit inputs.

`make test TEST=TestProviderAddon` covers namespaced attachments, independent
empty-cluster readiness, credential injection/protection, source drift, reference
immutability, identity collisions, shared worker consumers and ordered teardown.
`make test TEST=TestProviderStatus` covers the CompositeCluster provider summary,
exact per-provider native MachineDeployment membership, independent cluster
readiness, stable ordering, pool removal and draining-provider visibility. Unit
regressions also cover paginated direct reads, partial-read failures, stale
conditions, status capacity and replacement-UID isolation; API tests cover node-pool
references and deepcopy independence. Teardown regressions keep inventory current
while native finalizers hold deletion. Workers remain read-only observations.

`TestOperatorRuntime/provider-addons` runs the actual operator under generated RBAC
and namespace/filter scope, including pool create/delete watch events. The
`local/managed-service/provider-addons/` classes let
MSC expand native worker addons; no MSC dependency or Composite worker controller
is introduced.

`make test TEST=TestCredentialsRef` covers native credential-name injection,
protected overrides, endpoint/path conflicts, source isolation, credential-field
admission and immutable updates/patches. Secret contents are never inputs to
Composite. API-module tests also cover credential-reference deepcopy and JSON
round trips. Credential selection preserves template/source UID pins.

`make test TEST=TestEmptyComposition` exercises empty infrastructure readiness
without an endpoint or native children, stable status, ownership/pause gates,
empty template admission, initialization latching and teardown. Provider coverage
uses absent cluster provider configuration and independent CompositeProvider
creation, failure/recovery and retirement. Historical cluster provider-bindings records and old configuration are not
supported attachment paths.

## Build and publish an image

Build for the management nodes' architecture, not necessarily the workstation's.
For a single-platform image, from the repository root:

```sh
REGISTRY=registry.example.com
IMAGE_REPOSITORY="$REGISTRY/team/cluster-api-provider-composite"
VERSION="v0.1.0-dev.git$(git rev-parse --short=12 HEAD)"
IMAGE="$IMAGE_REPOSITORY:$VERSION"

docker login "$REGISTRY"
make docker-build PLATFORM=linux/amd64 VERSION="$VERSION" IMAGE="$IMAGE"
docker run --rm --platform linux/amd64 --network=none "$IMAGE" --version
docker push "$IMAGE"
docker buildx imagetools inspect "$IMAGE"
```

`make docker-build` embeds `VERSION` in the binary. Record the registry-reported
digest for deployment through Helm's `image.digest` value. Registry login
authenticates the workstation; Kubernetes needs its own read-only pull credentials
in the operator namespace when the image is private.

To publish an image for both supported architectures and record the source revision:

```sh
docker buildx build --platform linux/amd64,linux/arm64 \
  --file build/operator.Dockerfile \
  --build-arg "version=$VERSION" --build-arg "revision=$(git rev-parse HEAD)" \
  --tag "$IMAGE" --push .
```

Use a builder configured for the selected platforms. `make docker-build-multiarch`
instead writes `out/composite-image.tar`; it is a local archive target, not the
registry publication step. Follow [operations](operations.md#install) to install
the image with CRDs and RBAC from the same source revision.

## Local artifacts

```sh
make build
make docker-build PLATFORM=linux/arm64 IMAGE=composite:dev
make docker-build-multiarch IMAGE=composite:dev
make release-manifests VERSION="$VERSION" IMAGE="$IMAGE"
make helm-package VERSION=v0.1.0-alpha.1
```

Images use pinned public builder/runtime bases and run non-root. These commands
produce local artifacts and do not push or tag Git. `out/`, `.tools/`, caches and
private local material are excluded from source delivery. See
[operations](operations.md) for using artifacts on a management cluster.

Keep documentation focused on current behavior, usable commands and important
constraints. Explain changes in their PR and update the relevant guide; historical
plans and review reports are not part of the starting-point documentation.
