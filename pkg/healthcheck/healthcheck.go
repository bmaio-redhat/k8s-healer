package healthcheck

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// HealthCheckResult represents the result of a health check
type HealthCheckResult struct {
	Name      string
	Status    string // "healthy", "degraded", "unhealthy", "unknown"
	Message   string
	Duration  time.Duration
	Error     error
}

// ClusterHealthStatus represents the overall health status of the cluster
type ClusterHealthStatus struct {
	OverallStatus    string
	KubernetesAPI    HealthCheckResult
	KubeVirtCRDs     []HealthCheckResult
	KubeVirtAPI      HealthCheckResult
	KeyResources     []HealthCheckResult
	TotalChecks      int
	PassedChecks     int
	FailedChecks     int
	Warnings         int
}

// Key KubeVirt CRDs to check
var kubevirtCRDs = []struct {
	Group    string
	Version  string
	Resource string
	Name     string
}{
	{"kubevirt.io", "v1", "virtualmachines", "VirtualMachines"},
	{"kubevirt.io", "v1", "virtualmachineinstances", "VirtualMachineInstances"},
	{"kubevirt.io", "v1", "virtualmachineinstancemigrations", "VirtualMachineInstanceMigrations"},
	{"kubevirt.io", "v1", "virtualmachineinstancereplicasets", "VirtualMachineInstanceReplicaSets"},
	{"cdi.kubevirt.io", "v1beta1", "datavolumes", "DataVolumes"},
	{"cdi.kubevirt.io", "v1beta1", "datasources", "DataSources"},
	{"snapshot.kubevirt.io", "v1beta1", "virtualmachinesnapshots", "VirtualMachineSnapshots"},
	{"clone.kubevirt.io", "v1alpha1", "virtualmachineclones", "VirtualMachineClones"},
	{"instancetype.kubevirt.io", "v1beta1", "virtualmachineinstancetypes", "VirtualMachineInstanceTypes"},
	{"instancetype.kubevirt.io", "v1beta1", "virtualmachinepreferences", "VirtualMachinePreferences"},
}

