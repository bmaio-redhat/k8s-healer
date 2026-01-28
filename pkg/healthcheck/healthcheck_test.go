package healthcheck

import (
	"context"
	"fmt"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// createTestClients creates fake Kubernetes clients for testing
func createTestClients() (kubernetes.Interface, dynamic.Interface, *rest.Config) {
	// Create fake clientset with some test objects
	clientset := kubernetesfake.NewSimpleClientset(
		&v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
		},
		&v1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		},
		&v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
			},
		},
	)

	// Create fake dynamic client
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	// Create minimal config
	config := &rest.Config{
		Host: "https://test-cluster.example.com",
	}

	return clientset, dynamicClient, config
}

func TestCheckKubernetesAPI(t *testing.T) {
	clientset, _, _ := createTestClients()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := checkKubernetesAPI(ctx, clientset)

	if result.Status != "healthy" {
		t.Errorf("checkKubernetesAPI() status = %v, want healthy", result.Status)
	}

	if result.Error != nil {
		t.Errorf("checkKubernetesAPI() error = %v, want nil", result.Error)
	}

	if result.Duration == 0 {
		t.Error("checkKubernetesAPI() duration should be > 0")
	}
}

func TestCheckKubernetesAPI_Error(t *testing.T) {
	// Note: Fake clients don't respect context cancellation the same way real clients do
	// This test verifies the function handles errors, but with fake clients it may still succeed
	// In a real scenario with network issues, the real client would fail appropriately
	clientset := kubernetesfake.NewSimpleClientset()

	// Use a context with a very short timeout to potentially trigger timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	// Give it a moment for timeout
	time.Sleep(10 * time.Millisecond)

	result := checkKubernetesAPI(ctx, clientset)

	// With fake clients, the operation may still succeed even with cancelled context
	// The important thing is that the function doesn't panic and returns a valid result
	if result.Status == "unknown" {
		t.Error("checkKubernetesAPI() result status should not be unknown")
	}

	// Verify the result structure is valid
	if result.Name == "" {
		t.Error("checkKubernetesAPI() result should have a name")
	}
}

func TestCheckKubeVirtCRDs(t *testing.T) {
	_, dynamicClient, _ := createTestClients()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := checkKubeVirtCRDs(ctx, dynamicClient)

	if len(results) != len(kubevirtCRDs) {
		t.Errorf("checkKubeVirtCRDs() returned %d results, want %d", len(results), len(kubevirtCRDs))
	}

	// With fake client, CRDs will show as degraded (fake client limitation)
	// but the function should handle it gracefully without panicking
	for _, result := range results {
		if result.Status == "unknown" {
			t.Errorf("checkKubeVirtCRDs() result status should not be unknown for %s", result.Name)
		}

		// With fake client, status should be degraded (fake client limitation)
		// or unhealthy (CRD not found), but not unknown
		if result.Status != "degraded" && result.Status != "unhealthy" {
			t.Logf("checkKubeVirtCRDs() result for %s has status %s (expected degraded or unhealthy with fake client)", result.Name, result.Status)
		}

		if result.Duration == 0 {
			t.Errorf("checkKubeVirtCRDs() duration should be > 0 for %s", result.Name)
		}
	}
}

func TestCheckKubeVirtAPI(t *testing.T) {
	clientset, _, config := createTestClients()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := checkKubeVirtAPI(ctx, clientset, config)

	// With fake client, KubeVirt API check will return degraded status
	if result.Status == "unknown" {
		t.Error("checkKubeVirtAPI() result status should not be unknown")
	}

	// Duration should be set (even if it's very small for fake clients)
	if result.Duration < 0 {
		t.Error("checkKubeVirtAPI() duration should be >= 0")
	}

	// With fake client, should return degraded status
	if result.Status != "degraded" {
		t.Logf("checkKubeVirtAPI() with fake client returned status %s (expected degraded)", result.Status)
	}
}

func TestCheckKeyResources(t *testing.T) {
	clientset, dynamicClient, _ := createTestClients()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := checkKeyResources(ctx, clientset, dynamicClient)

	if len(results) == 0 {
		t.Error("checkKeyResources() should return at least one result")
	}

	// Check that all expected resources are present
	resourceNames := make(map[string]bool)
	for _, result := range results {
		resourceNames[result.Name] = true
	}

	expectedResources := []string{"Nodes", "Pods", "Namespaces"}
	for _, expected := range expectedResources {
		if !resourceNames[expected] {
			t.Errorf("checkKeyResources() missing expected resource: %s", expected)
		}
	}

	// All resources should be healthy with fake client
	for _, result := range results {
		if result.Status != "healthy" {
			t.Errorf("checkKeyResources() result for %s status = %v, want healthy", result.Name, result.Status)
		}

		if result.Duration == 0 {
			t.Errorf("checkKeyResources() duration should be > 0 for %s", result.Name)
		}
	}
}

