// Copyright 2026 Numspot.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package envtest_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	clusterctlv1 "sigs.k8s.io/cluster-api/cmd/clusterctl/api/v1alpha3"
	clusterctl "sigs.k8s.io/cluster-api/cmd/clusterctl/client"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client/cluster"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/client/config"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestClusterctlInitInstallation(t *testing.T) {
	clusterctlInstallationConfigHome(t)
	componentsPath, image := clusterctlInstallationRelease(t)
	configuration := clusterctlInstallationConfiguration(t, componentsPath)
	credentials := clusterctlInstallationKubeconfig(t)
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme, apiextensionsv1.AddToScheme, clusterctlv1.AddToScheme,
	} {
		require.NoError(t, add(scheme))
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	coreNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "clusterctl-core-"}}
	require.NoError(t, c.Create(t.Context(), coreNamespace))
	clusterctlInstallationCleanup(t, c, coreNamespace)
	targetNamespace := coreNamespace.Name + "-composite"
	// The suite already installs the real CAPI CRDs. Seed only their clusterctl
	// inventory entry, so Init extends an existing management cluster instead of
	// silently fetching the default core/bootstrap/control-plane providers.
	realCluster := cluster.New(cluster.Kubeconfig{Path: credentials}, configuration)
	require.NoError(t, realCluster.ProviderInventory().EnsureCustomResourceDefinitions(t.Context()))
	coreProvider := clusterctlv1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-api", Namespace: coreNamespace.Name, Labels: map[string]string{
			clusterv1.ProviderNameLabel: "cluster-api", clusterctlv1.ClusterctlLabel: "",
			clusterctlv1.ClusterctlCoreLabel: clusterctlv1.ClusterctlCoreLabelInventoryValue,
		}},
		ProviderName: "cluster-api", Type: string(clusterctlv1.CoreProviderType), Version: "v1.12.4",
	}
	require.NoError(t, realCluster.ProviderInventory().Create(t.Context(), coreProvider))
	clusterctlInstallationCleanup(t, c, &coreProvider)
	clusterCalls, certificateCalls := 0, 0
	ctl, err := clusterctl.New(t.Context(), "", clusterctl.InjectConfig(configuration),
		clusterctl.InjectClusterClientFactory(func(input clusterctl.ClusterClientFactoryInput) (cluster.Client, error) {
			clusterCalls++
			require.Equal(t, credentials, input.Kubeconfig.Path, "Init must use only the explicit envtest admin kubeconfig")
			real := cluster.New(cluster.Kubeconfig(input.Kubeconfig), configuration, cluster.InjectYamlProcessor(input.Processor))
			return &clusterctlInstallationCluster{Client: real, certificateCalls: &certificateCalls}, nil
		}))
	require.NoError(t, err)
	options := clusterctl.InitOptions{
		Kubeconfig: clusterctl.Kubeconfig{Path: credentials}, InfrastructureProviders: []string{"composite:v0.1.0"},
		TargetNamespace: targetNamespace, WaitProviders: false,
	}
	components, err := ctl.Init(t.Context(), options)
	require.NoError(t, err)
	require.Len(t, components, 1, "only Composite should be installed")
	for _, object := range components[0].Objs() {
		if object.GetKind() != "CustomResourceDefinition" {
			clusterctlInstallationCleanup(t, c, object.DeepCopy())
		}
	}
	inventory := components[0].InventoryObject()
	clusterctlInstallationCleanup(t, c, &inventory)
	assert.Equal(t, "composite", components[0].Name())
	assert.Equal(t, targetNamespace, components[0].TargetNamespace())
	assert.Empty(t, components[0].Variables())
	before := clusterctlInstallationSnapshot(t, c, components[0].Objs())
	clusterctlAssertInstalledResources(t, c, targetNamespace, image)
	provider := clusterctlAssertInstalledInventory(t, c, targetNamespace)
	assert.Equal(t, 1, certificateCalls, "real Init always invokes cert-manager, even for providers without webhooks")

	repeated, err := ctl.Init(t.Context(), options)
	require.NoError(t, err)
	assert.Empty(t, repeated, "an already installed provider must not enter the real installer queue twice")
	assert.Equal(t, before, clusterctlInstallationSnapshot(t, c, components[0].Objs()), "repeat Init must not recreate or patch components")
	assert.Equal(t, provider, clusterctlAssertInstalledInventory(t, c, targetNamespace))
	assert.Equal(t, 2, clusterCalls)
	assert.Equal(t, 2, certificateCalls)
}

