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

package hack_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/yaml"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

func TestQuickstartLifecycle(t *testing.T) {
	for platform, expected := range map[string]string{
		"linux/x86_64": "linux/amd64", "linux/amd64": "linux/amd64",
		"linux/aarch64": "linux/arm64", "linux/arm64": "linux/arm64",
	} {
		t.Run(platform, func(t *testing.T) {
			f := newQuickstartFixture(t)
			output, err := f.run(t, "up", map[string]string{"QUICKSTART_PLATFORM": platform})
			require.NoError(t, err, "%s", output)
			assert.Contains(t, string(output), "Docker provider is Ready")
			calls := f.calls(t)
			assert.Equal(t, []string{
				"buildx", "build", "--load", "--platform", expected, "--file", filepath.Join(f.root, "build", "operator.Dockerfile"),
				"--build-arg", "version=quickstart", "--tag", "composite:quickstart", f.root,
			}, quickstartFindCall(t, calls, "docker", "buildx", "build").Args)
			assert.Equal(t, []string{
				"create", "cluster", "--name", "composite-quickstart", "--image",
				"kindest/node:v1.34.0@sha256:7416a61b42b1662ca6ca89f02028ac133a309a2a30ba309614e8ec94d976dc5a",
				"--config", filepath.Join(f.root, "local", "quickstart-kind.yaml"), "--kubeconfig", f.kubeconfig(), "--wait", "5m",
			}, quickstartFindCall(t, calls, "kind", "create").Args)
			init := quickstartFindCall(t, calls, "clusterctl", "init")
			assert.Equal(t, []string{
				"init", "--config", filepath.Join(f.root, "out", "quickstart", "clusterctl.yaml"),
				"--kubeconfig", f.kubeconfig(), "--kubeconfig-context", "kind-composite-quickstart",
				"--core", "cluster-api:v1.12.4", "--bootstrap", "kubeadm:v1.12.4", "--control-plane", "kubeadm:v1.12.4",
				"--infrastructure", "docker:v1.12.4", "--wait-providers",
			}, init.Args)
			assert.Equal(t, "true", init.Topology, "CAPD rejects DockerClusterTemplate without this feature gate")
			assert.Equal(t, filepath.Join(f.root, "out", "quickstart", "config"), init.ConfigHome)
			assert.Equal(t, filepath.Join(f.root, "out", "quickstart", "cache"), init.CacheHome)
			install := quickstartFindCall(t, calls, "helm", "upgrade")
			for _, value := range []string{"image.repository=composite", "image.tag=quickstart", "image.digest=", "image.pullPolicy=Never"} {
				assert.Contains(t, install.Args, value)
			}
			var waits [][]string
			for _, call := range calls {
				assert.Equal(t, f.kubeconfig(), call.Kubeconfig, "all tools must be isolated from the caller's kubeconfig")
				assert.Equal(t, "docker", call.Provider)
				if call.Tool == "kubectl" && slices.Contains(call.Args, "wait") {
					waits = append(waits, call.Args)
				}
			}
			require.Len(t, waits, 8, "verify native child creation before observing its endpoint and the actual workload")
			assert.Contains(t, waits[1], "--for=create")
			assert.Contains(t, waits[3], "compositeprovider/docker")
			assert.Contains(t, waits[3], "--for=condition=Ready=True")
			assert.Contains(t, waits[5], "--for=jsonpath={.status.initialization.controlPlaneInitialized}=true")
			assert.Contains(t, waits[6], "machinedeployment/composite-docker-workers")
			assert.Contains(t, waits[6], "--for=condition=Available=True")
			assert.Contains(t, waits[7], "--all")
			patch := quickstartFindCall(
				t,
				calls,
				"kubectl",
				"--kubeconfig",
				f.kubeconfig(),
				"--context",
				"kind-composite-quickstart",
				"-n",
				"composite-quickstart",
				"patch",
			)
			assert.Contains(t, patch.Args, `{"spec":{"controlPlaneEndpoint":{"host":"192.0.2.10","port":6443}}}`)
			assert.Equal(
				t,
				[]string{"get", "kubeconfig", "--name", "composite-docker"},
				quickstartFindCall(t, calls, "kind", "get", "kubeconfig").Args,
			)
			assert.Contains(t, string(output), "workload.kubeconfig")
			info, err := os.Stat(f.kubeconfig())
			require.NoError(t, err)
			assert.Equal(t, fs.FileMode(0o600), info.Mode().Perm())
			f.assertCallerUnchanged(t)

			output, err = f.run(t, "up", nil)
			require.Error(t, err, "%s", output)
			assert.Contains(t, string(output), "refusing to reuse")
			assert.Len(t, f.calls(t), len(calls)+3, "an existing cluster must fail before another build or install")

			output, err = f.run(t, "status", nil)
			require.NoError(t, err, "%s", output)
			output, err = f.run(t, "down", nil)
			require.NoError(t, err, "%s", output)
			assert.Equal(t, []string{"delete", "cluster", "--name", "composite-quickstart", "--kubeconfig", f.kubeconfig()},
				quickstartFindCall(t, f.calls(t), "kind", "delete").Args)
			_, err = os.Stat(filepath.Join(f.root, "fake-cluster"))
			assert.ErrorIs(t, err, fs.ErrNotExist)
			f.assertCallerUnchanged(t)
			output, err = f.run(t, "down", nil)
			require.Error(t, err, "%s", output)
			assert.Contains(t, string(output), "refusing to delete")
		})
	}
}

