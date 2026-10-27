# ADR-0002: CNCF and Kubernetes SIG conventions

## Status

Proposed

## Context

Composite is a Numspot-owned Cluster API infrastructure provider with public Go
module paths under `github.com/numspot/cluster-api-provider-composite`.
[ADR-0001](0001-project-foundation.md) records the composition model and project
foundation. This ADR records the ecosystem and contribution conventions used to
make that implementation installable, reviewable and maintainable.

Following CNCF and Cluster API conventions is useful independently of repository
donation. Formal CNCF affiliation or transfer to `kubernetes-sigs` would require a
separate governance decision and acceptance by the relevant organizations.

## Decision drivers

- Keep the public APIs and installation artifacts compatible with Cluster API.
- Make contributions and verification reproducible with public tooling.
- Keep ownership, governance and security reporting explicit.
- Document current behavior and distinguish local verification from native-stack
  integration evidence.

## Considered options

1. Defer ecosystem conventions until external adoption: less initial work, but
   installation and contribution drift would accumulate.
2. Transfer ownership to `kubernetes-sigs`: requires sponsorship, governance and
   infrastructure decisions beyond this provider implementation.
3. Follow applicable conventions under `github.com/numspot`: usable public APIs,
   packaging and contribution rules while retaining Numspot ownership. This is
   the chosen approach.

## Decision

Follow applicable CNCF, Cluster API, Numspot, kubernetes-sigs and Go conventions,
as summarized in [AGENTS.md](../../../AGENTS.md). Keep the project under
`github.com/numspot` and revisit donation through a separate reviewed decision.

### API and controller conventions

- Use the `infrastructure.cluster.x-k8s.io` API group and generic Composite kind
  names. The current schemas serve/store `v1beta2`.
- Publish namespaced `CompositeCluster`, `CompositeClusterTemplate` and
  `CompositeProvider`, plus the cluster-scoped `CompositeProviderTemplate` catalog.
  Only the cluster and its template
  advertise the v1beta2 InfraCluster contract; profiles carry provider identity.
- Use Kubernetes status subresources, generation-aware `metav1.Condition` values,
  structural schemas and CEL validation. Preserve optional field presence,
  ownership, durable bindings and documented immutability.
- Keep `api/` independently consumable, with API machinery dependencies and no
  CAPI/controller-runtime/generator dependency tree.
- Generate deepcopy code, CRDs and RBAC from shared inputs. Keep full
  `Copyright 2026 Numspot.` Apache-2.0 headers and preserve foreign notices.

Current fields, status semantics and validation limits live in
[usage](../../usage.md#api-reference) and [design](../../design.md), rather than
being duplicated as a frozen API specification in this ADR.

### Public distribution

- The standalone [Helm chart](../../../helm/README.md) has no private library-chart
  dependency. Helm and [clusterctl/Kustomize](../../operations.md#clusterctl) share
  the four canonical CRDs and generated manager permissions.
- `make release-manifests` produces provider components and local clusterctl
  registration inputs. The repository's `metadata.yaml` describes the provider
  contract. Publishing source or selecting a candidate version does not publish
  a controller image or register the provider in upstream distribution.
- Use public, pinned build tools and base images. Build/push commands and registry
  digest selection are documented in [development](../../development.md).
- Keep private endpoints, kubeconfigs, credentials and local caches out of the
  public source and build context.

### Contributions and governance

The community documents follow the concerns covered by the
[CNCF project template](https://github.com/cncf/project-template):
[license](../../../LICENSE), [contributing](../../../CONTRIBUTING.md),
[maintainers](../../../MAINTAINERS.md), [governance](../../../GOVERNANCE.md),
[Code of Conduct](../../../CODE_OF_CONDUCT.md) and [security](../../../SECURITY.md).

Maintainers review changes and prefer consensus under the published governance
document. Contributions use descriptive Conventional Commit messages and DCO
sign-offs. Security and conduct reports use the private maintainer route described
in those policies. Adopting the CNCF behavioral standard does not establish CNCF
membership or a CNCF reporting channel for this project.

[`.github/`](../../../.github/) contains a bug-report form, PR template, workflow
and CODEOWNERS placeholder. Add ownership patterns only for confirmed GitHub
identities; the current file makes no automatic reviewer assignments. Prow and
Kubernetes-org ownership rules are not prerequisites for this Numspot repository.

### Verification

The [CI workflow](../../../.github/workflows/verify.yml) follows the longue-vue
workflow layout with independent Go, Helm and Docker checks on PRs, main pushes
and manual dispatch. It preserves both-module checks, scratch generated-output
verification, race/envtest coverage and the local image smoke test. Separate
[commit checks](../../../.github/workflows/commit-lint.yml) and
[Security scans](../../../.github/workflows/security.yml) retain read-only PR trust
boundaries and immutable action/tool pins.

[Release-please](../../../.github/workflows/release-please.yml) maintains stable
release PRs using a scoped GitHub App token. The separate
[draft-first workflow](../../../.github/workflows/release.yml) validates the
annotated tag/source, main ancestry and CAPI release-series support, reuses
CI/Security, and stages matching images, chart, binaries and clusterctl assets.
The reviewed draft is published only after all uploads, with immutability verified. Only trusted
release automation and the opt-in Code Scanning upload job receive write
credentials; PR checks remain read-only. No workflow
deploys a cluster. [Operations](../../operations.md#automated-releases) records
required secrets, repository settings and partial-publication caveats.

Record the commands actually run and their outcomes in each PR. Local API-server
and simulated lifecycle results are distinct from native-provider, worker and
cross-cloud integration evidence. [Development](../../development.md) defines the
current verification entry points and tool pins.

## Consequences

- Consumers can use ordinary Cluster API resources, public module paths and either
  documented installer.
- Maintainers retain ownership and must keep contribution, release and security
  policies aligned with actual project practice.
- Two installation paths require shared generation and render checks to prevent
  schema/RBAC drift.
- ADR-0001 remains the project-foundation record; this ADR remains the CNCF/SIG
  conventions record. Current behavior and operational commands belong in the
  four guides, without reintroducing historical milestone plans.

## References

- [Cluster API contributing guide](https://cluster-api.sigs.k8s.io/contributing)
- [clusterctl provider contract](https://cluster-api.sigs.k8s.io/developer/providers/contracts/clusterctl)
- [Cluster API provider list](https://cluster-api.sigs.k8s.io/reference/providers)
- [CNCF project template](https://github.com/cncf/project-template)
- [Developer Certificate of Origin](https://developercertificate.org/)
