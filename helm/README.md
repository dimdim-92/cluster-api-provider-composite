# Composite Helm Chart

A standalone chart for the manager, CRDs, service account, RBAC and metrics Service.
Install compatible CAPI/bootstrap/control-plane/native providers and credential
references separately. See [operations](../docs/operations.md) for prerequisites,
upgrades and moves.

The chart ships four `infrastructure.cluster.x-k8s.io/v1beta2` CRDs:

| Resource | Scope | Status subresource |
|---|---|---|
| `CompositeCluster` | Namespaced | Yes |
| `CompositeClusterTemplate` | Namespaced | No |
| `CompositeProvider` | Namespaced | Yes |
| `CompositeProviderTemplate` | Cluster | Yes |

Their schemas are copied from [`api/config/crd/bases/`](../api/config/crd/bases/).
See the [API reference](../docs/usage.md#api-reference) for profile fields,
attachment modes, immutable configuration and validation limits.

The `infraClusterName` attachment field enables additional regional infrastructure
of an existing kind while retaining a canonical child. Template-backed profiles
require matching CRDs and controller with source identity pins; see
[regional infrastructure](../docs/usage.md#same-provider-regional-infrastructure).

## Install

[Build and publish an image](../docs/development.md#build-and-publish-an-image), then
set the actual repository and published tag in your values file. For example,
adapt this `composite-values.yaml` to your registry and release:

```yaml
image:
  repository: registry.example.com/team/cluster-api-provider-composite
  tag: v0.1.0-alpha.1
  digest: "" # Clear a packaged release's digest when intentionally selecting a tag.
# For private images, provision this pull Secret in the release namespace:
# imagePullSecrets:
#   - name: registry-pull
```

An `image.digest` value containing the full `sha256:...` reported by the registry
takes precedence over the tag. Empty `image.tag` uses `Chart.appVersion`; that
fallback does not mean an image has been published. Registry pull Secrets are
namespaced independently of the cluster-scoped CompositeProviderTemplate catalog.

From the repository root, with an explicit management kubeconfig:

```sh
KUBECONFIG=/path/to/management.yaml
helm lint helm -f /path/to/composite-values.yaml
kubectl --kubeconfig "$KUBECONFIG" apply -f api/config/crd/bases
helm upgrade --install composite ./helm --namespace composite-system \
  --kubeconfig "$KUBECONFIG" --create-namespace \
  -f /path/to/composite-values.yaml --wait --timeout 5m
kubectl --kubeconfig "$KUBECONFIG" rollout status \
  deployment/composite -n composite-system --timeout=5m
```

Profile-backed attachments supply their own native GroupKind. Other API groups
still require administrator-supplied `additionalRBACRules`.

With these defaults, the Deployment and ServiceAccount are `composite`, the
ClusterRole/ClusterRoleBinding are `composite-system-composite`, and the metrics
Service is `composite-metrics`. `fullnameOverride` and `serviceAccount.name` change
the relevant names. Use the existing installation's owner for upgrades; see
[operations](../docs/operations.md#upgrade-and-uninstall) for older chart layouts.

## Published chart

The [release pipeline](../.github/workflows/release.yml) publishes this chart with
the provider's version, not a separate chart-only tag. After the relevant release
jobs succeed, the OCI reference is
`oci://ghcr.io/numspot/charts/cluster-api-provider-composite`, with the leading `v`
removed from the chart version. A matching chart archive is attached to the GitHub
release; both destinations receive the same archive. Its image defaults pin the
actual published repository and multiarch digest. Packaging stages these defaults
in scratch without changing source values or CRDs. Release-please keeps
chart/appVersion and the provider candidate in sync. The alpha manifest is only a
starting version; it does not mean any package exists.

See [release setup and artifacts](../docs/operations.md#automated-releases) for
repository permissions, package visibility and matching-image rules. Helm still
does not upgrade installed CRDs; use the matching source/release assets and the
normal CRD upgrade procedure below.

## Values

| Value | Purpose |
|---|---|
| `fullnameOverride`, `replicaCount` | Workload name override and replica count (default 1) |
| `image.repository`, `image.tag`, `image.digest` | Digest takes precedence; empty tag uses Chart.AppVersion |
| `imagePullSecrets` | Existing registry credential references |
| `watchNamespace`, `watchFilter` | Consumer scope; profile protection remains global |
| `leaderElection` | Enabled by default; required for coordinated replicas |
| `metrics.port`, `healthPort` | Default 5555 and 2112 |
| `metrics.insecure` | Isolated development-only unauthenticated HTTP |
| `metrics.existingSecret` | Serving certificate Secret containing tls.crt/tls.key |
| `serviceAccount.create`, `serviceAccount.name` | Create or reuse a service account |
| `resources`, `nodeSelector`, `tolerations`, `affinity` | Resource/scheduling settings |
| `additionalRBACRules` | Explicit access for additional native API groups |
| `extraArgs`, `extraEnv` | Advanced overrides; keep ports, probes and Service consistent |

Prefer dedicated values. `extraArgs` follow chart arguments and can override them.

The operator binary always initializes caches with LIST then WATCH; no Helm value
or environment setting enables streaming initial events. See
[informer startup](../docs/operations.md#informer-startup).

## CRD lifecycle

Helm installs missing CRDs from `crds/`, but does not upgrade/delete existing CRDs.
Back up resources and apply the complete matching set before a controller upgrade,
including both provider APIs:

```sh
kubectl --kubeconfig "$KUBECONFIG" apply --dry-run=server -f api/config/crd/bases
kubectl --kubeconfig "$KUBECONFIG" apply -f api/config/crd/bases
helm upgrade composite ./helm -n composite-system --kubeconfig "$KUBECONFIG" \
  -f /path/to/composite-values.yaml --wait --timeout 5m
```

The chart updates the operator and generated RBAC together, including global
catalog protection. A CRD-only apply does not install or upgrade the operator.
Current cluster/template CRDs advertise the v1beta2 InfraCluster contract;
CompositeProvider and CompositeProviderTemplate carry provider identity only.

Uninstall retains customer resources; it is not infrastructure cleanup. Helm and
clusterctl should not own the same installation. The chart does not create
clusterctl inventory. Source UID/generation pins also prevent transparent profile
restore or move; see [move and restore](../docs/operations.md#move-and-restore).

Metrics require TLS and TokenReview/SubjectAccessReview authorization by default.
Bind the generated metrics-reader role to intended scrapers. The shared manager
role is generated with the clusterctl role; change generation inputs rather than
editing generated RBAC/CRDs by hand.