func TestQuickstartRejectsBeforeClusterCreation(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{name: "preexisting-cluster", env: map[string]string{"QUICKSTART_CLUSTERS": "another-cluster\ncomposite-quickstart\n"}},
		{name: "preexisting-workload", env: map[string]string{"QUICKSTART_CLUSTERS": "composite-docker\n"}},
		{name: "windows-daemon", env: map[string]string{"QUICKSTART_PLATFORM": "windows/amd64"}},
		{name: "unsupported-arch", env: map[string]string{"QUICKSTART_PLATFORM": "linux/ppc64le"}},
		{name: "daemon-unavailable", env: map[string]string{"QUICKSTART_FAIL": "docker info"}},
		{name: "kind-list-failure", env: map[string]string{"QUICKSTART_FAIL": "kind get clusters"}},
		{name: "build-failure", env: map[string]string{"QUICKSTART_FAIL": "docker buildx build"}},
		{name: "image-run-failure", env: map[string]string{"QUICKSTART_FAIL": "docker run"}},
		{name: "wrong-image-version", env: map[string]string{"QUICKSTART_VERSION": "stale"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newQuickstartFixture(t)
			output, err := f.run(t, "up", tc.env)
			require.Error(t, err, "%s", output)
			for _, call := range f.calls(t) {
				assert.NotEqual(t, "create", call.Args[0])
				assert.NotContains(t, []string{"kubectl", "helm", "clusterctl"}, call.Tool)
			}
			_, err = os.Stat(filepath.Join(f.root, "out"))
			assert.ErrorIs(t, err, fs.ErrNotExist, "failed preflight/build must leave no managed state")
			f.assertCallerUnchanged(t)
		})
	}
}

func TestQuickstartRetainsFailedClusterForExplicitCleanup(t *testing.T) {
	for _, failure := range []string{"kind create cluster", "kind load", "clusterctl init", "helm upgrade", "kubectl --kubeconfig", "kind get kubeconfig"} {
		t.Run(failure, func(t *testing.T) {
			f := newQuickstartFixture(t)
			output, err := f.run(t, "up", map[string]string{"QUICKSTART_FAIL": failure})
			require.Error(t, err, "%s", output)
			assert.Contains(t, string(output), "make quickstart-down")
			for _, call := range f.calls(t) {
				assert.NotEqual(t, "delete", call.Args[0], "a failed startup must not silently destroy the cluster")
			}
			output, err = f.run(t, "down", map[string]string{"QUICKSTART_FAIL": "kind delete"})
			require.Error(t, err, "%s", output)
			output, err = f.run(t, "down", nil)
			require.NoError(t, err, "cleanup must remain retryable: %s", output)
			f.assertCallerUnchanged(t)
		})
	}
}

