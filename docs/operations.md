# Operations

Use an explicit management-cluster kubeconfig. Install compatible CAPI core,
kubeadm bootstrap, hosted-control-plane and native infrastructure providers first.
Composite uses v1beta2 KubeadmConfigTemplates and independently resolves native
contracts. Development tests use CAPI 1.12.4 and Kubernetes 1.34.1 envtest; actual
native worker/network/storage compatibility must be verified for your stack.

For Kamaji, include `CompositeCluster` in its
`--dynamic-infrastructure-clusters` setting and grant its controller permission
to publish the endpoint on CompositeCluster.

## Install

Choose one installer for each controller installation. Both use the same generated
CRDs/RBAC; neither installs native providers or cloud credentials. Supply an image
that exists in your registry or local kind nodes. Candidate tags in source do not
imply published images.

Build and publish from the source revision you are installing using the
[image workflow](development.md#build-and-publish-an-image). Match the image
platform to the management nodes and record the published digest. A workstation's
`docker login` does not configure Kubernetes image pulls: private images need a
registry pull Secret in the operator's namespace, supplied through your usual
secret-management process.

### CRDs only

The canonical directory contains exactly four CRDs:

| CRD | Scope | Served/storage version |
|---|---|---|
| `compositeclusters.infrastructure.cluster.x-k8s.io` | Namespaced | `v1beta2` |
| `compositeclustertemplates.infrastructure.cluster.x-k8s.io` | Namespaced | `v1beta2` |
| `compositeproviders.infrastructure.cluster.x-k8s.io` | Namespaced | `v1beta2` |
| `compositeprovidertemplates.infrastructure.cluster.x-k8s.io` | Cluster | `v1beta2` |

Use an explicit kubeconfig to validate and install them:

```sh
KUBECONFIG=/path/to/management.yaml
kubectl --kubeconfig "$KUBECONFIG" apply --dry-run=server -f api/config/crd/bases
kubectl --kubeconfig "$KUBECONFIG" apply -f api/config/crd/bases
kubectl --kubeconfig "$KUBECONFIG" wait --for=condition=Established --timeout=60s \
  crd/compositeclusters.infrastructure.cluster.x-k8s.io \
  crd/compositeclustertemplates.infrastructure.cluster.x-k8s.io \
  crd/compositeproviders.infrastructure.cluster.x-k8s.io \
  crd/compositeprovidertemplates.infrastructure.cluster.x-k8s.io
```

This registers or updates the APIs; install the operator separately to reconcile
their resources. Existing CompositeClusters can remain in place during a compatible
CRD update. See [upgrade and uninstall](#upgrade-and-uninstall) for schema-history
and controller/RBAC requirements.

### Helm

See [chart values](../helm/README.md). Set `image.repository` and `image.tag` to the
published image in your values file, or use `image.digest` to pin the exact image.
For a private registry, also set `imagePullSecrets` to an existing Secret in
`composite-system`. From the repository root:

```sh
KUBECONFIG=/path/to/management.yaml
helm upgrade --install composite ./helm --namespace composite-system \
  --kubeconfig "$KUBECONFIG" --create-namespace \
  --values /path/to/values.yaml --wait --timeout 5m
kubectl --kubeconfig "$KUBECONFIG" rollout status \
  deployment/composite -n composite-system --timeout=5m
```

For local images, build with `make docker-build IMAGE=composite:dev` using the
node's architecture, then `kind load docker-image composite:dev --name CLUSTER`.
Set the chart image repository/tag accordingly.

The default Deployment and ServiceAccount names match the release name
(`composite` above). Its ClusterRole/ClusterRoleBinding name is prefixed with the
release namespace. The metrics Service is `composite-metrics`.

### clusterctl

Set `VERSION` and `IMAGE` to the version and image published through the
[build workflow](development.md#build-and-publish-an-image), then generate a
custom local provider repository and registration file:

```sh
make release-manifests VERSION="$VERSION" IMAGE="$IMAGE"
clusterctl init --infrastructure "composite:$VERSION" \
  --config out/clusterctl.yaml --kubeconfig "$KUBECONFIG"
```

Raw [manager configuration](../config/manager/manager.yaml) contains
`${COMPOSITE_*}` placeholders; clusterctl must substitute them before application.
Provider templates declare native GroupKinds; no deployment mapping is needed.

## Automated releases

The [release workflows](../.github/workflows/release.yml) follow longue-vue's
release-please/GHCR/GoReleaser model. Composite has one coupled provider/chart
version rather than independent application and chart tags. Configure the following
repository settings before enabling them:

- Install a GitHub App on this repository with **Contents: read/write** and
  **Pull requests: read/write**. Store its ID as `RELEASE_PLEASE_APP_ID` and its PEM
  private key as `RELEASE_PLEASE_APP_PRIVATE_KEY` in Actions secrets. The scoped App
  token lets release PRs and tags trigger follow-up workflows; the default
  `GITHUB_TOKEN` would suppress those events. Do not reuse a developer's SSH/GPG key.
- Allow GitHub Actions and the required pinned actions. Permit the repository's
  `GITHUB_TOKEN` to publish its GHCR image and chart packages; existing packages may
  need to grant this repository Actions access. Publication jobs alone receive
  package/content write permissions.
- Require the new CI, commit-lint and applicable Security checks before merging.
  Protect `main` and release tags. Only tags whose commits are on main pass the
  release guard; unreviewed branch tags do not publish.
- Optionally enable Code Scanning and set the Actions repository variable
  `ENABLE_CODE_SCANNING` to `true`. Without it, SARIF reports remain downloadable
  workflow artifacts, including for PRs/forks. Dependabot configuration covers both
  Go modules and Actions; enable dependency/security updates in repository settings.

The [release manifest](../.release-please-manifest.json) starts from the source's
`0.1.0-alpha.2` candidate; this is not a claim that the candidate was published.
The [configuration](../release-please-config.json) starts commit discovery after
the initial repository commit and keeps alpha prereleases while the provider is
in development. Release PRs update the manifest/changelog, Make's default version,
and both Helm version fields together. To leave alpha, review the prerelease
settings explicitly rather than publishing an accidental stable release.

Review a release PR and verify the declared major/minor CAPI series in
[metadata.yaml](../metadata.yaml). Adding a new series requires an explicit contract
review; version automation does not silently claim support. A tag outside the
listed series fails validation before image/chart publication. Merge the release
PR only after the required checks pass. Release-please creates a `vX.Y.Z[-alpha.N]`
tag and release; the tag workflow then publishes:

- `ghcr.io/<owner>/cluster-api-provider-composite:<tag>` for Linux amd64/arm64,
  with and without the leading `v`. Prereleases do not overwrite `latest` or stable
  major/minor aliases.
- Linux/macOS amd64/arm64 binary archives and checksums through GoReleaser.
- The matching chart archive as a GitHub release asset and as
  `oci://ghcr.io/<owner>/charts/cluster-api-provider-composite` (chart versions omit `v`).
- `infrastructure-components.yaml` pinned to the published image digest and the
  matching `metadata.yaml` as root release assets for clusterctl. The generated
  local `out/clusterctl.yaml` contains an absolute path and is never published.

Image publication must succeed before the chart and release artifacts publish.
These are separate remote operations, not an atomic transaction; inspect every
job before advertising a release and investigate partial failures before retrying.
No workflow deploys or upgrades a Kubernetes cluster. Existing vulnerability
findings still require remediation; workflow reuse does not waive Security checks.

After a successful release, use its actual tag and package visibility. For example:

```sh
VERSION=vX.Y.Z-alpha.N  # Replace with an existing successful release.
helm upgrade --install composite \
  oci://ghcr.io/numspot/charts/cluster-api-provider-composite \
  --version "${VERSION#v}" --namespace composite-system \
  --kubeconfig "$KUBECONFIG" --create-namespace --values /path/to/values.yaml
```

Apply matching CRDs before upgrades as described under
[upgrade and uninstall](#upgrade-and-uninstall). For clusterctl, register the
release's actual download URL in your normal clusterctl configuration:

```yaml
providers:
  - name: composite
    type: InfrastructureProvider
    url: https://github.com/numspot/cluster-api-provider-composite/releases/latest/infrastructure-components.yaml
```

The URL above is clusterctl's version-aware provider repository URL, not proof of
an existing release. For alpha releases, select an actual published prerelease
explicitly with `--infrastructure composite:vX.Y.Z-alpha.N`.

## Managed-service integration

For provider addons, install all four generated CRDs and operator/RBAC from the
same source revision. Use an empty CompositeCluster, namespaced CompositeProviders
rendered by MSC supplemental addons, and native MachineDeployments rendered by MSC
child `addonDefinitions`. Install reusable CompositeProviderTemplates and native
sources before submitting addons. MSC needs namespaced provider write permissions;
Composite needs read-only worker/bootstrap provenance permissions for retirement,
not Secret read permissions.

Observe provider Ready and its generation separately from root InfrastructureReady.
Address template, credential-path and native failures on the provider/native
resources; CompositeCluster has no provider-selection field. See
[provider addons](usage.md#provider-addons) for manifests.

The [static managed-service bundle](usage.md#managed-service-manifests) separates
MSC definitions on its management cluster from ClusterClass, templates and profiles
on its configured service cluster. The latter hosts CAPI and Composite. Enable
CAPI's ClusterTopology feature and install the template CRDs before the ClusterClass.
The `composite-templates` namespace holds shared templates, not tenant resources.

Submit the MSI with an explicit empty provider selection to start its hosted
control plane before profiles. Empty Composite infrastructure becomes Ready
without an endpoint or native children. The MSC and ClusterClass variable schemas
must also permit zero entries. Provision profiles and credentials, then append
references through the MSI; merely creating a profile does not attach it.
Once attachments exist, their normal readiness gates apply.

MSC's service-cluster identity needs Cluster/MachineDeployment and native
MachineTemplate write permissions, plus read/list/watch for the teardown blocking
types. [The supplemental role](../local/managed-service/service-cluster/msc-rbac.yaml)
lists these rules; bind it to the actual target-cluster identity. Retain MSC's
normal namespace/watch permissions. CAPI needs permissions for the Composite and
Kamaji template/object APIs, and Kamaji needs CompositeCluster endpoint writes with
`CompositeCluster` in `--dynamic-infrastructure-clusters`. Apply these permissions
to the existing provider installations.

Keep provider credentials in each instance's target namespace throughout teardown.
The shared definitions use profile names as immutable revisions. A profile's
`ConfigurationValid` condition and schema/render checks do not establish working
regional cloud connectivity; verify the tenant API, each pool's join, and CNI/CSI
behavior against the intended native stack.

Profiles may select native Secret names with `credentialsRef.name` and explicit
`credentialsRef.namePaths`. These names resolve in the consuming namespace; shared
source templates must use namespace-local references. Composite requires no Secret
read permissions and does not test whether the Secret exists, contains the required
keys, or authenticates to the declared account/region. Check those through the
native provider. Rotate Secret contents using its supported process; selecting a
different Secret name or mapping requires a new immutable profile revision.

## Scope, diagnostics and metrics

### Verify the operator

After the rollout completes, inspect the selected pods and their image IDs:

```sh
kubectl --kubeconfig "$KUBECONFIG" get pods -n composite-system \
  -l app.kubernetes.io/instance=composite -o wide
kubectl --kubeconfig "$KUBECONFIG" logs deployment/composite \
  -n composite-system --tail=100
kubectl --kubeconfig "$KUBECONFIG" get cprov
kubectl --kubeconfig "$KUBECONFIG" get cocl,cct -A
```

`rollout status` checks the desired Deployment revision; an Available condition
can still describe a previous healthy replica during an incomplete rollout.
Operator probes are `/ping` and `/ready` on port 2112. They establish process/cache
health, not native infrastructure or worker readiness.

| Rollout symptom | Check |
|---|---|
| `ImagePullBackOff` / `ErrImagePull` | Image tag/digest, node architecture, registry reachability and same-namespace pull Secret |
| `FailedToRetrieveImagePullSecret` | The referenced Secret exists in the pod namespace and grants pull access to the image registry |
| `FailedCreatePodSandBox` / missing pod IP | Management-cluster CNI and node networking |
| Cache/list/watch forbidden | ServiceAccount binding and generated RBAC, including the global profile catalog |
| Probe failure | Listen addresses, probe paths/ports, configuration validation and operator logs |
| Operator healthy, CompositeCluster unready | The resource's conditions and referenced native children; see [usage](usage.md#observe-and-troubleshoot) |

### Informer startup

The operator always initializes informer caches with LIST followed by WATCH.
Streaming initial-event WatchList is disabled in the binary before Kubernetes
clients or caches are constructed, avoiding startup stalls waiting for the
initial-events-end bookmark. Normal watches, reconciliation, scope and provider
readiness semantics remain unchanged.

This policy applies to Helm, clusterctl and direct binary deployments. There is no
Helm toggle or environment opt-in: `KUBE_FEATURE_WatchListClient=true` cannot
re-enable it. Other client-go feature gates retain their defaults and environment
configuration. The policy requires an image built from this source; changing
installation manifests alone does not change an older binary's behavior.

A repeated `Could not wait for Cache to sync` followed by process exit indicates
an informer startup failure, not a requirement to add native providers to an empty
CompositeCluster. Verify `Starting workers` in the operator logs and current
CompositeCluster conditions; process probes and Deployment availability alone do
not establish successful cache synchronization. Increasing `--sync-period` changes
resync frequency, not the startup sync deadline.

### Configuration

| Setting | Default / behavior |
|---|---|
| `--namespace` / `WATCH_NAMESPACE` | Empty watches all namespaces; catalog protection always reads globally |
| `--watch-filter` / `WATCH_FILTER` | Empty selects all; otherwise matches CAPI's watch-filter label |
| `--leader-elect` / `LEADER_ELECTION` | Enabled; replicas coordinate with a Lease |
| `--leader-election-id` | `capi-composite.infrastructure.cluster.x-k8s.io` |
| `--leader-election-namespace` | Pod namespace by default; separate from watch namespace |
| Health | Port 2112, `/ping` and `/ready`; process health, not workload readiness |
| Metrics | Port 5555, TLS + authentication/authorization; HTTP/2 disabled |
| `--metrics-cert-dir` | Directory with `tls.crt`/`tls.key`; configure scraper trust |
| `--tls-min-version` | TLS 1.2 by default; 1.3 supported |

Explicit flags override environment values, including empty/false settings.
Install, watch and lease namespaces are distinct. A watch filter is not an RBAC
security boundary. Externally managed and paused resources are respected. Native
InfraClusters are polled every 30 seconds while settling and five minutes when
Ready; profile, provider, bootstrap and native MachineDeployment events also trigger
reconciliation. `CompositeCluster.status.providers` mirrors attachment observations
and lists their native node pools. Inspect the referenced MachineDeployments for
worker availability; it does not gate CompositeCluster Ready. Failed inventory
reads retain the last complete provider/pool references and mark provider
observations Unknown rather than publishing an empty or partial inventory. The
same rule applies when a provider exceeds the 100-reference status capacity;
inspect native MachineDeployments directly. Inventory also refreshes during
provider/Cluster teardown while native finalizers hold deletion.

Bind the generated metrics-reader ClusterRole to intended scrapers. Expected
responses are 401 without credentials, 403 without authorization and 200 with
both. The current delegated authenticator can return 500 for a malformed token
while still denying access. `--insecure-diagnostics=true` enables unauthenticated
HTTP for isolated development only.

Operator status/logs/Events use bounded categories rather than copying arbitrary
native errors, bootstrap data or warning headers. Verbosity is restricted to 0–4.
Inspect native resources and provider logs with appropriate permissions for the
underlying details. See [security](../SECURITY.md) for the trust boundary.

## Upgrade and uninstall

Before a schema/controller upgrade, preserve the existing CRDs, Composite resources,
binding annotations and native owner references in your normal backup workflow.
The same `v1beta2` API version can acquire new fields or stricter validation; compare
the actual schemas and migration constraints rather than relying only on the version.

For Helm, use all four CRDs from the same source as the chart before
upgrade. Helm's `crds/` directory installs missing CRDs but does not upgrade them:

```sh
kubectl --kubeconfig "$KUBECONFIG" apply --dry-run=server -f api/config/crd/bases
kubectl --kubeconfig "$KUBECONFIG" apply -f api/config/crd/bases
helm upgrade composite ./helm --namespace composite-system \
  --kubeconfig "$KUBECONFIG" --values /path/to/values.yaml --wait --timeout 5m
```

Upgrade controller permissions with the image. In particular, older two-CRD
installations may lack the provider/template APIs and their status/finalizer permissions,
global reference reads, `events.k8s.io` event writes, or the
TokenReview/SubjectAccessReview permissions needed for authenticated metrics.
The shared [generated role](../config/rbac/role.yaml)
is the installation baseline; custom native groups need explicit additional rules.

For manually applied or GitOps-managed installations, update the existing owner's
manifests. Preserve Deployment selectors and bind the generated rules to the
existing ServiceAccount. Labels and names from an older chart may differ from the
current standalone chart, so an image update and a change of installer are separate
operations. Verify the new replica's rollout before treating the upgrade as complete.

This release removes `CompositeCluster.spec.providers` and historical cluster
provider bindings. It is a breaking pre-release change: finish teardown of legacy
compositions using the old controller before upgrading. There is no automatic
migration or adoption of their existing children. Create new attachments as
CompositeProvider resources after installing matching CRDs and controller. Do not
rely on schema pruning to migrate a running composition.

For named regional attachments, keep the canonical InfraCluster of each kind and
apply regional native worker templates explicitly. Bootstrap names remain
`<composite>-<attachment>`; infrastructure may have a different persisted name.
Inspect CompositeProvider `status.infrastructureRef` rather than assuming all native children
share the CompositeCluster name. Do not downgrade schemas over attachments using
credential references: pruning a reference can silently lose credential selection.

For clusterctl, use `clusterctl upgrade apply` with the actual target version and
custom configuration. Back up CRDs, resources, pins and owner references first;
compare native child/template identities and readiness afterwards.

`helm uninstall composite -n composite-system` and default
`clusterctl delete --infrastructure composite` retain CRDs and customer resources.
An absent operator cannot process finalizers. Complete normal Cluster lifecycle
first, or reinstall a compatible operator. Deleting CRDs is not workload cleanup.

## Move and restore

Automatic cross-management-cluster moves are unsupported for template-backed profiles
in this alpha. Attachment bindings retain provider, cluster and profile UIDs and
source-template UID/generation. Recreated objects have different UIDs, even with
identical manifests, and are not silently accepted.

There is no automatic rebind procedure. A future migration must verify native ownership,
source configuration and rewritten identities explicitly. Do not clear binding metadata
to bypass the guard. Helm does not register clusterctl inventory either; inventory
enrollment alone does not solve source identity or CRD discovery and does not transfer
Helm ownership to clusterctl.

## Recovery

Keep backups of profiles, source templates, binding annotations and native owner references.
Status can be rebuilt, but lost pins must not be guessed from today's profile or
from resource names. Restore only verified metadata/configuration with matching
identity. If original evidence is unavailable, investigate the native/cloud state
before proceeding; do not strip finalizers to force progress.

An invalid binding record holds reconciliation/deletion and may hold catalog
deletion globally. The writer rejects records above 16 KiB or with invalid names
before persistence; already-corrupt records still need verified recovery. Choose
short enough Composite/attachment names for `<composite>-<attachment>` to be a
valid Kubernetes resource name. Never truncate an established child's identity.

Profiles require source-template references instead of embedded defaults.
Preserve attachment bindings and `infraClusterName` declarations; downgrading CRDs
may prune fields, and reinstalling a schema cannot restore lost data. Historical
cluster provider bindings are unsupported; use the old controller for their teardown.
Automatic conversion and native renaming remain unsupported.

Retain source templates as immutable named revisions and keep their referenced API
versions served through native-provider upgrades. Editing or recreating a source blocks
bound consumers; metadata-only updates are allowed. Source-template deletion is not
protected by the profile finalizer, and Composite never deletes shared sources.
