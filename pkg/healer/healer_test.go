package healer

import (
	"context"
	"testing"
	"time"

	"github.com/bmaio-redhat/k8s-healer/pkg/util"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// createTestHealer creates a healer instance with fake clients for testing
func createTestHealer(namespaces []string) (*Healer, error) {
	return createTestHealerWithExclusions(namespaces, []string{})
}

// createTestHealerWithExclusions creates a healer instance with fake clients and excluded namespaces
func createTestHealerWithExclusions(namespaces []string, excludedNamespaces []string) (*Healer, error) {
	// Create fake clientset
	clientset := fake.NewSimpleClientset()

	// Create fake dynamic client
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	// Initialize WatchedNamespaces map
	watchedNamespaces := make(map[string]bool)
	for _, ns := range namespaces {
		watchedNamespaces[ns] = true
	}

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
		NamespacePollInterval:      5 * time.Second,
		WatchedNamespaces:          watchedNamespaces,
		EnableResourceCreationThrottling: true,
		CurrentClusterStrain:       nil,
		ExcludedNamespaces:         excludedNamespaces,
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

func TestHealer_ExtractPrefixesFromNamespaces(t *testing.T) {
	healer, err := createTestHealer([]string{"test-123", "test-456", "e2e-789"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	prefixes := healer.extractPrefixesFromNamespaces()

	// Should extract "test-" and "e2e-" prefixes
	expectedPrefixes := map[string]bool{
		"test-": true,
		"e2e-":  true,
	}

	if len(prefixes) != len(expectedPrefixes) {
		t.Errorf("extractPrefixesFromNamespaces() returned %d prefixes, want %d", len(prefixes), len(expectedPrefixes))
	}

	for prefix := range expectedPrefixes {
		if !prefixes[prefix] {
			t.Errorf("extractPrefixesFromNamespaces() missing prefix %q", prefix)
		}
	}
}

func TestHealer_ExtractPrefixesFromNamespaces_WithUnderscore(t *testing.T) {
	healer, err := createTestHealer([]string{"test_123", "app_456"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	prefixes := healer.extractPrefixesFromNamespaces()

	// Should extract "test_" and "app_" prefixes
	expectedPrefixes := map[string]bool{
		"test_": true,
		"app_":  true,
	}

	if len(prefixes) != len(expectedPrefixes) {
		t.Errorf("extractPrefixesFromNamespaces() returned %d prefixes, want %d", len(prefixes), len(expectedPrefixes))
	}

	for prefix := range expectedPrefixes {
		if !prefixes[prefix] {
			t.Errorf("extractPrefixesFromNamespaces() missing prefix %q", prefix)
		}
	}
}

func TestHealer_ExtractPrefixesFromNamespaces_WithDot(t *testing.T) {
	healer, err := createTestHealer([]string{"test.123", "app.456"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	prefixes := healer.extractPrefixesFromNamespaces()

	// Should extract "test." and "app." prefixes
	expectedPrefixes := map[string]bool{
		"test.": true,
		"app.":  true,
	}

	if len(prefixes) != len(expectedPrefixes) {
		t.Errorf("extractPrefixesFromNamespaces() returned %d prefixes, want %d", len(prefixes), len(expectedPrefixes))
	}

	for prefix := range expectedPrefixes {
		if !prefixes[prefix] {
			t.Errorf("extractPrefixesFromNamespaces() missing prefix %q", prefix)
		}
	}
}

func TestHealer_ExtractPrefixesFromNamespaces_SkipsWildcards(t *testing.T) {
	healer, err := createTestHealer([]string{"test-123", "test-*", "e2e-456"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	prefixes := healer.extractPrefixesFromNamespaces()

	// Should extract "test-" and "e2e-" prefixes, but skip "test-*"
	expectedPrefixes := map[string]bool{
		"test-": true,
		"e2e-":  true,
	}

	if len(prefixes) != len(expectedPrefixes) {
		t.Errorf("extractPrefixesFromNamespaces() returned %d prefixes, want %d", len(prefixes), len(expectedPrefixes))
	}

	for prefix := range expectedPrefixes {
		if !prefixes[prefix] {
			t.Errorf("extractPrefixesFromNamespaces() missing prefix %q", prefix)
		}
	}
}

func TestHealer_ExtractPrefixesFromNamespaces_NoSeparator(t *testing.T) {
	healer, err := createTestHealer([]string{"test123", "app456"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	prefixes := healer.extractPrefixesFromNamespaces()

	// Namespaces without separators should not produce prefixes
	if len(prefixes) != 0 {
		t.Errorf("extractPrefixesFromNamespaces() returned %d prefixes, want 0", len(prefixes))
	}
}

func TestHealer_IsExcludedNamespace(t *testing.T) {
	healer, err := createTestHealerWithExclusions([]string{"test-123"}, []string{"test-keep", "test-important"})
	if err != nil {
		t.Fatalf("createTestHealerWithExclusions() error = %v", err)
	}

	tests := []struct {
		name      string
		namespace string
		expected  bool
	}{
		{
			name:      "excluded namespace",
			namespace: "test-keep",
			expected:  true,
		},
		{
			name:      "another excluded namespace",
			namespace: "test-important",
			expected:  true,
		},
		{
			name:      "non-excluded namespace",
			namespace: "test-123",
			expected:  false,
		},
		{
			name:      "non-excluded namespace 2",
			namespace: "test-456",
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := healer.isExcludedNamespace(tt.namespace)
			if result != tt.expected {
				t.Errorf("isExcludedNamespace(%q) = %v, want %v", tt.namespace, result, tt.expected)
			}
		})
	}
}

func TestHealer_DiscoverNewNamespaces_PrefixBased(t *testing.T) {
	// Create a healer with initial namespace "test-123"
	healer, err := createTestHealer([]string{"test-123"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	// Enable namespace polling
	healer.EnableNamespacePolling = true
	healer.NamespacePattern = ""

	// Create fake namespaces in the cluster
	clientset := healer.ClientSet.(*fake.Clientset)
	namespaces := []*v1.Namespace{
		{ObjectMeta: metav1.ObjectMeta{Name: "test-123"}}, // Already watched
		{ObjectMeta: metav1.ObjectMeta{Name: "test-456"}}, // Should be discovered
		{ObjectMeta: metav1.ObjectMeta{Name: "test-789"}}, // Should be discovered
		{ObjectMeta: metav1.ObjectMeta{Name: "default"}},  // Should not be discovered
		{ObjectMeta: metav1.ObjectMeta{Name: "e2e-123"}},  // Should not be discovered (different prefix)
	}

	for _, ns := range namespaces {
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create namespace %s: %v", ns.Name, err)
		}
	}

	// Discover new namespaces
	healer.discoverNewNamespaces()

	// Check that test-456 and test-789 are now in WatchedNamespaces
	if !healer.WatchedNamespaces["test-456"] {
		t.Error("test-456 should be discovered and watched")
	}
	if !healer.WatchedNamespaces["test-789"] {
		t.Error("test-789 should be discovered and watched")
	}

	// Check that default and e2e-123 are not watched
	if healer.WatchedNamespaces["default"] {
		t.Error("default should not be discovered (different prefix)")
	}
	if healer.WatchedNamespaces["e2e-123"] {
		t.Error("e2e-123 should not be discovered (different prefix)")
	}
}

func TestHealer_DiscoverNewNamespaces_WithExclusions(t *testing.T) {
	// Create a healer with initial namespace "test-123" and exclusions
	healer, err := createTestHealerWithExclusions([]string{"test-123"}, []string{"test-keep", "test-important"})
	if err != nil {
		t.Fatalf("createTestHealerWithExclusions() error = %v", err)
	}

	// Enable namespace polling
	healer.EnableNamespacePolling = true
	healer.NamespacePattern = ""

	// Create fake namespaces in the cluster
	clientset := healer.ClientSet.(*fake.Clientset)
	namespaces := []*v1.Namespace{
		{ObjectMeta: metav1.ObjectMeta{Name: "test-123"}},    // Already watched
		{ObjectMeta: metav1.ObjectMeta{Name: "test-456"}},    // Should be discovered
		{ObjectMeta: metav1.ObjectMeta{Name: "test-789"}},    // Should be discovered
		{ObjectMeta: metav1.ObjectMeta{Name: "test-keep"}},    // Should be discovered but excluded from deletion
		{ObjectMeta: metav1.ObjectMeta{Name: "test-important"}}, // Should be discovered but excluded from deletion
	}

	for _, ns := range namespaces {
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create namespace %s: %v", ns.Name, err)
		}
	}

	// Discover new namespaces
	healer.discoverNewNamespaces()

	// Check that test-456 and test-789 are discovered
	if !healer.WatchedNamespaces["test-456"] {
		t.Error("test-456 should be discovered and watched")
	}
	if !healer.WatchedNamespaces["test-789"] {
		t.Error("test-789 should be discovered and watched")
	}

	// Check that excluded namespaces are still discovered and watched (but won't be deleted)
	if !healer.WatchedNamespaces["test-keep"] {
		t.Error("test-keep should be discovered and watched (excluded from deletion only)")
	}
	if !healer.WatchedNamespaces["test-important"] {
		t.Error("test-important should be discovered and watched (excluded from deletion only)")
	}
}

func TestHealer_DiscoverNewNamespaces_WildcardPattern(t *testing.T) {
	// Create a healer with wildcard pattern
	healer, err := createTestHealer([]string{"test-*"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	// Enable namespace polling
	healer.EnableNamespacePolling = true
	healer.NamespacePattern = "test-*"

	// Create fake namespaces in the cluster
	clientset := healer.ClientSet.(*fake.Clientset)
	namespaces := []*v1.Namespace{
		{ObjectMeta: metav1.ObjectMeta{Name: "test-123"}}, // Should be discovered
		{ObjectMeta: metav1.ObjectMeta{Name: "test-456"}}, // Should be discovered
		{ObjectMeta: metav1.ObjectMeta{Name: "default"}},  // Should not be discovered
	}

	for _, ns := range namespaces {
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create namespace %s: %v", ns.Name, err)
		}
	}

	// Discover new namespaces
	healer.discoverNewNamespaces()

	// Check that test namespaces are discovered
	if !healer.WatchedNamespaces["test-123"] {
		t.Error("test-123 should be discovered and watched")
	}
	if !healer.WatchedNamespaces["test-456"] {
		t.Error("test-456 should be discovered and watched")
	}

	// Check that default is not watched
	if healer.WatchedNamespaces["default"] {
		t.Error("default should not be discovered (doesn't match pattern)")
	}
}

func TestHealer_DiscoverNewNamespaces_NoPatternsOrPrefixes(t *testing.T) {
	// Create a healer with no patterns or prefixes
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	// Enable namespace polling
	healer.EnableNamespacePolling = true
	healer.NamespacePattern = ""

	// Create fake namespaces in the cluster
	clientset := healer.ClientSet.(*fake.Clientset)
	namespaces := []*v1.Namespace{
		{ObjectMeta: metav1.ObjectMeta{Name: "test-123"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "test-456"}},
	}

	for _, ns := range namespaces {
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create namespace %s: %v", ns.Name, err)
		}
	}

	// Discover new namespaces (should not discover anything without patterns/prefixes)
	initialWatchedCount := len(healer.WatchedNamespaces)
	healer.discoverNewNamespaces()

	// Should not discover any new namespaces
	if len(healer.WatchedNamespaces) != initialWatchedCount {
		t.Errorf("discoverNewNamespaces() should not discover namespaces without patterns/prefixes. Watched count: %d, expected: %d", len(healer.WatchedNamespaces), initialWatchedCount)
	}
}

func TestHealer_DiscoverNewNamespaces_DetectDeleted(t *testing.T) {
	// Create a healer with initial namespace "test-123"
	healer, err := createTestHealer([]string{"test-123"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	// Enable namespace polling
	healer.EnableNamespacePolling = true
	healer.NamespacePattern = ""
	// Set a very high stale age to prevent automatic deletion during this test
	healer.StaleAge = 24 * time.Hour

	clientset := healer.ClientSet.(*fake.Clientset)

	// First, create namespaces including some that match the prefix
	// Set proper CreationTimestamp to avoid age calculation issues
	now := time.Now()
	namespaces := []*v1.Namespace{
		{ObjectMeta: metav1.ObjectMeta{Name: "test-123", CreationTimestamp: metav1.NewTime(now)}}, // Already watched
		{ObjectMeta: metav1.ObjectMeta{Name: "test-456", CreationTimestamp: metav1.NewTime(now)}}, // Will be discovered
		{ObjectMeta: metav1.ObjectMeta{Name: "test-789", CreationTimestamp: metav1.NewTime(now)}}, // Will be discovered
	}

	for _, ns := range namespaces {
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create namespace %s: %v", ns.Name, err)
		}
	}

	// Discover new namespaces
	healer.discoverNewNamespaces()

	// Verify test-456 and test-789 are now watched
	if !healer.WatchedNamespaces["test-456"] {
		t.Error("test-456 should be discovered and watched")
	}
	if !healer.WatchedNamespaces["test-789"] {
		t.Error("test-789 should be discovered and watched")
	}

	// Now delete test-456 and test-789
	err = clientset.CoreV1().Namespaces().Delete(context.TODO(), "test-456", metav1.DeleteOptions{})
	if err != nil {
		t.Fatalf("Failed to delete namespace test-456: %v", err)
	}
	err = clientset.CoreV1().Namespaces().Delete(context.TODO(), "test-789", metav1.DeleteOptions{})
	if err != nil {
		t.Fatalf("Failed to delete namespace test-789: %v", err)
	}

	// Run discovery again - should detect deleted namespaces
	healer.discoverNewNamespaces()

	// Verify deleted namespaces are no longer watched
	if healer.WatchedNamespaces["test-456"] {
		t.Error("test-456 should be removed from watched namespaces after deletion")
	}
	if healer.WatchedNamespaces["test-789"] {
		t.Error("test-789 should be removed from watched namespaces after deletion")
	}

	// Verify test-123 is still watched (it was explicitly specified, not discovered)
	if !healer.WatchedNamespaces["test-123"] {
		t.Error("test-123 should still be watched (explicitly specified)")
	}
}

func TestHealer_DiscoverNewNamespaces_DetectDeleted_WildcardPattern(t *testing.T) {
	// Create a healer with wildcard pattern
	healer, err := createTestHealer([]string{"test-*"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	// Enable namespace polling
	healer.EnableNamespacePolling = true
	healer.NamespacePattern = "test-*"
	// Set a very high stale age to prevent automatic deletion during this test
	healer.StaleAge = 24 * time.Hour

	clientset := healer.ClientSet.(*fake.Clientset)

	// Create namespaces matching the pattern
	// Set proper CreationTimestamp to avoid age calculation issues
	now := time.Now()
	namespaces := []*v1.Namespace{
		{ObjectMeta: metav1.ObjectMeta{Name: "test-123", CreationTimestamp: metav1.NewTime(now)}},
		{ObjectMeta: metav1.ObjectMeta{Name: "test-456", CreationTimestamp: metav1.NewTime(now)}},
	}

	for _, ns := range namespaces {
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("Failed to create namespace %s: %v", ns.Name, err)
		}
	}

	// Discover new namespaces
	healer.discoverNewNamespaces()

	// Verify namespaces are watched
	if !healer.WatchedNamespaces["test-123"] {
		t.Error("test-123 should be discovered and watched")
	}
	if !healer.WatchedNamespaces["test-456"] {
		t.Error("test-456 should be discovered and watched")
	}

	// Delete test-456
	err = clientset.CoreV1().Namespaces().Delete(context.TODO(), "test-456", metav1.DeleteOptions{})
	if err != nil {
		t.Fatalf("Failed to delete namespace test-456: %v", err)
	}

	// Run discovery again - should detect deleted namespace
	healer.discoverNewNamespaces()

	// Verify deleted namespace is no longer watched
	if healer.WatchedNamespaces["test-456"] {
		t.Error("test-456 should be removed from watched namespaces after deletion")
	}

	// Verify test-123 is still watched
	if !healer.WatchedNamespaces["test-123"] {
		t.Error("test-123 should still be watched")
	}
}

func TestHealer_CheckAndDeleteStaleNamespaces(t *testing.T) {
	// Create a healer with initial namespace "test-123"
	healer, err := createTestHealer([]string{"test-123"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	// Set stale age to 6 minutes
	healer.StaleAge = 6 * time.Minute

	clientset := healer.ClientSet.(*fake.Clientset)

	// Create namespaces with different ages
	now := time.Now()
	oldNamespace := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-456",
			CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute)), // Older than threshold
		},
	}
	newNamespace := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-789",
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute)), // Newer than threshold
		},
	}

	_, err = clientset.CoreV1().Namespaces().Create(context.TODO(), oldNamespace, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create old namespace: %v", err)
	}
	_, err = clientset.CoreV1().Namespaces().Create(context.TODO(), newNamespace, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create new namespace: %v", err)
	}

	// Extract patterns and prefixes
	patterns := []string{}
	prefixes := healer.extractPrefixesFromNamespaces()

	// Get all namespaces
	nsList, err := clientset.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Failed to list namespaces: %v", err)
	}

	// Check and delete stale namespaces
	healer.checkAndDeleteStaleNamespaces(nsList.Items, patterns, prefixes)

	// Verify old namespace was deleted
	_, err = clientset.CoreV1().Namespaces().Get(context.TODO(), "test-456", metav1.GetOptions{})
	if err == nil {
		t.Error("test-456 should have been deleted")
	} else if !apierrors.IsNotFound(err) {
		t.Errorf("Expected NotFound error, got: %v", err)
	}

	// Verify new namespace still exists
	_, err = clientset.CoreV1().Namespaces().Get(context.TODO(), "test-789", metav1.GetOptions{})
	if err != nil {
		t.Errorf("test-789 should still exist, got error: %v", err)
	}
}

func TestHealer_CheckAndDeleteStaleNamespaces_Excluded(t *testing.T) {
	// Create a healer with excluded namespace
	healer, err := createTestHealerWithExclusions([]string{"test-123"}, []string{"test-keep"})
	if err != nil {
		t.Fatalf("createTestHealerWithExclusions() error = %v", err)
	}

	// Set stale age to 6 minutes
	healer.StaleAge = 6 * time.Minute

	clientset := healer.ClientSet.(*fake.Clientset)

	// Create an old excluded namespace
	now := time.Now()
	oldExcludedNamespace := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-keep",
			CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute)), // Older than threshold
		},
	}

	_, err = clientset.CoreV1().Namespaces().Create(context.TODO(), oldExcludedNamespace, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create excluded namespace: %v", err)
	}

	// Extract patterns and prefixes
	patterns := []string{}
	prefixes := healer.extractPrefixesFromNamespaces()

	// Get all namespaces
	nsList, err := clientset.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Failed to list namespaces: %v", err)
	}

	// Check and delete stale namespaces
	healer.checkAndDeleteStaleNamespaces(nsList.Items, patterns, prefixes)

	// Verify excluded namespace was NOT deleted
	_, err = clientset.CoreV1().Namespaces().Get(context.TODO(), "test-keep", metav1.GetOptions{})
	if err != nil {
		t.Errorf("test-keep should NOT have been deleted (it's excluded), got error: %v", err)
	}
}

func TestHealer_CheckAndDeleteStaleNamespaces_WildcardPattern(t *testing.T) {
	// Create a healer with wildcard pattern
	healer, err := createTestHealer([]string{"test-*"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	// Set stale age to 6 minutes
	healer.StaleAge = 6 * time.Minute

	clientset := healer.ClientSet.(*fake.Clientset)

	// Create namespaces
	now := time.Now()
	oldNamespace := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-456",
			CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute)), // Older than threshold
		},
	}
	nonMatchingNamespace := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "default",
			CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute)), // Older but doesn't match pattern
		},
	}

	_, err = clientset.CoreV1().Namespaces().Create(context.TODO(), oldNamespace, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create old namespace: %v", err)
	}
	_, err = clientset.CoreV1().Namespaces().Create(context.TODO(), nonMatchingNamespace, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create non-matching namespace: %v", err)
	}

	// Extract patterns and prefixes
	patterns := []string{"test-*"}
	prefixes := make(map[string]bool)

	// Get all namespaces
	nsList, err := clientset.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Failed to list namespaces: %v", err)
	}

	// Check and delete stale namespaces
	healer.checkAndDeleteStaleNamespaces(nsList.Items, patterns, prefixes)

	// Verify matching namespace was deleted
	_, err = clientset.CoreV1().Namespaces().Get(context.TODO(), "test-456", metav1.GetOptions{})
	if err == nil {
		t.Error("test-456 should have been deleted")
	} else if !apierrors.IsNotFound(err) {
		t.Errorf("Expected NotFound error, got: %v", err)
	}

	// Verify non-matching namespace was NOT deleted
	_, err = clientset.CoreV1().Namespaces().Get(context.TODO(), "default", metav1.GetOptions{})
	if err != nil {
		t.Errorf("default should NOT have been deleted (doesn't match pattern), got error: %v", err)
	}
}