func TestQuickstartRejectsInvalidNativeEndpoint(t *testing.T) {
	f := newQuickstartFixture(t)
	output, err := f.run(t, "up", map[string]string{"QUICKSTART_ENDPOINT": "not-an-ip invalid-port"})
	require.Error(t, err, "%s", output)
	assert.Contains(t, string(output), "CAPD did not publish a valid IP/port endpoint")
	for _, call := range f.calls(t) {
		if call.Tool == "kubectl" {
			assert.NotContains(t, call.Args, "patch", "invalid native observations must not reach Composite's spec")
		}
	}
	output, err = f.run(t, "down", nil)
	require.NoError(t, err, "%s", output)
}

func TestQuickstartRefusesUnmanagedCleanupAndInvalidActions(t *testing.T) {
	for _, action := range []string{"down", "status", "unknown"} {
		t.Run(action, func(t *testing.T) {
			f := newQuickstartFixture(t)
			output, err := f.run(t, action, nil)
			require.Error(t, err, "%s", output)
			assert.Empty(t, f.calls(t))
			f.assertCallerUnchanged(t)
		})
	}
}

func TestQuickstartCleanupWaitsForNativeTeardown(t *testing.T) {
	f := newQuickstartFixture(t)
	output, err := f.run(t, "up", nil)
	require.NoError(t, err, "%s", output)
	before := len(f.calls(t))
	output, err = f.run(t, "down", map[string]string{"QUICKSTART_FAIL": "kubectl --kubeconfig"})
	require.Error(t, err, "%s", output)
	calls := f.calls(t)[before:]
	require.Len(t, calls, 1, "failed workload deletion must not destroy the management cluster")
	assert.Equal(t, "kubectl", calls[0].Tool)
	_, err = os.Stat(filepath.Join(f.root, "fake-workload"))
	require.NoError(t, err)
	output, err = f.run(t, "down", nil)
	require.NoError(t, err, "normal teardown must remain retryable: %s", output)
}

func TestQuickstartManifestReferences(t *testing.T) {
	resources := quickstartResources(t, "../local/quickstart.yaml", "../local/quickstart-templates.yaml")
	cluster := quickstartDecode[clusterv1.Cluster](t, resources["Cluster/composite-docker"])
	infra := quickstartDecode[infrav1.CompositeCluster](t, resources["CompositeCluster/composite-docker-infra"])
	provider := quickstartDecode[infrav1.CompositeProvider](t, resources["CompositeProvider/docker"])
	profile := quickstartDecode[infrav1.CompositeProviderTemplate](t, resources["CompositeProviderTemplate/docker-local-v1"])
	cp := quickstartDecode[controlplanev1.KubeadmControlPlane](t, resources["KubeadmControlPlane/composite-docker-control-plane"])
	pool := quickstartDecode[clusterv1.MachineDeployment](t, resources["MachineDeployment/composite-docker-workers"])
	assert.Equal(t, "CompositeCluster", cluster.Spec.InfrastructureRef.Kind)
	assert.Equal(t, infra.Name, cluster.Spec.InfrastructureRef.Name)
	assert.Equal(t, infra.Name, provider.Spec.ClusterRef.Name)
	assert.Equal(t, profile.Name, provider.Spec.TemplateRef.Name)
	assert.Empty(t, provider.Spec.InfraClusterName, "CAPD machines require the canonical same-name DockerCluster")
	assert.Equal(t, "DockerCluster", profile.Spec.InfraClusterKind.Kind)
	assert.Contains(t, resources, "DockerClusterTemplate/"+profile.Spec.InfrastructureTemplateRef.Name)
	assert.Contains(t, resources, "KubeadmConfigTemplate/"+profile.Spec.BootstrapTemplateRef.Name)
	assert.NotContains(t, resources, "DockerCluster/"+infra.Name, "only Composite may create its native child")
	assert.NotContains(t, resources, "KubeadmConfigTemplate/"+infra.Name+"-"+provider.Name)
	assert.Equal(t, infra.Name+"-"+provider.Name, pool.Spec.Template.Spec.Bootstrap.ConfigRef.Name)
	assert.Equal(t, cluster.Name, pool.Spec.ClusterName)
	assert.Equal(t, cluster.Name, pool.Spec.Template.Spec.ClusterName)
	assert.Equal(t, cp.Name, cluster.Spec.ControlPlaneRef.Name)
	require.NotNil(t, cp.Spec.Replicas)
	require.NotNil(t, pool.Spec.Replicas)
	assert.EqualValues(t, 1, *cp.Spec.Replicas)
	assert.EqualValues(t, 1, *pool.Spec.Replicas)
	assert.Equal(t, cp.Spec.Version, pool.Spec.Template.Spec.Version)
	assert.Contains(
		t,
		cp.Spec.KubeadmConfigSpec.ClusterConfiguration.APIServer.CertSANs,
		"0.0.0.0",
		"kind exposes CAPD's mapped LB endpoint at this address",
	)
	for _, ref := range []clusterv1.ContractVersionedObjectReference{cp.Spec.MachineTemplate.Spec.InfrastructureRef, pool.Spec.Template.Spec.InfrastructureRef} {
		assert.Equal(t, "DockerMachineTemplate", ref.Kind)
		require.Contains(t, resources, ref.Kind+"/"+ref.Name)
		image, found, err := unstructured.NestedString(resources[ref.Kind+"/"+ref.Name].Object, "spec", "template", "spec", "customImage")
		require.NoError(t, err)
		require.True(t, found)
		assert.True(t, strings.HasPrefix(image, "kindest/node:"+cp.Spec.Version+"@sha256:"))
	}
	assert.Equal(t, []string{"192.168.0.0/16"}, cluster.Spec.ClusterNetwork.Pods.CIDRBlocks)
	for _, resource := range resources {
		assert.Empty(t, resource.GetOwnerReferences(), "let CAPI/Composite establish live UID-correct ownership")
		if resource.GetKind() != "Namespace" && resource.GetKind() != "CompositeProviderTemplate" {
			assert.Equal(t, "composite-quickstart", resource.GetNamespace())
		}
	}
}