// Only cert-manager's mandatory EnsureInstalled is bypassed: envtest has no
// controllers or running pods. Installer, inventory, proxy and repository stay real.
type clusterctlInstallationCluster struct {
	cluster.Client
	certificateCalls *int
}

func (c *clusterctlInstallationCluster) CertManager() cluster.CertManagerClient {
	return &clusterctlInstallationCertManager{CertManagerClient: c.Client.CertManager(), calls: c.certificateCalls}
}

type clusterctlInstallationCertManager struct {
	cluster.CertManagerClient
	calls *int
}

func (c *clusterctlInstallationCertManager) EnsureInstalled(_ context.Context) error {
	*c.calls++
	return nil
}

func clusterctlInstallationCleanup(t *testing.T, c client.Client, object client.Object) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		assert.NoError(t, client.IgnoreNotFound(c.Delete(ctx, object)))
	})
}

func clusterctlInstallationConfigHome(t *testing.T) {
	t.Helper()
	// Avoid the legacy-home override fallback without moving nested Go caches.
	t.Cleanup(xdg.Reload)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	xdg.Reload()
	directory, err := xdg.ConfigFile(config.ConfigFolderXDG)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(directory, "overrides"), 0o700))
	for _, name := range []string{"COMPOSITE_WATCH_FILTER", "WATCH_FILTER"} {
		previous, present := os.LookupEnv(name)
		require.NoError(t, os.Unsetenv(name))
		if present {
			t.Cleanup(func() { require.NoError(t, os.Setenv(name, previous)) })
		}
	}
}

func clusterctlInstallationRelease(t *testing.T) (componentsPath, image string) {
	t.Helper()
	root := t.TempDir()
	for _, path := range []string{"config", "api/config/crd", "hack/check-release"} {
		require.NoError(t, os.CopyFS(filepath.Join(root, path), os.DirFS(filepath.Join("..", "..", path))))
	}
	for _, path := range []string{"Makefile", "go.mod", "go.sum", "metadata.yaml", "hack/tools.env", "hack/verify-version.sh", "hack/release-manifests.sh"} {
		data, err := os.ReadFile(filepath.Join("..", "..", path))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, path), data, 0o600))
	}
	goTool, err := exec.LookPath("go")
	require.NoError(t, err)
	goTool, err = filepath.Abs(goTool)
	require.NoError(t, err)
	image = "registry.example.org/composite@sha256:" + strings.Repeat("b", 64)
	cmd := exec.CommandContext(t.Context(), "make", "release-manifests", "VERSION=v0.1.0", "IMAGE="+image)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GO="+goTool)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "generating real release assets in scratch: %s", output)
	return filepath.Join(root, "out", "repository", "infrastructure-composite", "v0.1.0", "infrastructure-components.yaml"), image
}

func clusterctlInstallationConfiguration(t *testing.T, componentsPath string) config.Client {
	t.Helper()
	reader := config.NewMemoryReader()
	require.NoError(t, reader.Init(t.Context(), ""))
	_, err := reader.AddProvider("composite", clusterctlv1.InfrastructureProviderType, componentsPath)
	require.NoError(t, err)
	capiPath, err := capiCRDPath(t.Context())
	require.NoError(t, err)
	metadata, err := os.ReadFile(filepath.Join(capiPath, "..", "..", "..", "metadata.yaml"))
	require.NoError(t, err)
	coreRelease := filepath.Join(t.TempDir(), "cluster-api", "v1.12.4")
	require.NoError(t, os.MkdirAll(coreRelease, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(coreRelease, "metadata.yaml"), metadata, 0o600))
	_, err = reader.AddProvider("cluster-api", clusterctlv1.CoreProviderType, filepath.Join(coreRelease, "core-components.yaml"))
	require.NoError(t, err)
	configuration, err := config.New(t.Context(), "", config.InjectReader(reader))
	require.NoError(t, err)
	for _, name := range []string{"COMPOSITE_WATCH_FILTER", "WATCH_FILTER"} {
		_, err := configuration.Variables().Get(name)
		require.Error(t, err, "%s must be absent rather than explicitly empty", name)
	}
	return configuration
}

