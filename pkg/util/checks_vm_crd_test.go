package util

import (
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
