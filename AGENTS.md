# Agent Guide

Read [design](docs/design.md) and [development](docs/development.md). Keep changes
focused and pragmatic; explain current behavior in the four guides under `docs/`.
Keep ADR-0001 for project foundations and ADR-0002 for CNCF/SIG conventions under
`docs/architecture/decisions/`, without rebuilding historical phase plans or
completed audit archives.

## Code boundaries

- `api/` is a separate consumer module: no CAPI, controller-runtime or tool dependencies.
- `internal/controller/` owns plans, bindings, status, catalog protection and teardown.
- `internal/capi/` owns native contract/ownership operations; `internal/mint/` is pure rendering.
- `internal/profile/` owns exact JSON handling and allowed/protected overrides.
- `configuration/` and `cmd/operator/` wire environment-defaulted flags and the manager.
- `config/` and `helm/` share generated CRDs/RBAC. Tests belong alongside code or in `tests/envtest/`.

## Invariants

- Keep native workers native; do not add cloud SDK translators or a worker controller.
- Keep one canonical InfraCluster per GroupKind named after the infrastructure
  reference. Additional profile-backed children use immutable explicit names.
  Cluster labels use the actual owner name; names/labels do not authorize adoption.
- Retain dual ownership, direct ownership-sensitive reads and UID/resourceVersion guards.
- Configure attachments only through CompositeProvider resources, not CompositeCluster.
  GroupKind/name identities are distinct; templateRef, clusterRef and infraClusterName
  are immutable. Preserve current attachment/source pins and safe retirement/teardown.
  Historical cluster provider-bindings formats are unsupported.
- Profiles reference native templates. Never mutate/own source objects or copy their
  metadata into outputs. Pin profile UID and source UID/generation, not content hashes;
  do not silently rebind recreated sources or convert legacy hash-based pins.
- Validate the complete binding record before metadata/child writes. Persist initial
  bindings and finalizer together, before the normal status snapshot.
- Delete infrastructure, wait for confirmed absence, then delete bootstrap templates.
  An unavailable API does not prove deletion. Teardown must not depend on a live profile.
- Catalog protection reads references globally, independently of consumer watch scope.
- Initialization is latched; Ready reflects current observations and their generation.
  Preserve legacy status shape and avoid false provider history on first failure.
- Keep exact JSON numbers, bootstrap merge semantics and protected endpoint paths.
- Preserve scoped/pause behavior, 30-second settling and five-minute native-child polling.

## Working rules

- Use straightforward Go and test meaningful behavior/failure paths before fixing it.
- Preserve error identity; publish bounded safe diagnostics, not native payload/error dumps.
  Keep verbosity within 0–4 and state Events after successful writes.
- Follow applicable CNCF > Cluster API > Numspot > kubernetes-sigs > Go conventions.
- Keep probe/metrics settings consistent across code and installation templates.
- Use the full `Copyright 2026 Numspot.` Apache-2.0 Go header; preserve foreign notices.
- Regenerate after changing generation inputs; include all resulting artifacts.

```sh
make fmt
make gen                 # only when generated inputs change
make verify
make verify-hygiene
```

Verification must not repair working source. Documentation-only edits need factual,
link and whitespace checks, not new behavioral tests. Preserve unrelated work;
commit, push, publish or change deployment environments only when requested.
