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
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	certutil "k8s.io/client-go/util/cert"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

func TestOperatorRuntime(t *testing.T) {
	installation, c := operatorEnvironment(t)
	binary := filepath.Join(t.TempDir(), "operator")
	build := exec.CommandContext(t.Context(), "go", "build", "-race", "-o", binary, "./cmd/operator")
	build.Dir = filepath.Join("..", "..")
	out, err := build.CombinedOutput()
	require.NoError(t, err, "building the real operator: %s", out)
	t.Run("metrics-security", func(t *testing.T) {
		testOperatorMetrics(t, installation, c, binary)
	})
	t.Run("scope", func(t *testing.T) { testOperatorScope(t, installation, c, binary) })
	t.Run("all-namespaces", func(t *testing.T) { testOperatorAllNamespaces(t, installation, c, binary) })
	t.Run("leadership", func(t *testing.T) { testOperatorLeadership(t, installation, c, binary) })
	t.Run("custom-group-permissions", func(t *testing.T) { testOperatorCustomGroup(t, installation, c, binary) })
	t.Run("owner-event-group", func(t *testing.T) { testOperatorOwnerEventGroup(t, installation, c, binary) })
	t.Run("profile-catalog", func(t *testing.T) { testOperatorProfiles(t, installation, c, binary) })
	t.Run("provider-attachments", func(t *testing.T) { testOperatorProviderAttachments(t, installation, c, binary) })
}

func operatorEnvironment(t *testing.T) (*envtest.Environment, client.Client) {
	t.Helper()
	capiPath, err := capiCRDPath(t.Context())
	require.NoError(t, err)
	installation := &envtest.Environment{
		UseExistingCluster: new(false), Scheme: k8s.Scheme(), ErrorIfCRDPathMissing: true,
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "api", "config", "crd", "bases"), capiPath,
			filepath.Join(capiPath, "..", "..", "..", "bootstrap", "kubeadm", "config", "crd", "bases"),
			filepath.Join("testdata", "crds"),
		},
	}
	config, err := installation.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, installation.Stop()) })
	c, err := client.New(config, client.Options{Scheme: k8s.Scheme()})
	require.NoError(t, err)
	role := operatorRole(t, "role.yaml")
	require.NoError(t, c.Create(t.Context(), role))
	return installation, c
}

func operatorRole(t *testing.T, filename string) *rbacv1.ClusterRole {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", filename))
	require.NoError(t, err)
	role := &rbacv1.ClusterRole{}
	require.NoError(t, yaml.Unmarshal(data, role))
	return role
}

func operatorNamespace(t *testing.T, c client.Client, prefix string) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "runtime-" + prefix + "-"}}
	require.NoError(t, c.Create(t.Context(), ns))
	return ns.Name
}

func operatorCredentials(t *testing.T, installation *envtest.Environment, c client.Client, namespace string) string {
	t.Helper()
	username := "operator-" + namespace
	user, err := installation.AddUser(envtest.User{Name: username}, nil)
	require.NoError(t, err)
	role := operatorRole(t, "role.yaml")
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: username},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
		Subjects:   []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: username}},
	}
	require.NoError(t, c.Create(t.Context(), binding))
	limited, err := client.New(user.Config(), client.Options{Scheme: k8s.Scheme()})
	require.NoError(t, err)
	err = limited.Get(t.Context(), client.ObjectKey{Namespace: namespace, Name: "not-readable"}, &corev1.Secret{})
	require.True(t, apierrors.IsForbidden(err), "the operator must not run as admin or read arbitrary secrets: %v", err)
	data, err := user.KubeConfig()
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "operator.config")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

type operatorOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (o *operatorOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := o.buffer.Write(p)
	if err != nil {
		return n, fmt.Errorf("buffering operator output: %w", err)
	}
	return n, nil
}

func (o *operatorOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.String()
}

type operatorProcess struct {
	command *exec.Cmd
	output  *operatorOutput
	done    chan error
	health  string
	stopped bool
}

func startOperator(t *testing.T, binary, credentials, namespace string, extra ...string) *operatorProcess {
	t.Helper()
	p := &operatorProcess{output: &operatorOutput{}, done: make(chan error, 1), health: operatorAddress(t)}
	args := append([]string{
		"--namespace=" + namespace, "--leader-elect=false", "--diagnostics-address=0", "--health-addr=" + p.health,
		"--v=1",
	}, extra...)
	// Cleanup requests graceful termination before canceling the command context.
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	t.Cleanup(cancel)
	p.command = exec.CommandContext(ctx, binary, args...)
	// No workstation kubeconfig, proxy, credential plugin or operator defaults are inherited.
	p.command.Env = []string{"KUBECONFIG=" + credentials}
	p.command.Stdout, p.command.Stderr = p.output, p.output
	require.NoError(t, p.command.Start())
	go func() { p.done <- p.command.Wait() }()
	t.Cleanup(func() { stopOperator(t, p) })
	httpClient := &http.Client{Timeout: time.Second}
	ready := false
	require.Eventually(t, func() bool {
		select {
		case err := <-p.done:
			p.done <- err
			return true
		default:
		}
		response, err := operatorGet(t.Context(), httpClient, "http://"+p.health+"/ready")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		ready = response.StatusCode == http.StatusOK
		return ready
	}, 30*time.Second, 100*time.Millisecond, "operator did not become ready")
	require.True(t, ready, "operator exited before readiness: %s", p.output.String())
	return p
}