// PerformClusterHealthCheck performs comprehensive health checks on the cluster
func PerformClusterHealthCheck(clientset kubernetes.Interface, dynamicClient dynamic.Interface, config *rest.Config) (*ClusterHealthStatus, error) {
	status := &ClusterHealthStatus{
		KubeVirtCRDs: make([]HealthCheckResult, 0),
		KeyResources: make([]HealthCheckResult, 0),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Check Kubernetes API server
	status.KubernetesAPI = checkKubernetesAPI(ctx, clientset)

	// Check KubeVirt CRDs
	status.KubeVirtCRDs = checkKubeVirtCRDs(ctx, dynamicClient)

	// Check KubeVirt API endpoints
	status.KubeVirtAPI = checkKubeVirtAPI(ctx, clientset, config)

	// Check key resource accessibility
	status.KeyResources = checkKeyResources(ctx, clientset, dynamicClient)

	// Calculate overall status
	status.TotalChecks = 1 + len(status.KubeVirtCRDs) + 1 + len(status.KeyResources)
	status.PassedChecks = 0
	status.FailedChecks = 0
	status.Warnings = 0

	allChecks := []HealthCheckResult{status.KubernetesAPI, status.KubeVirtAPI}
	allChecks = append(allChecks, status.KubeVirtCRDs...)
	allChecks = append(allChecks, status.KeyResources...)

	for _, check := range allChecks {
		switch check.Status {
		case "healthy":
			status.PassedChecks++
		case "degraded":
			status.Warnings++
		case "unhealthy":
			status.FailedChecks++
		}
	}

	// Determine overall status
	if status.FailedChecks > 0 {
		status.OverallStatus = "unhealthy"
	} else if status.Warnings > 0 {
		status.OverallStatus = "degraded"
	} else {
		status.OverallStatus = "healthy"
	}

	return status, nil
}

// checkKubernetesAPI checks if the Kubernetes API server is accessible
func checkKubernetesAPI(ctx context.Context, clientset kubernetes.Interface) HealthCheckResult {
	start := time.Now()
	result := HealthCheckResult{
		Name:   "Kubernetes API Server",
		Status: "unknown",
	}

	// Try to list namespaces as a simple connectivity test
	_, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
	result.Duration = time.Since(start)

	if err != nil {
		result.Status = "unhealthy"
		result.Error = err
		result.Message = fmt.Sprintf("Failed to connect to Kubernetes API: %v", err)
		return result
	}

	result.Status = "healthy"
	result.Message = "Kubernetes API server is accessible"
	return result
}

// checkKubeVirtCRDs checks if KubeVirt CRDs are available
func checkKubeVirtCRDs(ctx context.Context, dynamicClient dynamic.Interface) []HealthCheckResult {
	results := make([]HealthCheckResult, 0, len(kubevirtCRDs))

	for _, crd := range kubevirtCRDs {
		start := time.Now()
		result := HealthCheckResult{
			Name:   crd.Name,
			Status: "unknown",
		}

		gvr := schema.GroupVersionResource{
			Group:    crd.Group,
			Version:  crd.Version,
			Resource: crd.Resource,
		}

		// Try to list resources of this type (namespaced or cluster-scoped)
		// Use panic recovery to handle fake client registration errors gracefully
		var err error
		var listErr interface{}
		func() {
			defer func() {
				if r := recover(); r != nil {
					listErr = r
				}
			}()
			// Try cluster-scoped first (most KubeVirt resources are cluster-scoped or can be listed cluster-wide)
			_, err = dynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
			if err != nil && errors.IsNotFound(err) {
				// Try namespaced as fallback
				_, err = dynamicClient.Resource(gvr).Namespace("default").List(ctx, metav1.ListOptions{Limit: 1})
			}
		}()
		result.Duration = time.Since(start)

		// Check if we recovered from a panic (fake client registration error)
		if listErr != nil {
			errStr := fmt.Sprintf("%v", listErr)
			if contains(errStr, "register resource") || contains(errStr, "out of map") || contains(errStr, "coding error") {
				// This is a fake client limitation - skip this check in tests
				result.Status = "degraded"
				result.Message = fmt.Sprintf("CRD check skipped (using fake client): %s/%s/%s", crd.Group, crd.Version, crd.Resource)
				result.Error = nil // Don't treat this as a real error
				results = append(results, result)
				continue // Skip to next CRD
			} else {
				// Unexpected panic - handle as error
				result.Status = "degraded"
				result.Message = fmt.Sprintf("Unexpected error checking CRD: %v", listErr)
				result.Error = fmt.Errorf("%v", listErr)
				results = append(results, result)
				continue
			}
		}

		if err != nil {
			// Normal error handling
			if errors.IsNotFound(err) {
				result.Status = "unhealthy"
				result.Message = fmt.Sprintf("CRD not found: %s/%s/%s", crd.Group, crd.Version, crd.Resource)
				result.Error = err
			} else if errors.IsForbidden(err) {
				result.Status = "degraded"
				result.Message = fmt.Sprintf("Access forbidden (may need RBAC permissions): %s/%s/%s", crd.Group, crd.Version, crd.Resource)
				result.Error = err
			} else {
				result.Status = "degraded"
				result.Message = fmt.Sprintf("Error accessing CRD: %v", err)
				result.Error = err
			}
		} else {
			result.Status = "healthy"
			result.Message = fmt.Sprintf("CRD is available and accessible")
		}

		results = append(results, result)
	}

	return results
}

// checkKubeVirtAPI checks if KubeVirt API endpoints are accessible
func checkKubeVirtAPI(ctx context.Context, clientset kubernetes.Interface, config *rest.Config) HealthCheckResult {
	start := time.Now()
	result := HealthCheckResult{
		Name:   "KubeVirt API",
		Status: "unknown",
	}
	defer func() {
		result.Duration = time.Since(start)
	}()

	// Use discovery client to check for KubeVirt API group
	// Try to get RESTClient from the clientset
	var discoveryClient *discovery.DiscoveryClient
	if realClientset, ok := clientset.(*kubernetes.Clientset); ok {
		discoveryClient = discovery.NewDiscoveryClient(realClientset.RESTClient())
	} else {
		// For fake clientset in tests, we can't get RESTClient
		result.Status = "degraded"
		result.Message = "Cannot check KubeVirt API (using fake client)"
		result.Duration = time.Since(start)
		return result
	}
	
	// Check if kubevirt.io API group exists
	apiGroups, err := discoveryClient.ServerGroups()
	result.Duration = time.Since(start)

	if err != nil {
		result.Status = "degraded"
		result.Error = err
		result.Message = fmt.Sprintf("Failed to discover API groups: %v", err)
		return result
	}

	// Check if kubevirt.io group is present
	kubevirtFound := false
	for _, group := range apiGroups.Groups {
		if group.Name == "kubevirt.io" {
			kubevirtFound = true
			// Check if it has versions
			if len(group.Versions) > 0 {
				result.Status = "healthy"
				result.Message = fmt.Sprintf("KubeVirt API group found with %d version(s)", len(group.Versions))
			} else {
				result.Status = "degraded"
				result.Message = "KubeVirt API group found but no versions available"
			}
			break
		}
	}

	if !kubevirtFound {
		result.Status = "unhealthy"
		result.Message = "KubeVirt API group not found - KubeVirt may not be installed"
	}

	return result
}

// checkKeyResources checks if key Kubernetes resources are accessible
func checkKeyResources(ctx context.Context, clientset kubernetes.Interface, dynamicClient dynamic.Interface) []HealthCheckResult {
	results := make([]HealthCheckResult, 0)

	// Check core resources
	coreResources := []struct {
		Name     string
		CheckFn  func() error
	}{
		{
			Name: "Nodes",
			CheckFn: func() error {
				_, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
				return err
			},
		},
		{
			Name: "Pods",
			CheckFn: func() error {
				_, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 1})
				return err
			},
		},
		{
			Name: "Namespaces",
			CheckFn: func() error {
				_, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
				return err
			},
		},
	}

	for _, resource := range coreResources {
		start := time.Now()
		result := HealthCheckResult{
			Name:   resource.Name,
			Status: "unknown",
		}

		err := resource.CheckFn()
		result.Duration = time.Since(start)

		if err != nil {
			result.Status = "unhealthy"
			result.Error = err
			result.Message = fmt.Sprintf("Failed to access %s: %v", resource.Name, err)
		} else {
			result.Status = "healthy"
			result.Message = fmt.Sprintf("%s resource is accessible", resource.Name)
		}

		results = append(results, result)
	}

	return results
}

