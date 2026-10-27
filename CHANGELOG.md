# Changelog

## [0.1.0](https://github.com/numspot/cluster-api-provider-composite/releases/tag/v0.1.0) (2026-10-27)

### Features

- Coordinate native infrastructure clusters and kubeadm bootstrap templates through
  the Cluster API `v1beta2` contract, without introducing a worker controller or
  cloud SDK translation layer.
- Provide reusable `CompositeProviderTemplate` profiles, namespaced
  `CompositeProvider` attachments, exact JSON override policies, immutable source
  identity pins, and ownership-safe reconciliation and teardown.
- Support empty compositions, canonical and additional regional infrastructure,
  credential-name injection, and read-only native worker-pool observations.

### Distribution

- Release matching Linux amd64/arm64 operator images, Linux/macOS amd64/arm64
  binaries and checksums, a Helm chart, and clusterctl installation assets.
- Gate publication on source/API/generated checks, race/envtest tests, packaging
  verification and security scans; promote the exact tested image archives and
  share one digest-pinned chart between GitHub and OCI publication.
- Stage the complete GitHub asset inventory in a reviewed draft before final
  protected publication verifies release and tag immutability.
- Provide custom clusterctl registration and concrete installation defaults, with
  real provider-generation and isolated API installation/inventory regressions.

### Compatibility and limitations

- All four Composite CRDs serve/store
  `infrastructure.cluster.x-k8s.io/v1beta2`; provider release series `0.1` declares
  the Cluster API `v1beta2` contract.
- This is an initial pre-1.0 release, not a guarantee of API stability or production
  support. Native worker, control-plane, network and storage compatibility must be
  verified for the intended provider stack.
- Historical cluster provider-binding formats and transparent source/profile
  rebinding are unsupported. Composite does not provision or manage native workers.