func quickstartResources(t *testing.T, paths ...string) map[string]*unstructured.Unstructured {
	t.Helper()
	resources := map[string]*unstructured.Unstructured{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		decoder := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(string(data)), 4096)
		for {
			resource := &unstructured.Unstructured{}
			err := decoder.Decode(resource)
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoError(t, err)
			key := resource.GetKind() + "/" + resource.GetName()
			require.NotContains(t, resources, key)
			resources[key] = resource
		}
	}
	return resources
}

func quickstartDecode[T any](t *testing.T, resource *unstructured.Unstructured) T {
	t.Helper()
	require.NotNil(t, resource)
	data, err := resource.MarshalJSON()
	require.NoError(t, err)
	var decoded T
	require.NoError(t, yaml.UnmarshalStrict(data, &decoded), "quickstart must use the current API wire shape")
	return decoded
}

type quickstartFixture struct{ root string }

type quickstartCall struct {
	Tool       string
	Args       []string
	Kubeconfig string
	Provider   string
	ConfigHome string
	CacheHome  string
	Topology   string
}

func newQuickstartFixture(t *testing.T) *quickstartFixture {
	t.Helper()
	f := &quickstartFixture{root: filepath.Join(t.TempDir(), "project with spaces")}
	for _, directory := range []string{"hack", "bin", "home/.kube"} {
		require.NoError(t, os.MkdirAll(filepath.Join(f.root, directory), 0o700))
	}
	script, err := os.ReadFile("quickstart.sh")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "hack", "quickstart.sh"), script, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "home", ".kube", "config"), []byte("caller-config\n"), 0o600))
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, tool := range []string{"docker", "kind", "kubectl", "clusterctl", "helm"} {
		shim := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=^TestQuickstartFakeTool$ -- " + tool + " \"$@\"\n"
		require.NoError(t, os.WriteFile(filepath.Join(f.root, "bin", tool), []byte(shim), 0o700))
	}
	return f
}

func (f *quickstartFixture) kubeconfig() string {
	return filepath.Join(f.root, "out", "quickstart", "kubeconfig")
}

