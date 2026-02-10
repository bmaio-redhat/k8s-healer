package healer

import (
	"testing"
	"time"

	"github.com/bmaio-redhat/k8s-healer/pkg/util"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
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
		ClientSet:                        clientset,
		DynamicClient:                    dynamicClient,
		Namespaces:                       namespaces,
		StopCh:                           make(chan struct{}),
		HealedPods:                       make(map[string]time.Time),
		HealedNodes:                      make(map[string]time.Time),
		HealedVMs:                        make(map[string]time.Time),
		HealedCRDs:                       make(map[string]time.Time),
		TrackedCRDs:                      make(map[string]bool),
		HealCooldown:                     10 * time.Minute,
		EnableVMHealing:                  true,
		EnableCRDCleanup:                 true,
		CRDResources:                     []string{"virtualmachines.kubevirt.io/v1"},
		StaleAge:                         6 * time.Minute,
		CleanupFinalizers:                true,
		EnableResourceOptimization:       true,
		StrainThreshold:                  util.DefaultClusterStrainThreshold,
		OptimizedPods:                    make(map[string]time.Time),
		EnableNamespacePolling:           false,
		NamespacePattern:                 "",
		NamespacePollInterval:            5 * time.Second,
		WatchedNamespaces:                watchedNamespaces,
		EnableResourceCreationThrottling: true,
		CurrentClusterStrain:             nil,
		ExcludedNamespaces:               excludedNamespaces,
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

// TestForceDeleteOptions_UseImmediateDeletion ensures all healer deletions use GracePeriodSeconds=0
// so stale/terminating resources are removed immediately (no grace period).
func TestForceDeleteOptions_UseImmediateDeletion(t *testing.T) {
	if forceDeleteOptions.GracePeriodSeconds == nil {
		t.Fatal("forceDeleteOptions.GracePeriodSeconds should be set for force delete")
	}
	if *forceDeleteOptions.GracePeriodSeconds != 0 {
		t.Errorf("forceDeleteOptions.GracePeriodSeconds = %d, want 0 for immediate deletion of stale/terminating resources",
			*forceDeleteOptions.GracePeriodSeconds)
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
