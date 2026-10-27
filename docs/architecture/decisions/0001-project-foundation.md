# ADR-0001: Project purpose and initial foundation

**Date:** 2026-09-14

**Status:** Implemented; experimental pre-1.0 provider

## Why this project exists

A Cluster API Cluster has one `infrastructureRef`. The original Numspot use case
needed one workload cluster with infrastructure from multiple native providers,
initially Outscale and OpenStack, behind a hosted control plane such as Kamaji.

The native providers already implement networks, machines and cloud lifecycle.
Reimplementing those APIs in another provider would duplicate their responsibilities
and couple this project to every cloud's configuration model.

Composite supplies the missing composition layer: CAPI references one
`CompositeCluster`, which coordinates several native InfraClusters and their
bootstrap templates. Reusable profiles keep account/region defaults out of every
cluster manifest. Callers can use ordinary CAPI objects without depending on
Numspot-specific orchestration.

## Critical decisions

| Decision | Reason and consequence |
|---|---|
| Compose infrastructure; delegate cloud and worker lifecycle | Native providers own cloud API calls. CAPI and caller orchestration own MachineDeployments, rollout and remediation. No cloud SDK translation layer or Composite worker controller. |
| Separate shared configuration from resource ownership | Cluster-scoped profiles select native InfraClusterTemplate and KubeadmConfigTemplate sources. Payloads live in those templates; profiles contain references and override policy. Consumers own independent generated resources, never the shared sources. |
| Use immutable profile revisions and explicit overrides | A profile change creates a new named revision. Consumers may change only permitted effective settings; protected identity and endpoint fields remain controlled. Native credential references are stored instead of secret snapshots. |
| Keep native payloads opaque | The installed native CRD validates provider-specific fields. Composite handles policy, exact JSON values, endpoint propagation and bootstrap composition rather than normalizing every cloud model. |
| Preserve canonical lookup while allowing explicit regional identities | Each native GroupKind retains a child named after the CAPI infrastructure reference. Additional regions use immutable explicit native names. Native worker templates must supply supported regional overrides; Composite does not select or reconcile workers. |
| Preserve identity before creating resources | V4 bindings record native identities, profile UID and source-template UID/generation with the initial finalizer. Defer custom content hashing for the alpha: source replacement/spec edits block existing consumers, and identical-content restore/move is not automatically accepted. |
| Use dual ownership and explicit deletion order | Children carry Composite controller ownership plus a non-controller CAPI Cluster reference. UID/resourceVersion guards protect mutations. Delete native infrastructure before bootstrap templates; missing APIs are not proof of deletion. |
| Separate initialization from current health | Initialization stays latched after success; conditions track current observations and generation. Status is reconstructable and does not replace durable identity. |
| Keep the API consumer module lightweight | `api/` contains CRD types and required API machinery, without CAPI/controller-runtime/tool dependencies. Composite serves v1beta2; native versions are resolved independently from their CRD contracts. |
| Expose purpose-specific CRDs | Namespaced CompositeCluster owns runtime infrastructure; CompositeClusterTemplate supplies immutable topology input; cluster-scoped CompositeProviderTemplate selects sources and policy; namespaced CompositeProvider binds an MSC addon to a cluster and namespace-local credential reference. Cluster, provider and catalog expose status subresources. |
| Share installation inputs | Helm and clusterctl/Kustomize use the same generated CRDs and RBAC. Differences in installer ownership, CRD upgrades and clusterctl inventory remain explicit. |

Profile administration and consumer writes are trusted operations. Any namespace
can reference a catalog profile, and bootstrap can execute commands on nodes.
An immutable profile does not freeze the contents of a native credential Secret.
Global reference checks retain profiles in use; newly created unresolved references
can race deletion and must fail closed rather than authorize provisioning.

## How the starting point was bootstrapped

1. **Start from the native composition use case.** The initial design targeted
   Outscale/OpenStack infrastructure under a hosted control plane. Native source
   inspection and local API/owner-lookup tests established the need for name
   alignment and dual ownership rather than another worker abstraction.
2. **Build a focused Go operator.** `controller-runtime` supplies manager, watches,
   workqueues and leadership. Pure rendering/profile logic is separate from
   native contract/ownership operations and reconciliation.
3. **Define APIs and generate installation artifacts.** Go API types and
   `controller-gen` produce deepcopy code, CRDs and RBAC. Explicit Make targets
   stamp contract labels and generate the shared Helm fragments. The API module
   remains independently importable.
4. **Harden lifecycle and reusable configuration.** Add ownership/precondition
   regressions, durable bindings, immutable profile admission, override policy and
   reconstruction after status loss. Keep diagnostic output bounded and metrics
   authenticated by default.
5. **Make verification reproducible.** Pin tools and public image bases; run
   race-enabled unit/envtest tests, actual-operator tests under shipped RBAC,
   generation/API checks, vulnerability scanning and source hygiene. Local kind
   rehearsals exercised both installers and a prepared resource-graph move using
   test-only provider state, not native cloud workloads.
6. **Prepare a clean GitHub starting point.** Deliver source, tests, examples,
   generated manifests and essential documentation as fresh repository content.
   Do not carry development Git history, local credentials/caches or historical
   phase/audit archives into that starting point. Keep Numspot ownership,
   `github.com/numspot/cluster-api-provider-composite` module paths and Apache-2.0.

The initial toolchain, dependencies and commands live in
[development](../../development.md) and [hack/tools.env](../../../hack/tools.env), rather
than being duplicated as a second set of pins in this record.

## Limits of this initial decision

- This provides infrastructure composition, not cross-cloud networking, storage,
  node initialization or arbitrary control-plane compatibility.
- Named regional infrastructure does not alter native worker lookup or guarantee
  that every native provider supports regional machine overrides or auxiliary resources.
  Automatic inline/profile migration and arbitrary profile revision changes remain unsupported.
- Bootstrap templates are updated in place; changing them does not by itself roll
  existing native workers. Pool-specific templates remain the caller's choice.
- Template-backed profiles do not automatically accept moved/restored source objects
  with new UIDs. Attachments pin source identity; historical cluster provider
  bindings are unsupported and require the old controller for teardown.
- Local API/schema/operator verification is not evidence that a native workload
  joins successfully or that every provider upgrade/move is safe.

This ADR records the project foundation; [ADR-0002](0002-cncf-sigs-conventions.md)
records CNCF/SIG and contribution conventions. Keep detailed current behavior in
[design](../../design.md), runnable inputs in [local examples](../../../local/), and
operational steps in [usage](../../usage.md) and [operations](../../operations.md).
