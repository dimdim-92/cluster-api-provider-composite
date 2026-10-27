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
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type runtimeConflictClient struct {
	client.Client
	reads   int
	updates int
}

func (c *runtimeConflictClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	c.reads++
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return fmt.Errorf("reading conflict fixture: %w", err)
	}
	return nil
}

func (c *runtimeConflictClient) Status() client.SubResourceWriter {
	return runtimeConflictStatus{SubResourceWriter: c.Client.Status(), parent: c}
}

type runtimeConflictStatus struct {
	client.SubResourceWriter
	parent *runtimeConflictClient
}

func (s runtimeConflictStatus) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	s.parent.updates++
	if s.parent.updates == 1 {
		// Emulate the operator changing metadata between the native provider's
		// read and status update. The retry must observe the new resourceVersion.
		current := &unstructured.Unstructured{}
		current.SetGroupVersionKind(fakeClusterGVK)
		if err := s.parent.Client.Get(ctx, client.ObjectKeyFromObject(obj), current); err != nil {
			return fmt.Errorf("reading concurrent fixture: %w", err)
		}
		current.SetAnnotations(map[string]string{"tests.example.org/concurrent-controller": "preserve"})
		if err := s.parent.Update(ctx, current); err != nil {
			return fmt.Errorf("simulating concurrent controller: %w", err)
		}
		return fmt.Errorf("simulating stale status write: %w", apierrors.NewConflict(
			schema.GroupResource{Group: fakeClusterGVK.Group, Resource: "fakeclusters"}, obj.GetName(), errors.New("concurrent reconciliation"),
		))
	}
	if err := s.SubResourceWriter.Update(ctx, obj, opts...); err != nil {
		return fmt.Errorf("writing refreshed fixture status: %w", err)
	}
	return nil
}

func TestOperatorRuntimeStatusRefreshesAfterConcurrentWrite(t *testing.T) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(fakeClusterGVK, &unstructured.Unstructured{})
	child := &unstructured.Unstructured{}
	child.SetGroupVersionKind(fakeClusterGVK)
	child.SetName("concurrent-status")
	child.SetNamespace("test")
	child.Object["status"] = map[string]any{"ready": false}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(child).WithStatusSubresource(child).Build()
	c := &runtimeConflictClient{Client: base}
	markRuntimeChildReady(t, c, child)
	assert.Equal(t, 2, c.updates)
	assert.Equal(t, 2, c.reads, "a conflict must trigger a new authoritative read")
	fresh := &unstructured.Unstructured{}
	fresh.SetGroupVersionKind(fakeClusterGVK)
	require.NoError(t, base.Get(t.Context(), client.ObjectKeyFromObject(child), fresh))
	ready, found, err := unstructured.NestedBool(fresh.Object, "status", "ready")
	require.NoError(t, err)
	assert.True(t, found && ready)
	assert.Equal(t, "preserve", fresh.GetAnnotations()["tests.example.org/concurrent-controller"])
}
