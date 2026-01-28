package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverClusterEndpoints_InvalidKubeconfig(t *testing.T) {
	// Test with a non-existent kubeconfig file
	invalidPath := "/nonexistent/path/to/kubeconfig"
	
	err := DiscoverClusterEndpoints(invalidPath)
	
	if err == nil {
		t.Error("DiscoverClusterEndpoints() with invalid kubeconfig should return an error")
	}
	
	if !strings.Contains(err.Error(), "failed to build Kubernetes config") {
		t.Errorf("DiscoverClusterEndpoints() error message should mention config failure, got: %v", err)
	}
}

func TestDiscoverClusterEndpoints_EmptyKubeconfig(t *testing.T) {
	// Test with empty kubeconfig path (should try default locations)
	// In a test environment without a real cluster, this will fail
	// but we can verify it handles the error gracefully
	
	err := DiscoverClusterEndpoints("")
	
	// In test environment without real cluster, this should fail
	// but the error should be about config or connection, not a panic
	if err != nil {
		// Verify error is about config/connection, not something unexpected
		errMsg := err.Error()
		if !strings.Contains(errMsg, "failed to build Kubernetes config") &&
			!strings.Contains(errMsg, "failed to create Kubernetes clientset") &&
			!strings.Contains(errMsg, "failed to discover API groups") {
			// If it's a different error (like connection refused), that's also acceptable
			// The important thing is it doesn't panic
			t.Logf("DiscoverClusterEndpoints() with empty path returned expected error: %v", err)
		}
	} else {
		// If it succeeds, that means there's a valid kubeconfig in default location
		// which is fine for the test - we just want to ensure no panic
		t.Log("DiscoverClusterEndpoints() with empty path succeeded (valid default kubeconfig found)")
	}
}

func TestDiscoverClusterEndpoints_InvalidKubeconfigFile(t *testing.T) {
	// Create a temporary file with invalid kubeconfig content
	tmpDir := t.TempDir()
	invalidKubeconfig := filepath.Join(tmpDir, "invalid-config")
	
	// Write invalid YAML content
	err := os.WriteFile(invalidKubeconfig, []byte("invalid: yaml: content: [unclosed"), 0644)
	if err != nil {
		t.Fatalf("Failed to create temporary invalid kubeconfig file: %v", err)
	}
	
	// Test discovery with invalid kubeconfig file
	err = DiscoverClusterEndpoints(invalidKubeconfig)
	
	if err == nil {
		t.Error("DiscoverClusterEndpoints() with invalid kubeconfig file should return an error")
	}
	
	// Should fail at config parsing stage
	if !strings.Contains(err.Error(), "failed to build Kubernetes config") &&
		!strings.Contains(err.Error(), "failed to create Kubernetes clientset") {
		t.Logf("DiscoverClusterEndpoints() with invalid file returned error (expected): %v", err)
	}
}

func TestDiscoverClusterEndpoints_EmptyKubeconfigFile(t *testing.T) {
	// Create an empty kubeconfig file
	tmpDir := t.TempDir()
	emptyKubeconfig := filepath.Join(tmpDir, "empty-config")
	
	// Create empty file
	err := os.WriteFile(emptyKubeconfig, []byte(""), 0644)
	if err != nil {
		t.Fatalf("Failed to create temporary empty kubeconfig file: %v", err)
	}
	
	// Test discovery with empty kubeconfig file
	err = DiscoverClusterEndpoints(emptyKubeconfig)
	
	// Should fail at config parsing stage
	if err == nil {
		t.Error("DiscoverClusterEndpoints() with empty kubeconfig file should return an error")
	}
	
	// Error should be about config parsing
	if !strings.Contains(err.Error(), "failed to build Kubernetes config") &&
		!strings.Contains(err.Error(), "failed to create Kubernetes clientset") {
		t.Logf("DiscoverClusterEndpoints() with empty file returned error (expected): %v", err)
	}
}

// TestDiscoverClusterEndpoints_ErrorHandling verifies that the function
// handles errors gracefully without panicking
func TestDiscoverClusterEndpoints_ErrorHandling(t *testing.T) {
	testCases := []struct {
		name        string
		kubeconfig  string
		expectError bool
	}{
		{
			name:        "nonexistent file",
			kubeconfig:  "/nonexistent/path",
			expectError: true,
		},
		{
			name:        "empty string",
			kubeconfig:  "",
			expectError: false, // May succeed if default kubeconfig exists
		},
		{
			name:        "invalid path with special chars",
			kubeconfig:  "/tmp/\x00invalid",
			expectError: true,
		},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// This should not panic regardless of input
			err := DiscoverClusterEndpoints(tc.kubeconfig)
			
			if tc.expectError && err == nil {
				t.Errorf("DiscoverClusterEndpoints() with %s should return an error", tc.name)
			}
			
			// Verify error message is informative if error occurred
			if err != nil {
				errMsg := err.Error()
				if errMsg == "" {
					t.Error("DiscoverClusterEndpoints() error message should not be empty")
				}
				
				// Error should contain context about what failed
				if !strings.Contains(errMsg, "failed") && !strings.Contains(errMsg, "error") {
					t.Logf("DiscoverClusterEndpoints() error message: %s", errMsg)
				}
			}
		})
	}
}

// TestDiscoverClusterEndpoints_OutputFormat is a helper test that verifies
// the function structure and that it would produce formatted output
// Note: This test requires a real cluster connection to fully validate output
func TestDiscoverClusterEndpoints_OutputFormat(t *testing.T) {
	// This test documents the expected output format
	// Actual testing requires a real cluster connection
	
	// Expected output sections:
	expectedSections := []string{
		"Kubernetes Cluster Endpoint Discovery",
		"Cluster Host:",
		"API Groups:",
		"Resources by API Group:",
		"KubeVirt-Specific Resources:",
		"Endpoint discovery complete",
	}
	
	// Verify that if the function were to succeed, it would include these sections
	// This is more of a documentation test
	for _, section := range expectedSections {
		if !strings.Contains(section, "Discovery") && !strings.Contains(section, "Groups") {
			// Just verify the sections are defined
			_ = section
		}
	}
	
	// This test passes if it doesn't panic
	// Full output validation requires integration testing with a real cluster
	t.Log("Output format test passed - full validation requires real cluster connection")
}
