# Hosted cluster with provider-specific addon pools

This example starts a hosted control plane with **no native infrastructure**,
then adds Outscale Paris infrastructure and two native worker pools through MSC.
An optional OpenStack addon demonstrates adding another provider. All manifests
referenced below are committed in this repository.

```text
ManagedServiceInstance/empty-demo
  -> Cluster/cluster-empty-demo
     -> CompositeCluster/cluster-empty-demo-infra (spec: {}, Ready)
     -> KamajiControlPlane
  -> SupplementalAddon/osc-paris
     -> CompositeProvider/supplemental-osc-paris-provider
        templateRef -> CompositeProviderTemplate/osc-paris-addon-v1
        clusterRef  -> cluster-empty-demo-infra
        credentialsRef -> namespace-local Outscale Secret
        -> OscCluster/cluster-empty-demo-infra
        -> KubeadmConfigTemplate/cluster-empty-demo-infra-supplemental-osc-paris-provider
     -> MSC child addon/small  -> OscMachineTemplate + MachineDeployment
     -> MSC child addon/medium -> OscMachineTemplate + MachineDeployment
```

Composite coordinates infrastructure/bootstrap and keeps their dual CAPI ownership.
MSC creates the addon graph. CAPI and native providers create, join and manage
Machines. Provider Ready does not imply worker availability, and worker failures do
not undo the empty CompositeCluster's infrastructure readiness.

## 1. Prerequisites and placement

You need:

- MSC with supplemental-class `addonDefinitions` expansion enabled/supported.
- CAPI with ClusterTopology, kubeadm bootstrap, Composite, CAPOSC, Kamaji and its
  CAPI control-plane provider. Install CAPO before using the OpenStack example.
- Native provider CRDs and compatible provider versions, Kubernetes-ready worker
  images, credentials and worker-to-control-plane networking.
- Kamaji endpoint publication for `CompositeCluster`, including its dynamic-kind
  configuration and write RBAC; see [operations](../operations.md).
- Working CNI/routing and DNS/datastore connectivity for the hosted control plane.
  This example is not a CNI, CSI or cross-cloud routing installation.

MSC's **management cluster** hosts its classes, overlays, instance and supplemental
addons. Its **service cluster** hosts CAPI, Composite, native templates and the
tenant's resources. They can be the same API server. The workload/tenant API is a
separate endpoint.

```sh
export MSC_KUBECONFIG=/path/to/msc-management.yaml
export SERVICE_KUBECONFIG=/path/to/capi-management.yaml
```

