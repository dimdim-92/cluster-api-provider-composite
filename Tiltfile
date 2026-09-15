# capi-composite local development loop.
cluster_name = 'kind-kind'
project_name = 'capi-composite'

allow_k8s_contexts(['kind', cluster_name])

# Build a Linux binary on the host so Delve can use unoptimized debug symbols.
local_resource(
    '%s-binary' % project_name,
    'GOOS=linux CGO_ENABLED=0 go build -ldflags="-X main.Version=$(date -u +%Y%m%d-%H%M%S)" -gcflags "all=-N -l" -o capi-composite-controller ./cmd/operator',
    deps=['./api/', './cmd/', './internal/', './configuration/'],
)

custom_build(
    'localhost:5001/capi-composite-controller',
    'docker build -t $EXPECTED_REF -f build/tilt.Dockerfile .',
    deps=['./capi-composite-controller'],
)

# CRDs are applied as a local_resource so ordering against the deployment is
# explicit — k8s_yaml does not guarantee CRDs land before the controller starts.
local_resource(
    'crds',
    'kubectl apply -f api/config/crd/bases/',
    deps=['./api/config/crd/bases/'],
)

# Upstream provider CRDs we generate children for. Manual: needed once, and only
# on a cluster where the real providers are not installed.
local_resource(
    'provider-crds',
    'kubectl apply -f tests/crds/',
    trigger_mode=TRIGGER_MODE_MANUAL,
)

local_resource(
    'setup-capi',
    'CLUSTER_TOPOLOGY=true clusterctl init --infrastructure docker --control-plane kubeadm --bootstrap kubeadm',
    trigger_mode=TRIGGER_MODE_MANUAL,
)

k8s_yaml('local/namespace.yaml')
k8s_yaml('local/rbac.yaml')
k8s_yaml('local/deployment.yaml')

k8s_resource(
    'capi-composite-controller',
    port_forwards=[
        '2112:2112',   # health: /ping, /ready
        '5555:5555',   # metrics
        '50100:50100', # delve
    ],
    resource_deps=['crds'],
    labels=['controller'],
)
