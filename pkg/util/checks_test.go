package util

import (
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestIsUnhealthy(t *testing.T) {
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
			name: "pod in CrashLoopBackOff with restart count below threshold",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "crash-pod", Namespace: "default"},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:         "container",
							RestartCount: 2,
							State: v1.ContainerState{
								Waiting: &v1.ContainerStateWaiting{
									Reason: "CrashLoopBackOff",
								},
							},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "pod in CrashLoopBackOff with restart count at threshold",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "crash-pod", Namespace: "default"},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:         "container",
							RestartCount: 3,
							State: v1.ContainerState{
								Waiting: &v1.ContainerStateWaiting{
									Reason: "CrashLoopBackOff",
								},
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "pod in CrashLoopBackOff with restart count above threshold",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "crash-pod", Namespace: "default"},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:         "container",
							RestartCount: 5,
							State: v1.ContainerState{
								Waiting: &v1.ContainerStateWaiting{
									Reason: "CrashLoopBackOff",
								},
							},
						},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsUnhealthy(tt.pod)
			if result != tt.expected {
				t.Errorf("IsUnhealthy() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetHealReason(t *testing.T) {
	tests := []struct {
		name     string
		pod      *v1.Pod
		expected string
	}{
		{
			name: "pod in CrashLoopBackOff",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "crash-pod", Namespace: "default"},
				Status: v1.PodStatus{
					ContainerStatuses: []v1.ContainerStatus{
						{
							Name:         "container",
							RestartCount: 5,
							State: v1.ContainerState{
								Waiting: &v1.ContainerStateWaiting{
									Reason: "CrashLoopBackOff",
								},
							},
						},
					},
				},
			},
			expected: "Persistent CrashLoopBackOff (Restarts: 5)",
		},
		{
			name: "healthy pod",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "healthy-pod", Namespace: "default"},
				Status: v1.PodStatus{
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
			expected: "Unspecified Failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetHealReason(tt.pod)
			if result != tt.expected {
				t.Errorf("GetHealReason() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestIsNodeUnhealthy(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		node     *v1.Node
		expected bool
	}{
		{
			name: "healthy node",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "healthy-node"},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:   v1.NodeReady,
							Status: v1.ConditionTrue,
							LastTransitionTime: metav1.Time{Time: now.Add(-1 * time.Hour)},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "node NotReady for too long",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "notready-node"},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:   v1.NodeReady,
							Status: v1.ConditionFalse,
							LastTransitionTime: metav1.Time{Time: now.Add(-6 * time.Minute)},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "node NotReady for short time",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "notready-node"},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:   v1.NodeReady,
							Status: v1.ConditionFalse,
							LastTransitionTime: metav1.Time{Time: now.Add(-2 * time.Minute)},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "node Unknown for too long",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "unknown-node"},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:   v1.NodeReady,
							Status: v1.ConditionUnknown,
							LastTransitionTime: metav1.Time{Time: now.Add(-3 * time.Minute)},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "node with memory pressure",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "pressure-node"},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:   v1.NodeReady,
							Status: v1.ConditionTrue,
							LastTransitionTime: metav1.Time{Time: now.Add(-1 * time.Hour)},
						},
						{
							Type:   v1.NodeMemoryPressure,
							Status: v1.ConditionTrue,
							LastTransitionTime: metav1.Time{Time: now.Add(-15 * time.Minute)},
						},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsNodeUnhealthy(tt.node)
			if result != tt.expected {
				t.Errorf("IsNodeUnhealthy() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestIsVirtualMachineUnhealthy(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		vm       *unstructured.Unstructured
		expected bool
	}{
		{
			name: "healthy VM",
			vm: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":      "healthy-vm",
						"namespace": "default",
						"creationTimestamp": now.Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"printableStatus": "Running",
					},
				},
			},
			expected: false,
		},
		{
			name: "VM in Error state",
			vm: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":      "error-vm",
						"namespace": "default",
						"creationTimestamp": now.Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"printableStatus": "Error",
					},
				},
			},
			expected: true,
		},
		{
			name: "VM in ErrorUnschedulable for long time",
			vm: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":      "unschedulable-vm",
						"namespace": "default",
						"creationTimestamp": now.Add(-5 * time.Minute).Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"printableStatus": "ErrorUnschedulable",
					},
				},
			},
			expected: true,
		},
		{
			name: "VM in ErrorUnschedulable for short time",
			vm: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":      "unschedulable-vm",
						"namespace": "default",
						"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"printableStatus": "ErrorUnschedulable",
					},
				},
			},
			expected: false,
		},
		{
			name: "VM stuck in Provisioning",
			vm: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":      "provisioning-vm",
						"namespace": "default",
						"creationTimestamp": now.Add(-3 * time.Minute).Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"printableStatus": "Provisioning",
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsVirtualMachineUnhealthy(tt.vm)
			if result != tt.expected {
				t.Errorf("IsVirtualMachineUnhealthy() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestIsCRDResourceStale(t *testing.T) {
	now := time.Now()
	staleAge := 6 * time.Minute

	tests := []struct {
		name           string
		resource       *unstructured.Unstructured
		staleAge       time.Duration
		checkFinalizers bool
		expected       bool
	}{
		{
			name: "recent resource",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":      "recent-resource",
						"namespace": "default",
						"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
					},
				},
			},
			staleAge:        staleAge,
			checkFinalizers: true,
			expected:       false,
		},
		{
			name: "old resource",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":      "old-resource",
						"namespace": "default",
						"creationTimestamp": now.Add(-10 * time.Minute).Format(time.RFC3339),
					},
				},
			},
			staleAge:        staleAge,
			checkFinalizers: true,
			expected:       true,
		},
		{
			name: "resource stuck in terminating",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":      "terminating-resource",
						"namespace": "default",
						"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
						"deletionTimestamp": now.Add(-10 * time.Minute).Format(time.RFC3339),
						"finalizers": []interface{}{"finalizer.example.com"},
					},
					"status": map[string]interface{}{},
				},
			},
			staleAge:        staleAge,
			checkFinalizers: true,
			expected:       true,
		},
		{
			name: "resource with error phase",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":      "error-resource",
						"namespace": "default",
						"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"phase": "Error",
					},
				},
			},
			staleAge:        staleAge,
			checkFinalizers: true,
			expected:       true,
		},
		{
			name: "resource with error printableStatus",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":      "error-status-resource",
						"namespace": "default",
						"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"printableStatus": "Error",
					},
				},
			},
			staleAge:        staleAge,
			checkFinalizers: true,
			expected:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsCRDResourceStale(tt.resource, tt.staleAge, tt.checkFinalizers)
			if result != tt.expected {
				t.Errorf("IsCRDResourceStale() = %v, want %v", result, tt.expected)
			}
		})
	}
}

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
				StrainPercentage:  0.0,
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
				StrainPercentage:  50.0,
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
				StrainPercentage:  33.33,
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
		name         string
		pod          *v1.Pod
		clusterStrain ClusterStrainInfo
		expected     bool
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