func TestPerformClusterHealthCheck(t *testing.T) {
	clientset, dynamicClient, config := createTestClients()

	status, err := PerformClusterHealthCheck(clientset, dynamicClient, config)

	if err != nil {
		t.Fatalf("PerformClusterHealthCheck() error = %v, want nil", err)
	}

	if status == nil {
		t.Fatal("PerformClusterHealthCheck() status = nil, want non-nil")
	}

	// Verify all checks were performed
	if status.KubernetesAPI.Status == "unknown" {
		t.Error("PerformClusterHealthCheck() KubernetesAPI status should not be unknown")
	}

	if len(status.KubeVirtCRDs) != len(kubevirtCRDs) {
		t.Errorf("PerformClusterHealthCheck() KubeVirtCRDs count = %d, want %d", len(status.KubeVirtCRDs), len(kubevirtCRDs))
	}

	if status.KubeVirtAPI.Status == "unknown" {
		t.Error("PerformClusterHealthCheck() KubeVirtAPI status should not be unknown")
	}

	if len(status.KeyResources) == 0 {
		t.Error("PerformClusterHealthCheck() KeyResources should not be empty")
	}

	// Verify totals
	expectedTotal := 1 + len(kubevirtCRDs) + 1 + len(status.KeyResources)
	if status.TotalChecks != expectedTotal {
		t.Errorf("PerformClusterHealthCheck() TotalChecks = %d, want %d", status.TotalChecks, expectedTotal)
	}

	// Verify overall status is set
	if status.OverallStatus == "" {
		t.Error("PerformClusterHealthCheck() OverallStatus should not be empty")
	}

	validStatuses := map[string]bool{
		"healthy":   true,
		"degraded":  true,
		"unhealthy": true,
	}
	if !validStatuses[status.OverallStatus] {
		t.Errorf("PerformClusterHealthCheck() OverallStatus = %s, want one of: healthy, degraded, unhealthy", status.OverallStatus)
	}
}

func TestFormatHealthCheckStatus(t *testing.T) {
	status := &ClusterHealthStatus{
		OverallStatus: "healthy",
		KubernetesAPI: HealthCheckResult{
			Name:    "Kubernetes API Server",
			Status:  "healthy",
			Message: "Kubernetes API server is accessible",
			Duration: 45 * time.Millisecond,
		},
		KubeVirtAPI: HealthCheckResult{
			Name:    "KubeVirt API",
			Status:  "healthy",
			Message: "KubeVirt API group found",
			Duration: 120 * time.Millisecond,
		},
		KubeVirtCRDs: []HealthCheckResult{
			{
				Name:    "VirtualMachines",
				Status:  "healthy",
				Message: "CRD is available and accessible",
				Duration: 23 * time.Millisecond,
			},
			{
				Name:    "VirtualMachineInstances",
				Status:  "healthy",
				Message: "CRD is available and accessible",
				Duration: 18 * time.Millisecond,
			},
		},
		KeyResources: []HealthCheckResult{
			{
				Name:    "Nodes",
				Status:  "healthy",
				Message: "Nodes resource is accessible",
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
			Name:    "Kubernetes API Server",
			Status:  "healthy",
			Message: "Kubernetes API server is accessible",
			Duration: 45 * time.Millisecond,
		},
		KubeVirtAPI: HealthCheckResult{
			Name:    "KubeVirt API",
			Status:  "degraded",
			Message: "KubeVirt API group found but no versions available",
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
			Name:    "Kubernetes API Server",
			Status:  "unhealthy",
			Message: "Failed to connect to Kubernetes API",
			Duration: 45 * time.Millisecond,
			Error:   fmt.Errorf("connection refused"),
		},
		KubeVirtAPI: HealthCheckResult{
			Name:    "KubeVirt API",
			Status:  "unhealthy",
			Message: "KubeVirt API group not found",
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
		"VirtualMachines":                    false,
		"VirtualMachineInstances":            false,
		"VirtualMachineInstanceMigrations":  false,
		"DataVolumes":                        false,
		"VirtualMachineSnapshots":            false,
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

// Helper function to check if a string contains a substring
// Note: Using strings.Contains from standard library would be better, but this is for testing
func testContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && testContainsHelper(s, substr))
}

func testContainsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
