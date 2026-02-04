package healer

import (
	"context"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

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
		{ObjectMeta: metav1.ObjectMeta{Name: "test-123"}},       // Already watched
		{ObjectMeta: metav1.ObjectMeta{Name: "test-456"}},       // Should be discovered
		{ObjectMeta: metav1.ObjectMeta{Name: "test-789"}},       // Should be discovered
		{ObjectMeta: metav1.ObjectMeta{Name: "test-keep"}},      // Should be discovered but excluded from deletion
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
