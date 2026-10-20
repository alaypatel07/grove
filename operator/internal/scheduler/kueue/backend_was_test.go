// /*
// Copyright 2026 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

package kueue

import (
	"context"
	"fmt"
	"testing"

	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	kueuev1beta2 "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

const wasTestRackKey = "kubernetes.io/rack"

func newWASTestBackend(t *testing.T, config string) (scheduler.Backend, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, grovecorev1alpha1.AddToScheme(scheme))
	require.NoError(t, groveschedulerv1alpha1.AddToScheme(scheme))
	pcs := &grovecorev1alpha1.PodCliqueSet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default", Labels: map[string]string{queueNameLabel: "poc"}},
		Spec: grovecorev1alpha1.PodCliqueSetSpec{Template: grovecorev1alpha1.PodCliqueSetTemplateSpec{
			Cliques: []*grovecorev1alpha1.PodCliqueTemplateSpec{
				{Name: "prefill", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2}},
				{Name: "decode", Spec: grovecorev1alpha1.PodCliqueSpec{Replicas: 2}},
			},
		}},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		pcs,
		newStandalonePodClique("demo-0-prefill", "demo"),
		newStandalonePodClique("demo-0-decode", "demo"),
	).Build()
	b := New(cl, scheme, record.NewFakeRecorder(10), configv1alpha1.SchedulerProfile{
		Name:   configv1alpha1.SchedulerNameKueue,
		Config: &runtime.RawExtension{Raw: []byte(config)},
	})
	require.NoError(t, b.Init(cl))
	return b, cl
}

// wasTestPodGang is a two-clique PodGang whose rack constraint is gang-wide, the case Kueue's TAS places per PodSet.
func wasTestPodGang() *groveschedulerv1alpha1.PodGang {
	rackKey := wasTestRackKey
	return &groveschedulerv1alpha1.PodGang{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-0", Namespace: "default", UID: "demo-0-uid",
			Labels: map[string]string{apicommon.LabelPartOfKey: "demo"},
		},
		Spec: groveschedulerv1alpha1.PodGangSpec{
			TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
				PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &rackKey},
			},
			PodGroups: []groveschedulerv1alpha1.PodGroup{
				{Name: "demo-0-prefill", MinReplicas: 2},
				{Name: "demo-0-decode", MinReplicas: 2},
			},
		},
	}
}

func TestBackend_WASMode_SyncPodGang(t *testing.T) {
	tests := []struct {
		mode             string
		wantKueueRackKey bool
		wantRootKeys     []schedulingv1alpha3.TopologyConstraint
	}{
		{mode: wasModeGangOnly, wantKueueRackKey: true},
		{mode: wasModeFull, wantKueueRackKey: true, wantRootKeys: []schedulingv1alpha3.TopologyConstraint{{Key: wasTestRackKey}}},
		{mode: wasModeWASTopology, wantRootKeys: []schedulingv1alpha3.TopologyConstraint{{Key: wasTestRackKey}}},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			b, cl := newWASTestBackend(t, fmt.Sprintf(`{"wasMode":%q}`, tt.mode))
			require.NoError(t, b.SyncPodGang(context.Background(), wasTestPodGang()))

			// Kueue's TAS gets one required rack per PodSet unless kube-scheduler alone owns topology.
			kueueWorkload := &kueuev1beta2.Workload{}
			require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "demo-0"}, kueueWorkload))
			require.Len(t, kueueWorkload.Spec.PodSets, 2)
			for _, podSet := range kueueWorkload.Spec.PodSets {
				if !tt.wantKueueRackKey {
					assert.Nil(t, podSet.TopologyRequest)
					assert.Empty(t, podSet.Template.Annotations)
					continue
				}
				require.NotNil(t, podSet.TopologyRequest)
				assert.Equal(t, wasTestRackKey, *podSet.TopologyRequest.Required)
			}

			root := &schedulingv1alpha3.CompositePodGroup{}
			require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "demo-0"}, root))
			require.NotNil(t, root.Spec.SchedulingPolicy.Gang)
			assert.Equal(t, int32(2), root.Spec.SchedulingPolicy.Gang.MinGroupCount)
			var rootKeys []schedulingv1alpha3.TopologyConstraint
			if root.Spec.SchedulingConstraints != nil {
				rootKeys = root.Spec.SchedulingConstraints.Topology
			}
			assert.Equal(t, tt.wantRootKeys, rootKeys)

			leaves := &schedulingv1beta1.PodGroupList{}
			require.NoError(t, cl.List(context.Background(), leaves))
			require.Len(t, leaves.Items, 2)
			for _, leaf := range leaves.Items {
				require.NotNil(t, leaf.Spec.SchedulingPolicy.Gang)
				assert.Equal(t, int32(2), leaf.Spec.SchedulingPolicy.Gang.MinCount)
				assert.Equal(t, "demo-0", *leaf.Spec.ParentCompositePodGroupName)
			}
		})
	}
}

func TestBackend_WASMode_PreparePodSetsSchedulingGroup(t *testing.T) {
	b, cl := newWASTestBackend(t, `{"wasMode":"GangOnly"}`)
	podGang := wasTestPodGang()
	require.NoError(t, cl.Create(context.Background(), podGang))
	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Labels: map[string]string{
		apicommon.LabelPartOfKey: "demo",
		apicommon.LabelPodClique: "demo-0-prefill",
		apicommon.LabelPodGang:   "demo-0",
	}}}
	require.NoError(t, b.PreparePod(pod))

	leaves := &schedulingv1beta1.PodGroupList{}
	require.NoError(t, cl.List(context.Background(), leaves, client.MatchingLabels{apicommon.LabelPodClique: "demo-0-prefill"}))
	require.Len(t, leaves.Items, 1)
	require.NotNil(t, pod.Spec.SchedulingGroup)
	assert.Equal(t, leaves.Items[0].Name, *pod.Spec.SchedulingGroup.PodGroupName)
	assert.Equal(t, string(configv1alpha1.SchedulerNameKube), pod.Spec.SchedulerName)
	assert.Equal(t, "demo-0", pod.Labels[prebuiltWorkloadNameLabel])
}

func TestBackend_WASMode_InitRejectsUnsupportedConfig(t *testing.T) {
	for config, wantErr := range map[string]string{
		`{"wasMode":"Partial"}`: `unsupported kueue wasMode "Partial"`,
		`{"wasMode":"Full","underlyingSchedulerName":"kai-scheduler"}`: `requires underlyingSchedulerName "default-scheduler"`,
	} {
		t.Run(config, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()
			b := New(cl, runtime.NewScheme(), nil, configv1alpha1.SchedulerProfile{
				Name:   configv1alpha1.SchedulerNameKueue,
				Config: &runtime.RawExtension{Raw: []byte(config)},
			})
			require.ErrorContains(t, b.Init(cl), wantErr)
		})
	}
}