func (f *quickstartFixture) run(t *testing.T, action string, overrides map[string]string) ([]byte, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "/bin/bash", filepath.Join(f.root, "hack", "quickstart.sh"), action)
	cmd.Dir = t.TempDir() // The script must resolve the repository, not rely on the caller's directory.
	env := map[string]string{
		"PATH": filepath.Join(f.root, "bin") + ":/usr/bin:/bin", "HOME": filepath.Join(f.root, "home"),
		"KUBECONFIG": filepath.Join(f.root, "home", ".kube", "config"), "KIND_EXPERIMENTAL_PROVIDER": "podman",
		"QUICKSTART_FAKE_TOOL": "1", "QUICKSTART_ROOT": f.root, "GORACE": "atexit_sleep_ms=0",
		"QUICKSTART_PLATFORM": "linux/amd64", "QUICKSTART_VERSION": "quickstart", "QUICKSTART_ENDPOINT": "192.0.2.10 6443",
	}
	maps.Copy(env, overrides)
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("running quickstart %s: %w", action, err)
	}
	return output, nil
}

func (f *quickstartFixture) calls(t *testing.T) []quickstartCall {
	t.Helper()
	file, err := os.Open(filepath.Join(f.root, "calls.jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	var calls []quickstartCall
	decoder := json.NewDecoder(file)
	for {
		var call quickstartCall
		err := decoder.Decode(&call)
		if errors.Is(err, io.EOF) {
			return calls
		}
		require.NoError(t, err)
		calls = append(calls, call)
	}
}

func (f *quickstartFixture) assertCallerUnchanged(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, "home", ".kube", "config"))
	require.NoError(t, err)
	assert.Equal(t, "caller-config\n", string(data))
}

func quickstartFindCall(t *testing.T, calls []quickstartCall, tool string, prefix ...string) quickstartCall {
	t.Helper()
	for _, call := range calls {
		if call.Tool == tool && len(call.Args) >= len(prefix) && slices.Equal(call.Args[:len(prefix)], prefix) {
			return call
		}
	}
	t.Fatalf("missing %s %v in calls: %+v", tool, prefix, calls)
	return quickstartCall{}
}

