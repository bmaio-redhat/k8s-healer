package util

import (
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsClusterUnderStrain(t *testing.T) {
	tests := []struct {
		name      string
		nodes     []*v1.Node
		threshold float64
		expected  ClusterStrainInfo
	}{
		{
			name: "no strain - all nodes healthy",
			nodes: []*v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
					Status: v1.NodeStatus{
						Conditions: []v1.NodeCondition{
							{Type: v1.NodeReady, Status: v1.ConditionTrue},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "node-2"},
					Status: v1.NodeStatus{
						Conditions: []v1.NodeCondition{
							{Type: v1.NodeReady, Status: v1.ConditionTrue},
						},
					},
				},
			},
			threshold: 30.0,
			expected: ClusterStrainInfo{
				StrainedNodesCount: 0,
				TotalNodesCount:    2,
				StrainPercentage:   0.0,
				NodesUnderPressure: []string{},
				HasStrain:          false,
			},
		},
		{
			name: "strain detected - 50% nodes under pressure",
			nodes: []*v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
					Status: v1.NodeStatus{
						Conditions: []v1.NodeCondition{
							{Type: v1.NodeReady, Status: v1.ConditionTrue},
							{Type: v1.NodeMemoryPressure, Status: v1.ConditionTrue},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "node-2"},
					Status: v1.NodeStatus{
						Conditions: []v1.NodeCondition{
							{Type: v1.NodeReady, Status: v1.ConditionTrue},
						},
					},
				},
			},
			threshold: 30.0,
			expected: ClusterStrainInfo{
				StrainedNodesCount: 1,
				TotalNodesCount:    2,
				StrainPercentage:   50.0,
				NodesUnderPressure: []string{"node-1"},
				HasStrain:          true,
			},
		},
		{
			name: "no strain - below threshold",
			nodes: []*v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
					Status: v1.NodeStatus{
						Conditions: []v1.NodeCondition{
							{Type: v1.NodeReady, Status: v1.ConditionTrue},
							{Type: v1.NodeMemoryPressure, Status: v1.ConditionTrue},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "node-2"},
					Status: v1.NodeStatus{
						Conditions: []v1.NodeCondition{
							{Type: v1.NodeReady, Status: v1.ConditionTrue},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "node-3"},
					Status: v1.NodeStatus{
						Conditions: []v1.NodeCondition{
							{Type: v1.NodeReady, Status: v1.ConditionTrue},
						},
					},
				},
			},
			threshold: 50.0,
			expected: ClusterStrainInfo{
				StrainedNodesCount: 1,
				TotalNodesCount:    3,
				StrainPercentage:   33.33,
				NodesUnderPressure: []string{"node-1"},
				HasStrain:          false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsClusterUnderStrain(tt.nodes, tt.threshold)
			if result.HasStrain != tt.expected.HasStrain {
				t.Errorf("IsClusterUnderStrain().HasStrain = %v, want %v", result.HasStrain, tt.expected.HasStrain)
			}
			if result.StrainedNodesCount != tt.expected.StrainedNodesCount {
				t.Errorf("IsClusterUnderStrain().StrainedNodesCount = %v, want %v", result.StrainedNodesCount, tt.expected.StrainedNodesCount)
			}
			if result.TotalNodesCount != tt.expected.TotalNodesCount {
				t.Errorf("IsClusterUnderStrain().TotalNodesCount = %v, want %v", result.TotalNodesCount, tt.expected.TotalNodesCount)
			}
			if len(result.NodesUnderPressure) != len(tt.expected.NodesUnderPressure) {
				t.Errorf("IsClusterUnderStrain().NodesUnderPressure length = %v, want %v", len(result.NodesUnderPressure), len(tt.expected.NodesUnderPressure))
			}
		})
	}
}

