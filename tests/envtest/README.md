# API-server and operator tests

```sh
make envtest-download
make test
make test TEST=TestProviderAttachment
make test TEST=TestOperatorRuntime
```

The suite uses pinned local API-server/etcd assets and the race detector. Direct
Go invocations need `KUBEBUILDER_ASSETS`; `SKIP_ENVTEST=1` is local-only and rejected
in CI. Tests never use workstation kubeconfigs or provision cloud workers.

Reconciler tests use a live API client. Operator runtime tests build/run the real
binary under shipped RBAC, including scope, leadership and authenticated metrics.
The four generated CRDs expose namespaced CompositeCluster,
CompositeClusterTemplate and CompositeProvider, and cluster-scoped
CompositeProviderTemplate. CompositeProvider is the only attachment path;
CompositeCluster and its template accept empty specs and have no providers field.
Historical cluster provider-binding records are unsupported.

Retained regressions cover native ownership and UID/resourceVersion race guards,
source pin/drift protection, immutable provider references, credentials isolation,
independent empty-cluster readiness, owner/pause gates, shared worker consumers,
ordered source-independent retirement and API contracts.

Passing envtest does not establish native cloud compatibility or safe workload
moves. Real native-stack integration must be tested separately.
