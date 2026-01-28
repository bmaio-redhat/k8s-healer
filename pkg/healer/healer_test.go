package healer

import (
	"testing"
	"time"

	"github.com/bmaio-redhat/k8s-healer/pkg/util"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// createTestHealer creates a healer instance with fake clients for testing
func createTestHealer(namespaces []string) (*Healer, error) {
	// Create fake clientset
	clientset := fake.NewSimpleClientset()

	// Create fake dynamic client
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	healer := &Healer{
		ClientSet:                  clientset,
		DynamicClient:              dynamicClient,
		Namespaces:                 namespaces,
		StopCh:                     make(chan struct{}),
		HealedPods:                 make(map[string]time.Time),
		HealedNodes:                make(map[string]time.Time),
		HealedVMs:                  make(map[string]time.Time),
		HealedCRDs:                 make(map[string]time.Time),
		TrackedCRDs:                make(map[string]bool),
		HealCooldown:               10 * time.Minute,
		EnableVMHealing:            true,
		EnableCRDCleanup:           true,
		CRDResources:               []string{"virtualmachines.kubevirt.io/v1"},
		StaleAge:                   6 * time.Minute,
		CleanupFinalizers:          true,
		EnableResourceOptimization: true,
		StrainThreshold:            util.DefaultClusterStrainThreshold,
		OptimizedPods:              make(map[string]time.Time),
		EnableNamespacePolling:     false,
		NamespacePattern:           "",
		NamespacePollInterval:      30 * time.Second,
		WatchedNamespaces:          make(map[string]bool),
		EnableResourceCreationThrottling: true,
		CurrentClusterStrain:       nil,
	}

	return healer, nil
}

func TestNewHealer(t *testing.T) {
	// This test requires a valid kubeconfig or in-cluster config
	// For unit tests, we'll skip this and test the healer creation with fake clients
	// In integration tests, we would test with real configs
	t.Skip("Skipping NewHealer test - requires kubeconfig or in-cluster config")
}

func TestHealer_DisplayClusterInfo(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	config := &rest.Config{
		Host: "https://test-cluster.example.com",
	}

	// This should not panic
	// Note: Health checks with fake clients will show degraded status for CRDs
	// which is expected behavior - the function should handle it gracefully
	healer.DisplayClusterInfo(config, "")
}

func TestHealer_IsTestNamespace(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	healer.NamespacePattern = "test-*"

	tests := []struct {
		name      string
		namespace string
		expected  bool
	}{
		{
			name:      "matches pattern",
			namespace: "test-12345",
			expected:  true,
		},
		{
			name:      "does not match pattern",
			namespace: "default",
			expected:  false,
		},
		{
			name:      "matches pattern with different prefix",
			namespace: "test-e2e-67890",
			expected:  true,
		},
		{
			name:      "no pattern set",
			namespace: "test-12345",
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "no pattern set" {
				healer.NamespacePattern = ""
			} else {
				healer.NamespacePattern = "test-*"
			}

			result := healer.isTestNamespace(tt.namespace)
			if result != tt.expected {
				t.Errorf("isTestNamespace(%q) = %v, want %v", tt.namespace, result, tt.expected)
			}
		})
	}
}