// Re-execute this test binary behind PATH shims: no real daemon, cluster or network
// is reachable through these fakes. Unknown operations fail closed.
func TestQuickstartFakeTool(t *testing.T) {
	if os.Getenv("QUICKSTART_FAKE_TOOL") != "1" {
		return
	}
	separator := slices.Index(os.Args, "--")
	require.GreaterOrEqual(t, separator, 0)
	tool, args := os.Args[separator+1], os.Args[separator+2:]
	require.NotEmpty(t, args)
	root := os.Getenv("QUICKSTART_ROOT")
	call := quickstartCall{
		Tool: tool, Args: args, Kubeconfig: os.Getenv("KUBECONFIG"), Provider: os.Getenv("KIND_EXPERIMENTAL_PROVIDER"),
		ConfigHome: os.Getenv("XDG_CONFIG_HOME"), CacheHome: os.Getenv("XDG_CACHE_HOME"), Topology: os.Getenv("CLUSTER_TOPOLOGY"),
	}
	file, err := os.OpenFile(filepath.Join(root, "calls.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	require.NoError(t, json.NewEncoder(file).Encode(call))
	require.NoError(t, file.Close())
	if failure := os.Getenv("QUICKSTART_FAIL"); failure != "" && strings.HasPrefix(tool+" "+strings.Join(args, " "), failure) {
		os.Exit(23)
	}
	quickstartFakeOperation(t, root, tool, args)
	os.Exit(0)
}

func quickstartFakeOperation(t *testing.T, root, tool string, args []string) {
	t.Helper()
	switch tool {
	case "docker":
		switch args[0] {
		case "info":
			fmt.Println(os.Getenv("QUICKSTART_PLATFORM"))
		case "run":
			fmt.Println(os.Getenv("QUICKSTART_VERSION"))
		case "buildx":
			require.Contains(t, []string{"version", "build"}, args[1])
		default:
			t.Fatalf("unexpected Docker operation: %v", args)
		}
	case "kind":
		quickstartFakeKind(t, root, args)
	case "clusterctl":
		require.Equal(t, "init", args[0])
	case "helm":
		require.Equal(t, "upgrade", args[0])
	case "kubectl":
		quickstartFakeKubectl(t, root, args)
	default:
		t.Fatalf("unexpected quickstart tool: %s", tool)
	}
}

func quickstartKubectlArgs(t *testing.T, root string, args []string) []string {
	t.Helper()
	require.GreaterOrEqual(t, len(args), 5)
	if args[1] == filepath.Join(root, "out", "quickstart", "workload.kubeconfig") {
		require.Equal(t, []string{"--kubeconfig", args[1], "--context", "kind-composite-docker"}, args[:4])
	} else {
		require.Equal(
			t,
			[]string{"--kubeconfig", filepath.Join(root, "out", "quickstart", "kubeconfig"), "--context", "kind-composite-quickstart"},
			args[:4],
		)
	}
	args = args[4:]
	if args[0] == "-n" {
		args = args[2:]
	}
	return args
}

func quickstartFakeKubectl(t *testing.T, root string, args []string) {
	t.Helper()
	args = quickstartKubectlArgs(t, root, args)
	switch args[0] {
	case "get":
		if args[1] == "dockercluster/composite-docker-infra" {
			fmt.Println(os.Getenv("QUICKSTART_ENDPOINT"))
		}
	case "apply":
		if slices.Contains(args, filepath.Join(root, "local", "quickstart.yaml")) {
			require.NoError(t, os.WriteFile(filepath.Join(root, "fake-workload"), []byte("native-containers"), 0o600))
		}
	case "delete":
		require.Equal(t, "cluster/composite-docker", args[1], "cleanup must go through the owning CAPI Cluster")
		err := os.Remove(filepath.Join(root, "fake-workload"))
		if !errors.Is(err, fs.ErrNotExist) {
			require.NoError(t, err)
		}
	case "patch":
		require.Equal(t, "compositecluster/composite-docker-infra", args[1], "endpoint publication must not mutate native sources or pins")
	case "wait":
		quickstartFakeWait(t, root, args)
	case "rollout":
	default:
		t.Fatalf("unexpected kubectl operation: %v", args)
	}
}

func quickstartFakeWait(t *testing.T, root string, args []string) {
	t.Helper()
	if !slices.Contains(args, "dockercluster/composite-docker-infra") {
		return
	}
	if slices.Contains(args, "--for=create") {
		require.NoError(t, os.WriteFile(filepath.Join(root, "fake-native-created"), []byte("created"), 0o600))
		return
	}
	_, err := os.Stat(filepath.Join(root, "fake-native-created"))
	require.NoError(t, err, "the native child is created asynchronously; wait for existence before reading it")
}

func quickstartFakeKind(t *testing.T, root string, args []string) {
	t.Helper()
	switch args[0] {
	case "get":
		switch args[1] {
		case "clusters":
			fmt.Print(os.Getenv("QUICKSTART_CLUSTERS"))
			if _, err := os.Stat(filepath.Join(root, "fake-cluster")); err == nil {
				fmt.Println("composite-quickstart")
			}
		case "kubeconfig":
			require.Equal(t, []string{"get", "kubeconfig", "--name", "composite-docker"}, args)
			fmt.Println("workload-config")
		default:
			t.Fatalf("unexpected kind get operation: %v", args)
		}
	case "create":
		require.NoError(t, os.WriteFile(filepath.Join(root, "fake-cluster"), []byte("node"), 0o600))
		require.NoError(t, os.WriteFile(os.Getenv("KUBECONFIG"), []byte("quickstart-config\n"), 0o600))
	case "load":
		require.Equal(t, []string{"load", "docker-image", "composite:quickstart", "--name", "composite-quickstart"}, args)
	case "delete":
		_, workloadErr := os.Stat(filepath.Join(root, "fake-workload"))
		require.ErrorIs(t, workloadErr, fs.ErrNotExist, "destroying management first leaks sibling CAPD containers")
		err := os.Remove(filepath.Join(root, "fake-cluster"))
		if !errors.Is(err, fs.ErrNotExist) {
			require.NoError(t, err)
		}
	default:
		t.Fatalf("unexpected kind operation: %v", args)
	}
}
