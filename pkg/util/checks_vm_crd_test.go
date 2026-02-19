package util

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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
						"name":              "healthy-vm",
						"namespace":         "default",
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
						"name":              "error-vm",
						"namespace":         "default",
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
						"name":              "unschedulable-vm",
						"namespace":         "default",
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
						"name":              "unschedulable-vm",
						"namespace":         "default",
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
						"name":              "provisioning-vm",
						"namespace":         "default",
						"creationTimestamp": now.Add(-3 * time.Minute).Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"printableStatus": "Provisioning",
					},
				},
			},
			expected: true,
		},
		{
			name: "VM with Failure condition past threshold",
			vm: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "failure-conditions-vm",
						"namespace":         "default",
						"creationTimestamp": now.Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"printableStatus": "Running",
						"conditions": []interface{}{
							map[string]interface{}{
								"type":               "Failure",
								"status":             "True",
								"lastTransitionTime": now.Add(-5 * time.Minute).Format(time.RFC3339),
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "VM in ErrorUnschedulable with conditions lastTransitionTime",
			vm: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "unschedulable-conditions-vm",
						"namespace":         "default",
						"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"printableStatus": "ErrorUnschedulable",
						"conditions": []interface{}{
							map[string]interface{}{
								"type":               "Scheduled",
								"status":             "False",
								"lastTransitionTime": now.Add(-5 * time.Minute).Format(time.RFC3339),
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
		name            string
		resource        *unstructured.Unstructured
		staleAge        time.Duration
		checkFinalizers bool
		expected        bool
	}{
		{
			name: "recent resource",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "recent-resource",
						"namespace":         "default",
						"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
					},
				},
			},
			staleAge:        staleAge,
			checkFinalizers: true,
			expected:        false,
		},
		{
			name: "old resource",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "old-resource",
						"namespace":         "default",
						"creationTimestamp": now.Add(-10 * time.Minute).Format(time.RFC3339),
					},
				},
			},
			staleAge:        staleAge,
			checkFinalizers: true,
			expected:        true,
		},
		{
			name: "resource stuck in terminating",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "terminating-resource",
						"namespace":         "default",
						"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
						"deletionTimestamp": now.Add(-10 * time.Minute).Format(time.RFC3339),
						"finalizers":        []interface{}{"finalizer.example.com"},
					},
					"status": map[string]interface{}{},
				},
			},
			staleAge:        staleAge,
			checkFinalizers: true,
			expected:        true,
		},
		{
			name: "resource stuck in terminating (force-delete even when checkFinalizers false)",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "terminating-resource-no-flag",
						"namespace":         "default",
						"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
						"deletionTimestamp": now.Add(-10 * time.Minute).Format(time.RFC3339),
						"finalizers":        []interface{}{"finalizer.example.com"},
					},
					"status": map[string]interface{}{},
				},
			},
			staleAge:        staleAge,
			checkFinalizers: false,
			expected:        true,
		},
		{
			name: "resource with error phase",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "error-resource",
						"namespace":         "default",
						"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"phase": "Error",
					},
				},
			},
			staleAge:        staleAge,
			checkFinalizers: true,
			expected:        true,
		},
		{
			name: "resource with error printableStatus",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "error-status-resource",
						"namespace":         "default",
						"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"printableStatus": "Error",
					},
				},
			},
			staleAge:        staleAge,
			checkFinalizers: true,
			expected:        true,
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

func TestGetVirtualMachineHealReason(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		vm       *unstructured.Unstructured
		contains string
	}{
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
			contains: "Error state",
		},
		{
			name: "VM ErrorUnschedulable",
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
			contains: "ErrorUnschedulable",
		},
		{
			name: "VM stuck in Provisioning",
			vm: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":      "provisioning-vm",
						"namespace": "default",
						"creationTimestamp": now.Add(-10 * time.Minute).Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"printableStatus": "Provisioning",
					},
				},
			},
			contains: "Provisioning",
		},
		{
			name: "VM with no status",
			vm: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":      "no-status",
						"namespace": "default",
					},
				},
			},
			contains: "Unspecified VM Failure",
		},
		{
			name: "VM with error conditions (default branch)",
			vm: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "error-conditions-vm",
						"namespace":         "default",
						"creationTimestamp": now.Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"printableStatus": "Starting",
						"conditions": []interface{}{
							map[string]interface{}{
								"type":               "Failure",
								"status":             "True",
								"lastTransitionTime": now.Add(-5 * time.Minute).Format(time.RFC3339),
							},
						},
					},
				},
			},
			contains: "error conditions detected",
		},
		{
			name: "VM ErrorUnschedulable with conditions duration",
			vm: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "unschedulable-duration-vm",
						"namespace":         "default",
						"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"printableStatus": "ErrorUnschedulable",
						"conditions": []interface{}{
							map[string]interface{}{
								"type":               "Ready",
								"status":             "False",
								"lastTransitionTime": now.Add(-10 * time.Minute).Format(time.RFC3339),
							},
						},
					},
				},
			},
			contains: "ErrorUnschedulable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := GetVirtualMachineHealReason(tt.vm)
			if !strings.Contains(reason, tt.contains) {
				t.Errorf("GetVirtualMachineHealReason() = %q, want to contain %q", reason, tt.contains)
			}
		})
	}
}

func TestGetCRDResourceStaleReason(t *testing.T) {
	now := time.Now()
	staleAge := 5 * time.Minute
	tests := []struct {
		name     string
		resource *unstructured.Unstructured
		contains string
	}{
		{
			name: "resource older than stale age",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":      "old-resource",
						"namespace": "default",
						"creationTimestamp": now.Add(-1 * time.Hour).Format(time.RFC3339),
					},
				},
				},
			contains: "older than stale age",
		},
		{
			name: "resource stuck terminating",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "terminating-resource",
						"namespace":         "default",
						"creationTimestamp": now.Format(time.RFC3339),
						"deletionTimestamp": now.Add(-10 * time.Minute).Format(time.RFC3339),
					},
				},
				},
			contains: "Stuck in terminating",
		},
		{
			name: "resource with finalizers",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "finalizer-resource",
						"namespace":         "default",
						"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
						"finalizers":        []interface{}{"test-finalizer"},
					},
				},
				},
			contains: "finalizers",
		},
		{
			name: "resource with error phase",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "error-phase",
						"namespace":         "default",
						"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"phase": "Error",
					},
				},
				},
			contains: "failed",
		},
		{
			name: "resource with error conditions in status",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":              "error-conditions",
						"namespace":         "default",
						"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
					},
					"status": map[string]interface{}{
						"conditions": []interface{}{
							map[string]interface{}{
								"type":               "Error",
								"status":             "True",
								"lastTransitionTime": now.Add(-10 * time.Minute).Format(time.RFC3339),
							},
						},
					},
				},
				},
			contains: "Error conditions detected",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := GetCRDResourceStaleReason(tt.resource, staleAge, true)
			if !strings.Contains(reason, tt.contains) {
				t.Errorf("GetCRDResourceStaleReason() = %q, want to contain %q", reason, tt.contains)
			}
		})
	}
}