Install the operator, all four generated CRDs and generated RBAC from **the same
source revision**. Build/publish an image using [development](../development.md#build-and-publish-an-image),
then follow [installation](../operations.md#install). The chart's development
version does not assert that its image has already been published.

## 2. Configure the immutable native sources

Before first use, edit:

- [provider-templates.yaml](../../local/managed-service/service-cluster/provider-templates.yaml):
  native networks, regional identities and bootstrap requirements.
- [overlay.yaml](../../local/managed-service/management-cluster/overlay.yaml):
  hosted-control-plane service exposure and Kubernetes version.
- [outscale-addon.yaml](../../local/managed-service/provider-addons/outscale-addon.yaml):
  Kubernetes-ready image and keypair names. Keep its version consistent with the
  parent instance/overlay.

The native templates are administrator-managed. Composite reads only their
`spec.template.spec`, does not copy metadata and never mutates/owns/deletes them.
Publish new named revisions instead of editing or recreating sources in use.

```sh
kubectl --kubeconfig "$SERVICE_KUBECONFIG" apply \
  -f local/managed-service/service-cluster/namespace.yaml \
  -f local/managed-service/service-cluster/provider-templates.yaml \
  -f local/managed-service/provider-addons/templates.yaml
kubectl --kubeconfig "$SERVICE_KUBECONFIG" wait \
  --for=condition=ConfigurationValid=True \
  compositeprovidertemplate/osc-paris-addon-v1 --timeout=60s
```

The reusable Outscale configuration contains:

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: CompositeProviderTemplate
metadata:
  name: osc-paris-addon-v1
spec:
  provider: outscale
  account: account-a
  region: eu-west-2
  zones: [eu-west-2a]
  credentialsNamePaths: [/credentials/fromSecret]
  # Native infrastructure/bootstrap references and policy are in templates.yaml.
```

This fragment illustrates credential injection; use the complete committed
[templates.yaml](../../local/managed-service/provider-addons/templates.yaml).
`ConfigurationValid` validates sources/policy, not cloud credentials or provisioning.

## 3. Install the cluster and addon definitions

On the service cluster:

```sh
kubectl --kubeconfig "$SERVICE_KUBECONFIG" apply \
  -f local/managed-service/service-cluster/composite-cluster-template.yaml \
  -f local/managed-service/service-cluster/kamaji-control-plane-template.yaml \
  -f local/managed-service/service-cluster/cluster-class.yaml
```

On MSC's management cluster:

```sh
kubectl --kubeconfig "$MSC_KUBECONFIG" apply \
  -f local/managed-service/management-cluster/namespace.yaml \
  -f local/managed-service/management-cluster/cluster-class.yaml \
  -f local/managed-service/management-cluster/overlay.yaml \
  -f local/managed-service/management-cluster/outscale-nodepool-class.yaml \
  -f local/managed-service/management-cluster/openstack-nodepool-class.yaml \
  -f local/managed-service/provider-addons/outscale-class.yaml \
  -f local/managed-service/provider-addons/openstack-class.yaml
```

Bind [msc-rbac.yaml](../../local/managed-service/service-cluster/msc-rbac.yaml) to
the identity MSC uses on its service cluster. That identity needs namespaced
CompositeProvider and native worker-resource writes. Composite itself needs no
Secret read permission.

The Outscale provider class renders a `CompositeProvider` resource and declares
`small` and `medium` worker child addons. Their native machine types are
`tinav7.c2r4p2` and `tinav7.c4r8p2`. Both consume the same generated bootstrap
template. The OpenStack provider class selects the native OpenStack worker class.
Render these classes with your MSC version before applying real instance requests.

## 4. Start the empty hosted cluster

```sh
kubectl --kubeconfig "$MSC_KUBECONFIG" apply -f local/managed-service/instance-empty.yaml
kubectl --kubeconfig "$SERVICE_KUBECONFIG" get cluster,compositecluster \
  -n kubernetes-cluster-empty-demo
kubectl --kubeconfig "$MSC_KUBECONFIG" wait --for=condition=Ready=True \
  managedserviceinstance/empty-demo -n cloud-kubernetes --timeout=15m
```

The generated contract has no provider-selection field, latched
`status.initialization.provisioned: true` and Ready=True after successful owner,
pause and binding checks. No native infrastructure, bootstrap template or endpoint
is required for that initial success. The MSI additionally waits for the hosted
control plane and remote API probe; those checks can fail independently.

## 5. Supply the namespace-local Secret and add Outscale pools

Use your secret-management mechanism to create
`outscale-account-a-eu-west-2` in **`kubernetes-cluster-empty-demo` on the service
cluster**, in CAPOSC's expected format. Do not place credentials in this repository
or the shared template. Composite references the Secret name only; CAPOSC reads
the actual credentials.

```sh
kubectl --kubeconfig "$MSC_KUBECONFIG" apply \
  -f local/managed-service/provider-addons/outscale-addon.yaml
```

MSC creates this namespaced provider:

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: CompositeProvider
metadata:
  name: supplemental-osc-paris-provider
  namespace: kubernetes-cluster-empty-demo
spec:
  clusterRef: {name: cluster-empty-demo-infra}
  templateRef: {name: osc-paris-addon-v1}
  credentialsRef: {name: outscale-account-a-eu-west-2}
  infraSpec:
    network:
      clusterName: cluster-empty-demo
      internetService: {name: cluster-empty-demo}
```

Do not apply this reference output over MSC's object. Composite injects the Secret
name at `/credentials/fromSecret`, resolves allowed overrides, pins source identity
and coordinates the canonical OscCluster. It creates bootstrap after infrastructure
application and a complete control-plane endpoint are available. Provider Ready
requires current native health and bootstrap readiness.

When the provider is ready, MSC expands its two worker addons. Their native
MachineDeployments reference:

```yaml
bootstrap:
  configRef:
    apiGroup: bootstrap.cluster.x-k8s.io
    kind: KubeadmConfigTemplate
    name: cluster-empty-demo-infra-supplemental-osc-paris-provider
# infrastructureRef selects each pool's native OscMachineTemplate.
```

## 6. Observe each layer separately

```sh
kubectl --kubeconfig "$SERVICE_KUBECONFIG" wait --for=condition=Ready=True \
  compositeprovider/supplemental-osc-paris-provider \
  -n kubernetes-cluster-empty-demo --timeout=15m
kubectl --kubeconfig "$SERVICE_KUBECONFIG" get \
  compositecluster,compositeprovider,osccluster,kubeadmconfigtemplate,machinedeployment,machine \
  -n kubernetes-cluster-empty-demo
kubectl --kubeconfig "$MSC_KUBECONFIG" get managedservicesupplementaladdon/osc-paris \
  -n cloud-kubernetes -o yaml
```

Check condition `observedGeneration`, pause and deletion state, not a historic
initialization bit alone. Expected separation:

| Observation | Meaning |
|---|---|
| CompositeCluster Ready | Empty infrastructure contract is usable; addon failures do not gate it |
| CompositeProvider Ready | Current native infrastructure, endpoint propagation and bootstrap are usable |
| MachineDeployment Available / MSC worker addon Ready | Worker pool availability |
| Tenant nodes Ready | Worker join and node/network initialization succeeded |

Inspect bounded provider conditions first, then the referenced native resources
with appropriate access. A missing/changed source, prohibited override, duplicate
native identity or unavailable native API retains existing resources rather than
adopting/rebinding them. Cloud authorization failures belong to the native provider.

## 7. Optional OpenStack pool

Supply `openstack-account-a-eu-west-par` in the same service-cluster namespace.
Create another `ManagedServiceSupplementalAddon` on MSC's management cluster:

```yaml
apiVersion: apiextensions.managedservice.numspot.com/v1alpha2
kind: ManagedServiceSupplementalAddon
metadata:
  name: ovh-paris
  namespace: cloud-kubernetes
spec:
  instanceRef: {name: empty-demo}
  classRef: {name: composite-openstack-provider-v1, namespace: cloud-kubernetes}
  parameters:
    nodePoolId: ovh-paris
    clusterName: cluster-empty-demo
    infrastructureName: cluster-empty-demo-infra
    clusterVersion: v1.34.10
    templateName: ovh-paris-addon-v1
    credentialsSecretName: openstack-account-a-eu-west-par
    attachment: ovh-paris
    replicas: "1"
    machineSpec: >-
      {"flavor":"b3-16","image":{"filter":{"name":"replace-with-kubernetes-ready-image"}},
       "identityRef":{"type":"Secret","name":"openstack-account-a-eu-west-par","cloudName":"openstack","region":"EU-WEST-PAR"},
       "ports":[{"network":{"filter":{"name":"replace-with-network"}},
       "securityGroups":[{"filter":{"name":"replace-with-worker-security-group"}}]}]}
```

Edit image/network/security-group selections before use. This creates the canonical
OpenStackCluster alongside the canonical OscCluster: GroupKinds differ, so the
shared native name is valid. A Warsaw addon using `ovh-warsaw-addon-v1` must supply
a distinct `infraClusterName`, retain Paris as canonical OpenStack context, and
explicitly select regional credentials/network/subnet/security groups in its native
MachineTemplate. Template selection does not rewrite native worker lookup behavior.

## 8. Cleanup

Delete supplemental addons through MSC; it must drain their worker child graph
before releasing provider resources:

```sh
kubectl --kubeconfig "$MSC_KUBECONFIG" delete managedservicesupplementaladdon/osc-paris \
  -n cloud-kubernetes --wait=false
```

Composite waits for MachineDeployment/MachineSet/Machine consumers, then deletes
infrastructure, confirms absence, deletes bootstrap and releases provider protection.
Source templates are not required for teardown. A canonical attachment remains
protected while dependent regional attachments exist. Unreadable APIs or unknown
Machine bootstrap provenance retain protection.

Retired provider identities remain pinned for the cluster lifetime: do not recreate
the same provider name expecting transparent rebinding. Use a new addon identity
for a new attachment. Keep controllers, credentials and definitions until cleanup
finishes. To remove the whole cluster, delete its MSI through MSC; never strip
Composite finalizers or binding annotations to force deletion.