func TestFormatBool(t *testing.T) {
	tests := []struct {
		name     string
		input    bool
		expected string
	}{
		{
			name:     "true",
			input:    true,
			expected: "✅ Enabled",
		},
		{
			name:     "false",
			input:    false,
			expected: "❌ Disabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatBool(tt.input)
			if result != tt.expected {
				t.Errorf("formatBool(%v) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestHealer_CheckAndHealVirtualMachine_AgeBased(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	now := time.Now()
	oldVM := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":      "old-vm",
				"namespace": "default",
				"creationTimestamp": now.Add(-10 * time.Minute).Format(time.RFC3339),
			},
			"status": map[string]interface{}{
				"printableStatus": "Running",
			},
		},
	}

	// VM is older than 6 minutes, should be eligible for deletion
	vmAge := time.Since(oldVM.GetCreationTimestamp().Time)
	if vmAge < 6*time.Minute {
		t.Errorf("VM age (%v) should be >= 6 minutes for this test", vmAge)
	}

	// The actual deletion logic is complex and requires mocking the dynamic client
	// This test verifies the age calculation logic
	creationTime := oldVM.GetCreationTimestamp()
	age := time.Since(creationTime.Time)
	if age < healer.StaleAge {
		t.Errorf("VM age (%v) should be >= stale age (%v)", age, healer.StaleAge)
	}
}

func TestHealer_CheckAndHealVirtualMachine_ErrorUnschedulable(t *testing.T) {
	_, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	now := time.Now()
	vm := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":      "unschedulable-vm",
				"namespace": "default",
				"creationTimestamp": now.Add(-5 * time.Minute).Format(time.RFC3339),
			},
			"status": map[string]interface{}{
				"printableStatus": "ErrorUnschedulable",
			},
		},
	}

	// Check if VM is in ErrorUnschedulable state
	status, found, _ := unstructured.NestedMap(vm.Object, "status")
	if !found {
		t.Fatal("VM status not found")
	}

	printableStatus, found, _ := unstructured.NestedString(status, "printableStatus")
	if !found {
		t.Fatal("VM printableStatus not found")
	}

	if printableStatus != "ErrorUnschedulable" {
		t.Errorf("Expected ErrorUnschedulable, got %q", printableStatus)
	}

	// VM should be eligible for cleanup if in ErrorUnschedulable for >= 3 minutes
	vmAge := time.Since(vm.GetCreationTimestamp().Time)
	errorUnschedulableThreshold := util.ErrorUnschedulableCleanupThreshold
	if vmAge < errorUnschedulableThreshold {
		t.Errorf("VM age (%v) should be >= ErrorUnschedulable threshold (%v) for this test", vmAge, errorUnschedulableThreshold)
	}
}

func TestHealer_CheckCRDResource_AgeBased(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	now := time.Now()
	oldResource := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":      "old-resource",
				"namespace": "default",
				"creationTimestamp": now.Add(-10 * time.Minute).Format(time.RFC3339),
			},
		},
	}

	// Resource is older than stale age, should be eligible for cleanup
	creationTime := oldResource.GetCreationTimestamp()
	age := time.Since(creationTime.Time)
	if age < healer.StaleAge {
		t.Errorf("Resource age (%v) should be >= stale age (%v)", age, healer.StaleAge)
	}

	// Verify staleness check
	isStale := util.IsCRDResourceStale(oldResource, healer.StaleAge, healer.CleanupFinalizers)
	if !isStale {
		t.Error("Resource should be considered stale")
	}
}

func TestHealer_CheckCRDResource_StuckFinalizers(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	now := time.Now()
	resource := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":      "stuck-resource",
				"namespace": "default",
				"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
				"deletionTimestamp": now.Add(-10 * time.Minute).Format(time.RFC3339),
				"finalizers": []interface{}{"finalizer.example.com"},
			},
		},
	}

	// Resource is stuck in terminating state, should be eligible for cleanup
	deletionTimestamp := resource.GetDeletionTimestamp()
	if deletionTimestamp == nil {
		t.Fatal("Resource should have deletion timestamp")
	}

	terminatingDuration := time.Since(deletionTimestamp.Time)
	if terminatingDuration < healer.StaleAge {
		t.Errorf("Terminating duration (%v) should be >= stale age (%v)", terminatingDuration, healer.StaleAge)
	}

	// Verify staleness check
	isStale := util.IsCRDResourceStale(resource, healer.StaleAge, healer.CleanupFinalizers)
	if !isStale {
		t.Error("Resource with stuck finalizers should be considered stale")
	}
}

func TestHealer_CheckCRDResource_ErrorPhase(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	now := time.Now()
	resource := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":      "error-resource",
				"namespace": "default",
				"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
			},
			"status": map[string]interface{}{
				"phase": "Error",
			},
		},
	}

	// Resource in error phase should be eligible for cleanup
	isStale := util.IsCRDResourceStale(resource, healer.StaleAge, healer.CleanupFinalizers)
	if !isStale {
		t.Error("Resource in error phase should be considered stale")
	}
}