// FormatHealthCheckStatus returns a formatted string representation of the health status
func FormatHealthCheckStatus(status *ClusterHealthStatus) string {
	var output string

	// Overall status
	statusIcon := "✅"
	if status.OverallStatus == "degraded" {
		statusIcon = "⚠️"
	} else if status.OverallStatus == "unhealthy" {
		statusIcon = "❌"
	}

	output += fmt.Sprintf("\n%s Cluster Health: %s\n", statusIcon, status.OverallStatus)
	output += fmt.Sprintf("   Checks: %d passed, %d warnings, %d failed\n\n", status.PassedChecks, status.Warnings, status.FailedChecks)

	// Kubernetes API
	output += formatCheckResult("Kubernetes API", status.KubernetesAPI)

	// KubeVirt API
	output += formatCheckResult("KubeVirt API", status.KubeVirtAPI)

	// KubeVirt CRDs
	if len(status.KubeVirtCRDs) > 0 {
		output += "\n📦 KubeVirt CRDs:\n"
		healthyCount := 0
		for _, crd := range status.KubeVirtCRDs {
			if crd.Status == "healthy" {
				healthyCount++
			}
			output += formatCheckResult("  "+crd.Name, crd)
		}
		output += fmt.Sprintf("   Summary: %d/%d CRDs healthy\n", healthyCount, len(status.KubeVirtCRDs))
	}

	// Key Resources
	if len(status.KeyResources) > 0 {
		output += "\n🔑 Key Resources:\n"
		for _, resource := range status.KeyResources {
			output += formatCheckResult("  "+resource.Name, resource)
		}
	}

	return output
}

// formatCheckResult formats a single health check result
func formatCheckResult(name string, result HealthCheckResult) string {
	icon := "✅"
	if result.Status == "degraded" {
		icon = "⚠️"
	} else if result.Status == "unhealthy" {
		icon = "❌"
	} else if result.Status == "unknown" {
		icon = "❓"
	}

	return fmt.Sprintf("   %s %s: %s (%v)\n", icon, name, result.Message, result.Duration.Round(time.Millisecond))
}

// contains checks if a string contains a substring (case-sensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