func TestIsPodResourceConstrained(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		pod      *v1.Pod
		expected bool
	}{
		{
			name: "healthy pod",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "healthy-pod", Namespace: "default"},
				Status: v1.PodStatus{
					Phase: v1.PodRunning,
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:         "container",
							RestartCount: 0,
							State: v1.ContainerState{
								Running: &v1.ContainerStateRunning{},
							},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "pod with OOMKilled",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "oom-pod", Namespace: "default"},
				Status: v1.PodStatus{
					Phase: v1.PodRunning,
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:         "container",
							RestartCount: 1,
							LastTerminationState: v1.ContainerState{
								Terminated: &v1.ContainerStateTerminated{
									Reason: "OOMKilled",
								},
							},
							State: v1.ContainerState{
								Running: &v1.ContainerStateRunning{},
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "pod with high restart count",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "restart-pod", Namespace: "default"},
				Status: v1.PodStatus{
					Phase: v1.PodRunning,
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:         "container",
							RestartCount: 7,
							State: v1.ContainerState{
								Running: &v1.ContainerStateRunning{},
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "pod stuck in Pending",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "pending-pod",
					Namespace:         "default",
					CreationTimestamp: metav1.Time{Time: now.Add(-10 * time.Minute)},
				},
				Status: v1.PodStatus{
					Phase: v1.PodPending,
					Conditions: []v1.PodCondition{
						{
							Type:   v1.PodScheduled,
							Status: v1.ConditionFalse,
							Reason: "InsufficientMemory",
						},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsPodResourceConstrained(tt.pod)
			if result.HasIssue != tt.expected {
				t.Errorf("IsPodResourceConstrained().HasIssue = %v, want %v", result.HasIssue, tt.expected)
			}
		})
	}
}

func TestGetPodPriority(t *testing.T) {
	tests := []struct {
		name     string
		pod      *v1.Pod
		expected int
	}{
		{
			name: "system-cluster-critical pod",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					PriorityClassName: "system-cluster-critical",
				},
			},
			expected: 100,
		},
		{
			name: "system-node-critical pod",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					PriorityClassName: "system-node-critical",
				},
			},
			expected: 90,
		},
		{
			name: "high priority pod",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					PriorityClassName: "high",
				},
			},
			expected: 70,
		},
		{
			name: "default priority pod",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					PriorityClassName: "",
				},
			},
			expected: 30,
		},
		{
			name: "low priority pod",
			pod: &v1.Pod{
				Spec: v1.PodSpec{
					PriorityClassName: "low",
				},
			},
			expected: 20,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetPodPriority(tt.pod)
			if result != tt.expected {
				t.Errorf("GetPodPriority() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestShouldEvictPodForResourceOptimization(t *testing.T) {
	tests := []struct {
		name          string
		pod           *v1.Pod
		clusterStrain ClusterStrainInfo
		expected      bool
	}{
		{
			name: "no strain - should not evict",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:         "container",
							RestartCount: 7,
						},
					},
				},
			},
			clusterStrain: ClusterStrainInfo{HasStrain: false},
			expected:      false,
		},
		{
			name: "strain but system-critical pod - should not evict",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"},
				Spec: v1.PodSpec{
					PriorityClassName: "system-cluster-critical",
				},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:         "container",
							RestartCount: 7,
						},
					},
				},
			},
			clusterStrain: ClusterStrainInfo{HasStrain: true, StrainPercentage: 50.0},
			expected:      false,
		},
		{
			name: "strain and OOMKilled pod - should evict",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:         "container",
							RestartCount: 1,
							LastTerminationState: v1.ContainerState{
								Terminated: &v1.ContainerStateTerminated{
									Reason: "OOMKilled",
								},
							},
						},
					},
				},
			},
			clusterStrain: ClusterStrainInfo{HasStrain: true, StrainPercentage: 50.0},
			expected:      true,
		},
		{
			name: "strain and high restart count - should evict",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default"},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:         "container",
							RestartCount: 7,
						},
					},
				},
			},
			clusterStrain: ClusterStrainInfo{HasStrain: true, StrainPercentage: 50.0},
			expected:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ShouldEvictPodForResourceOptimization(tt.pod, tt.clusterStrain)
			if result != tt.expected {
				t.Errorf("ShouldEvictPodForResourceOptimization() = %v, want %v", result, tt.expected)
			}
		})
	}
}
