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

package controller

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	infrav1 "github.com/numspot/cluster-api-provider-composite/api/v1beta2"
)

func TestConditionLastTransitionTime(t *testing.T) {
	t.Parallel()
	old := metav1.NewTime(time.Unix(1, 0))
	foreign := metav1.Condition{
		Type: "OtherController", Status: metav1.ConditionUnknown,
		Reason: "Waiting", Message: "not ours", ObservedGeneration: 1, LastTransitionTime: old,
	}
	for _, status := range []metav1.ConditionStatus{metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionUnknown} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			conds := []metav1.Condition{foreign, {
				Type: infrav1.ReadyCondition, Status: status,
				Reason: "Before", Message: "before", ObservedGeneration: 1, LastTransitionTime: old,
			}}
			setCondition(&conds, 2, infrav1.ReadyCondition, status, "After", "after")
			got := apimeta.FindStatusCondition(conds, infrav1.ReadyCondition)
			require.NotNil(t, got)
			assert.Equal(t, old, got.LastTransitionTime, "reason, message and generation are not status transitions")
			assert.Equal(t, int64(2), got.ObservedGeneration)
			assert.Equal(t, "After", got.Reason)
			assert.Equal(t, "after", got.Message)
			before := append([]metav1.Condition(nil), conds...)
			setCondition(&conds, 2, infrav1.ReadyCondition, status, "After", "after")
			assert.Equal(t, before, conds, "identical observations must be a no-op")
			next := metav1.ConditionTrue
			if status == next {
				next = metav1.ConditionUnknown
			}
			setCondition(&conds, 2, infrav1.ReadyCondition, next, "After", "after")
			got = apimeta.FindStatusCondition(conds, infrav1.ReadyCondition)
			assert.True(t, got.LastTransitionTime.After(old.Time))
			assert.Equal(t, next, got.Status)
			assert.Equal(t, &foreign, apimeta.FindStatusCondition(conds, foreign.Type))
			assert.Len(t, conds, 2)
		})
	}
	var conds []metav1.Condition
	setTrue(&conds, 1, infrav1.ReadyCondition)
	require.Len(t, conds, 1)
	assert.False(t, conds[0].LastTransitionTime.IsZero())
}

func TestConditionDiagnosticsAreBounded(t *testing.T) {
	t.Parallel()
	for _, text := range []string{strings.Repeat("x", messageLimit*2), strings.Repeat("\u754c", messageLimit)} {
		clipped := clip(text)
		assert.LessOrEqual(t, len(clipped), messageLimit)
		assert.True(t, utf8.ValidString(clipped))
		assert.Equal(t, clipped, clip(clipped), "clipping must be idempotent")
		var conds []metav1.Condition
		setFalse(&conds, 1, infrav1.ProvidersReadyCondition, infrav1.WaitingForProvidersReason, text)
		require.Len(t, conds, 1)
		assert.Equal(t, clipped, conds[0].Message, "all condition writers must bound joined diagnostics")
	}
	assert.Equal(t, "short", clip("short"))
}
