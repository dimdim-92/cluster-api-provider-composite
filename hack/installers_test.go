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
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func TestInstallersNeedNoWatchListConfiguration(t *testing.T) {
	t.Run("clusterctl", func(t *testing.T) {
		data, err := os.ReadFile("../config/manager/manager.yaml")
		require.NoError(t, err)
		assertNoWatchListConfiguration(t, installerPodSpec(t, data))
	})

	for _, tc := range []struct {
		name      string
		args      []string
		wantError bool
	}{
		{name: "helm-default"},
		{name: "helm-no-opt-in", args: []string{"--set=watchListClient=true"}, wantError: true},
		{
			name: "helm-extra-env",
			args: []string{"--set-string=extraEnv[0].name=EXAMPLE", "--set-string=extraEnv[0].value=custom"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			helm := os.Getenv("HELM")
			if helm == "" {
				helm = "helm"
			}
			args := append([]string{"template", "composite", "../helm", "--namespace=composite-system", "--show-only=templates/app.yaml"}, tc.args...)
			cmd := exec.CommandContext(t.Context(), helm, args...)
			output, err := cmd.CombinedOutput()
			if tc.wantError {
				require.Error(t, err)
				assert.Contains(t, string(output), "watchListClient")
				return
			}
			require.NoError(t, err, "rendering Helm deployment: %s", output)
			assertNoWatchListConfiguration(t, installerPodSpec(t, output))
		})
	}
}

func installerPodSpec(t *testing.T, data []byte) *corev1.PodSpec {
	t.Helper()
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var resource struct {
			Kind string `json:"kind"`
			Spec struct {
				Template corev1.PodTemplateSpec `json:"template"`
			} `json:"spec"`
		}
		err := decoder.Decode(&resource)
		if errors.Is(err, io.EOF) {
			t.Fatal("installer contains no Deployment")
		}
		require.NoError(t, err)
		if resource.Kind == "Deployment" {
			return &resource.Spec.Template.Spec
		}
	}
}

func assertNoWatchListConfiguration(t *testing.T, pod *corev1.PodSpec) {
	t.Helper()
	for i := range pod.Containers {
		container := &pod.Containers[i]
		if container.Name != "manager" {
			continue
		}
		for _, env := range container.Env {
			assert.NotEqual(t, "KUBE_FEATURE_WatchListClient", env.Name, "WatchList policy belongs in the binary, not installer configuration")
		}
		return
	}
	t.Fatal("installer contains no manager container")
}
