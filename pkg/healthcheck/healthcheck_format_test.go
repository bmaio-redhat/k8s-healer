package healthcheck

import (
	"fmt"
	"testing"
	"time"
)

func TestFormatHealthCheckStatus(t *testing.T) {
	status := &ClusterHealthStatus{
		OverallStatus: "healthy",
		KubernetesAPI: HealthCheckResult{
			Name:     "Kubernetes API Server",
			Status:   "healthy",
			Message:  "Kubernetes API server is accessible",
			Duration: 45 * time.Millisecond,
		},
		KubeVirtAPI: HealthCheckResult{
			Name:     "KubeVirt API",
			Status:   "healthy",
			Message:  "KubeVirt API group found",
			Duration: 120 * time.Millisecond,
		},
		KubeVirtCRDs: []HealthCheckResult{
			{
				Name:     "VirtualMachines",
				Status:   "healthy",
				Message:  "CRD is available and accessible",
				Duration: 23 * time.Millisecond,
			},
			{
				Name:     "VirtualMachineInstances",
				Status:   "healthy",
				Message:  "CRD is available and accessible",
				Duration: 18 * time.Millisecond,
			},
		},
		KeyResources: []HealthCheckResult{
			{
				Name:     "Nodes",
				Status:   "healthy",
				Message:  "Nodes resource is accessible",
				Duration: 12 * time.Millisecond,
			},
		},
		TotalChecks:  5,
		PassedChecks: 5,
		FailedChecks: 0,
		Warnings:     0,
	}

	output := FormatHealthCheckStatus(status)

	if output == "" {
		t.Error("FormatHealthCheckStatus() output should not be empty")
	}

	// Check that key elements are present
	if !testContains(output, "Cluster Health") {
		t.Error("FormatHealthCheckStatus() output should contain 'Cluster Health'")
	}

	if !testContains(output, "Kubernetes API") {
		t.Error("FormatHealthCheckStatus() output should contain 'Kubernetes API'")
	}

	if !testContains(output, "KubeVirt API") {
		t.Error("FormatHealthCheckStatus() output should contain 'KubeVirt API'")
	}

	if !testContains(output, "KubeVirt CRDs") {
		t.Error("FormatHealthCheckStatus() output should contain 'KubeVirt CRDs'")
	}

	if !testContains(output, "Key Resources") {
		t.Error("FormatHealthCheckStatus() output should contain 'Key Resources'")
	}
}

func TestFormatHealthCheckStatus_Degraded(t *testing.T) {
	status := &ClusterHealthStatus{
		OverallStatus: "degraded",
		KubernetesAPI: HealthCheckResult{
			Name:     "Kubernetes API Server",
			Status:   "healthy",
			Message:  "Kubernetes API server is accessible",
			Duration: 45 * time.Millisecond,
		},
		KubeVirtAPI: HealthCheckResult{
			Name:     "KubeVirt API",
			Status:   "degraded",
			Message:  "KubeVirt API group found but no versions available",
			Duration: 120 * time.Millisecond,
		},
		TotalChecks:  2,
		PassedChecks: 1,
		FailedChecks: 0,
		Warnings:     1,
	}

	output := FormatHealthCheckStatus(status)

	if !testContains(output, "degraded") {
		t.Error("FormatHealthCheckStatus() should show degraded status")
	}

	if !testContains(output, "1 warnings") {
		t.Error("FormatHealthCheckStatus() should show warning count")
	}
}

func TestFormatHealthCheckStatus_Unhealthy(t *testing.T) {
	status := &ClusterHealthStatus{
		OverallStatus: "unhealthy",
		KubernetesAPI: HealthCheckResult{
			Name:     "Kubernetes API Server",
			Status:   "unhealthy",
			Message:  "Failed to connect to Kubernetes API",
			Duration: 45 * time.Millisecond,
			Error:    fmt.Errorf("connection refused"),
		},
		KubeVirtAPI: HealthCheckResult{
			Name:     "KubeVirt API",
			Status:   "unhealthy",
			Message:  "KubeVirt API group not found",
			Duration: 120 * time.Millisecond,
		},
		TotalChecks:  2,
		PassedChecks: 0,
		FailedChecks: 2,
		Warnings:     0,
	}

	output := FormatHealthCheckStatus(status)

	if !testContains(output, "unhealthy") {
		t.Error("FormatHealthCheckStatus() should show unhealthy status")
	}

	if !testContains(output, "2 failed") {
		t.Error("FormatHealthCheckStatus() should show failed count")
	}
}

func TestFormatCheckResult(t *testing.T) {
	tests := []struct {
		name     string
		result   HealthCheckResult
		expected string
	}{
		{
			name: "healthy result",
			result: HealthCheckResult{
				Name:     "Test Resource",
				Status:   "healthy",
				Message:  "Resource is accessible",
				Duration: 50 * time.Millisecond,
			},
			expected: "✅",
		},
		{
			name: "degraded result",
			result: HealthCheckResult{
				Name:     "Test Resource",
				Status:   "degraded",
				Message:  "Resource has warnings",
				Duration: 50 * time.Millisecond,
			},
			expected: "⚠️",
		},
		{
			name: "unhealthy result",
			result: HealthCheckResult{
				Name:     "Test Resource",
				Status:   "unhealthy",
				Message:  "Resource is not accessible",
				Duration: 50 * time.Millisecond,
			},
			expected: "❌",
		},
		{
			name: "unknown result",
			result: HealthCheckResult{
				Name:     "Test Resource",
				Status:   "unknown",
				Message:  "Status unknown",
				Duration: 50 * time.Millisecond,
			},
			expected: "❓",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := formatCheckResult("Test", tt.result)
			if !testContains(output, tt.expected) {
				t.Errorf("formatCheckResult() output = %q, should contain %q", output, tt.expected)
			}
			if !testContains(output, tt.result.Message) {
				t.Errorf("formatCheckResult() output = %q, should contain message %q", output, tt.result.Message)
			}
		})
	}
}

func TestKubeVirtCRDsList(t *testing.T) {
	// Verify that kubevirtCRDs list is not empty
	if len(kubevirtCRDs) == 0 {
		t.Error("kubevirtCRDs list should not be empty")
	}

	// Verify expected CRDs are present
	expectedCRDs := map[string]bool{
		"VirtualMachines":                  false,
		"VirtualMachineInstances":          false,
		"VirtualMachineInstanceMigrations": false,
		"DataVolumes":                      false,
		"VirtualMachineSnapshots":          false,
	}

	for _, crd := range kubevirtCRDs {
		if _, exists := expectedCRDs[crd.Name]; exists {
			expectedCRDs[crd.Name] = true
		}
	}

	for name, found := range expectedCRDs {
		if !found {
			t.Errorf("kubevirtCRDs list missing expected CRD: %s", name)
		}
	}
}