func clusterctlInstallationKubeconfig(t *testing.T) string {
	t.Helper()
	require.NotNil(t, cfg)
	// Never discover a workstation kubeconfig: every endpoint and credential is
	// copied exclusively from the isolated suite's envtest REST configuration.
	configuration := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{"envtest": {
			Server: cfg.Host, CertificateAuthorityData: cfg.CAData, InsecureSkipTLSVerify: cfg.Insecure,
		}},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{"envtest-admin": {
			ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData, Token: cfg.BearerToken,
		}},
		Contexts:       map[string]*clientcmdapi.Context{"envtest": {Cluster: "envtest", AuthInfo: "envtest-admin"}},
		CurrentContext: "envtest",
	}
	data, err := clientcmd.Write(configuration)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "envtest-admin.kubeconfig")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func clusterctlInstallationSnapshot(t *testing.T, c client.Client, objects []unstructured.Unstructured) map[string]string {
	t.Helper()
	snapshot := make(map[string]string, len(objects))
	for _, expected := range objects {
		actual := &unstructured.Unstructured{}
		actual.SetGroupVersionKind(expected.GroupVersionKind())
		require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(&expected), actual))
		assert.Equal(t, "infrastructure-composite", actual.GetLabels()[clusterv1.ProviderNameLabel], expected.GetName())
		assert.Contains(t, actual.GetLabels(), clusterctlv1.ClusterctlLabel, expected.GetName())
		identity := expected.GroupVersionKind().String() + "/" + expected.GetNamespace() + "/" + expected.GetName()
		snapshot[identity] = string(actual.GetUID()) + "/" + actual.GetResourceVersion()
	}
	return snapshot
}

func clusterctlAssertInstalledResources(t *testing.T, c client.Client, namespace, image string) {
	t.Helper()
	deployment := &appsv1.Deployment{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: namespace, Name: "composite-controller-manager"}, deployment))
	assert.Equal(t, "composite-manager", deployment.Spec.Template.Spec.ServiceAccountName)
	require.Len(t, deployment.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, image, deployment.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, []corev1.EnvVar{{Name: "WATCH_FILTER", Value: ""}}, deployment.Spec.Template.Spec.Containers[0].Env)
	serviceAccount := &corev1.ServiceAccount{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: namespace, Name: "composite-manager"}, serviceAccount))
	binding := &rbacv1.ClusterRoleBinding{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Name: "composite-manager-rolebinding"}, binding))
	assert.Equal(t, []rbacv1.Subject{{Kind: "ServiceAccount", Name: "composite-manager", Namespace: namespace}}, binding.Subjects)
	assert.Equal(t, rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "composite-manager-role"}, binding.RoleRef)
	role := &rbacv1.ClusterRole{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Name: binding.RoleRef.Name}, role))
	for _, plural := range []string{"compositeclusters", "compositeclustertemplates", "compositeproviders", "compositeprovidertemplates"} {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		require.NoError(t, c.Get(t.Context(), client.ObjectKey{Name: plural + ".infrastructure.cluster.x-k8s.io"}, crd))
		require.Len(t, crd.Spec.Versions, 1)
		assert.Equal(t, "v1beta2", crd.Spec.Versions[0].Name)
		assert.True(t, crd.Spec.Versions[0].Served)
		assert.True(t, crd.Spec.Versions[0].Storage)
	}
}

func clusterctlAssertInstalledInventory(t *testing.T, c client.Client, namespace string) *clusterctlv1.Provider {
	t.Helper()
	providers := &clusterctlv1.ProviderList{}
	require.NoError(t, c.List(t.Context(), providers))
	require.Len(t, providers.Items, 2, "the seeded core provider and Composite must be the only inventory entries")
	composite := providers.FilterByProviderNameAndType("composite", clusterctlv1.InfrastructureProviderType)
	require.Len(t, composite, 1, "repeated Init must not duplicate the provider")
	provider := &composite[0]
	assert.Equal(t, "infrastructure-composite", provider.Name)
	assert.Equal(t, namespace, provider.Namespace)
	assert.Equal(t, "v0.1.0", provider.Version)
	assert.Equal(t, "composite", provider.ProviderName)
	assert.Equal(t, string(clusterctlv1.InfrastructureProviderType), provider.Type)
	assert.Equal(t, "infrastructure-composite", provider.Labels[clusterv1.ProviderNameLabel])
	assert.Equal(t, clusterctlv1.ClusterctlCoreLabelInventoryValue, provider.Labels[clusterctlv1.ClusterctlCoreLabel])
	assert.Contains(t, provider.Labels, clusterctlv1.ClusterctlLabel)
	return provider
}
