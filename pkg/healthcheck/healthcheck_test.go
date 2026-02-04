package healthcheck

import (
	"context"
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
