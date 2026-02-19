package util

import (
	"strings"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
							Type:               v1.NodeReady,
							Status:             v1.ConditionTrue,
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
							Type:               v1.NodeReady,
							Status:             v1.ConditionFalse,
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
							Type:               v1.NodeReady,
							Status:             v1.ConditionFalse,
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
							Type:               v1.NodeReady,
							Status:             v1.ConditionUnknown,
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
							Type:               v1.NodeReady,
							Status:             v1.ConditionTrue,
							LastTransitionTime: metav1.Time{Time: now.Add(-1 * time.Hour)},
						},
						{
							Type:               v1.NodeMemoryPressure,
							Status:             v1.ConditionTrue,
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

func TestGetNodeHealReason(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		node     *v1.Node
		contains string
	}{
		{
			name: "NotReady node",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "notready-node"},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:               v1.NodeReady,
							Status:             v1.ConditionFalse,
							LastTransitionTime: metav1.Time{Time: now.Add(-6 * time.Minute)},
						},
					},
				},
			},
			contains: "NotReady",
		},
		{
			name: "Unknown node",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "unknown-node"},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:               v1.NodeReady,
							Status:             v1.ConditionUnknown,
							LastTransitionTime: metav1.Time{Time: now.Add(-3 * time.Minute)},
						},
					},
				},
			},
			contains: "Unknown",
		},
		{
			name: "node with memory pressure",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "pressure-node"},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{Type: v1.NodeReady, Status: v1.ConditionTrue, LastTransitionTime: metav1.Time{Time: now.Add(-1 * time.Hour)}},
						{Type: v1.NodeMemoryPressure, Status: v1.ConditionTrue, LastTransitionTime: metav1.Time{Time: now.Add(-15 * time.Minute)}},
					},
				},
			},
			contains: "resource pressure",
		},
		{
			name: "healthy node",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "healthy-node"},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{Type: v1.NodeReady, Status: v1.ConditionTrue, LastTransitionTime: metav1.Time{Time: now}},
					},
				},
			},
			contains: "Unspecified",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := GetNodeHealReason(tt.node)
			if !strings.Contains(reason, tt.contains) {
				t.Errorf("GetNodeHealReason() = %q, want to contain %q", reason, tt.contains)
			}
		})
	}
}