func stopOperator(t *testing.T, p *operatorProcess) {
	t.Helper()
	if p.stopped {
		return
	}
	p.stopped = true
	if err := p.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Logf("operator already stopped: %v", err)
	}
	select {
	case err := <-p.done:
		assert.NoError(t, err, "operator exited unsuccessfully: %s", p.output.String())
	case <-time.After(15 * time.Second):
		_ = p.command.Process.Kill()
		t.Logf("operator stopped after forced kill: %v", <-p.done)
		t.Error("operator did not shut down gracefully")
	}
}

func operatorAddress(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}

func operatorGet(ctx context.Context, c *http.Client, url string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("creating probe request: %w", err)
	}
	response, err := c.Do(request)
	if err != nil {
		return nil, fmt.Errorf("requesting operator endpoint: %w", err)
	}
	return response, nil
}

func testOperatorMetrics(t *testing.T, installation *envtest.Environment, c client.Client, binary string) {
	ns := operatorNamespace(t, c, "metrics")
	credentials := operatorCredentials(t, installation, c, ns)
	cert, key, err := certutil.GenerateSelfSignedCertKey("127.0.0.1", nil, nil)
	require.NoError(t, err)
	certDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(certDir, "tls.crt"), cert, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(certDir, "tls.key"), key, 0o600))
	metrics := operatorAddress(t)
	p := startOperator(t, binary, credentials, ns, "--diagnostics-address="+metrics, "--metrics-cert-dir="+certDir, "--v=4")
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(cert))
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	https := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for _, probe := range []string{"/ping", "/ready"} {
		response, err := operatorGet(t.Context(), https, "http://"+p.health+probe)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, response.StatusCode)
		require.NoError(t, response.Body.Close())
	}
	reader := operatorRole(t, "metrics-reader.yaml")
	require.NoError(t, c.Create(t.Context(), reader))
	allowed := metricsToken(t, installation, c, ns, "allowed")
	denied := metricsToken(t, installation, c, ns, "denied")
	require.NoError(t, c.Create(t.Context(), &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "metrics-" + ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: reader.Name},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: ns, Name: "allowed"}},
	}))
	for _, tc := range []struct {
		name, token string
		status      int
	}{
		// The pinned delegated authenticator reports malformed tokens as a fail-closed 500.
		{"anonymous", "", http.StatusUnauthorized},
		{"invalid", "not-a-real-token", http.StatusInternalServerError},
		{"denied", denied, http.StatusForbidden},
		{"allowed", allowed, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+metrics+"/metrics", http.NoBody)
			require.NoError(t, err)
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			response, err := https.Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			assert.Equal(t, tc.status, response.StatusCode)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			if tc.status == http.StatusOK {
				assert.Contains(t, string(body), "go_goroutines")
			}
		})
	}
	legacyTransport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}}
	t.Cleanup(legacyTransport.CloseIdleConnections)
	legacy := &http.Client{Transport: legacyTransport, Timeout: time.Second}
	response, err := operatorGet(t.Context(), legacy, "https://"+metrics+"/metrics")
	if response != nil {
		require.NoError(t, response.Body.Close())
	}
	require.Error(t, err, "TLS below the configured floor must fail")
	untrustedTransport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	t.Cleanup(untrustedTransport.CloseIdleConnections)
	untrusted := &http.Client{Transport: untrustedTransport, Timeout: time.Second}
	response, err = operatorGet(t.Context(), untrusted, "https://"+metrics+"/metrics")
	if response != nil {
		require.NoError(t, response.Body.Close())
	}
	require.Error(t, err, "the metrics certificate must require explicit trust")
	response, err = operatorGet(t.Context(), &http.Client{Timeout: time.Second}, "http://"+metrics+"/metrics")
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, response.StatusCode, "plaintext must not reach the metrics handler")
	require.NoError(t, response.Body.Close())
	assert.NotContains(t, p.output.String(), allowed)
	assert.NotContains(t, p.output.String(), denied)
	assert.NotContains(t, p.output.String(), "not-a-real-token")
}

func metricsToken(t *testing.T, installation *envtest.Environment, c client.Client, namespace, name string) string {
	t.Helper()
	require.NoError(t, c.Create(t.Context(), &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}))
	clientset, err := kubernetes.NewForConfig(installation.Config)
	require.NoError(t, err)
	token, err := clientset.CoreV1().ServiceAccounts(namespace).CreateToken(t.Context(), name,
		&authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: new(int64(600))}}, metav1.CreateOptions{})
	require.NoError(t, err)
	return token.Status.Token
}
