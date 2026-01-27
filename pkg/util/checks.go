package util

import (
	"fmt"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DefaultRestartThreshold is the number of times a container must restart
// before we consider the Pod persistently unhealthy and eligible for healing.
const DefaultRestartThreshold = 3

// IsUnhealthy checks if a Pod exhibits signs of persistent failure that requires healing.
// This function implements the core criteria: currently only CrashLoopBackOff.
func IsUnhealthy(pod *v1.Pod) bool {
	// A Pod is considered unhealthy if any of its containers are in CrashLoopBackOff
	// and have exceeded the restart threshold.
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting != nil && status.State.Waiting.Reason == "CrashLoopBackOff" {
			if status.RestartCount >= DefaultRestartThreshold {
				fmt.Printf("   [Check] 🚨 Pod %s/%s failed check: CrashLoopBackOff (Restarts: %d).\n",
					pod.Namespace, pod.Name, status.RestartCount)
				return true
			}
		}
	}

	// Add checks for other failure phases like PodFailed, or ImagePullBackOff here if needed.

	return false
}

// GetHealReason retrieves the specific reason for the healing action.
func GetHealReason(pod *v1.Pod) string {
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting != nil && status.State.Waiting.Reason == "CrashLoopBackOff" {
			if status.RestartCount >= DefaultRestartThreshold {
				return fmt.Sprintf("Persistent CrashLoopBackOff (Restarts: %d)", status.RestartCount)
			}
		}
	}
	return "Unspecified Failure"
}

// VM-specific constants
const (
	// DefaultNodeNotReadyThreshold is the duration a node must be NotReady before considering it unhealthy
	DefaultNodeNotReadyThreshold = 5 * time.Minute
	// DefaultNodeUnknownThreshold is the duration a node must be Unknown before considering it unhealthy
	DefaultNodeUnknownThreshold = 2 * time.Minute
)

// IsNodeUnhealthy checks if a Node exhibits signs of persistent failure that requires healing.
func IsNodeUnhealthy(node *v1.Node) bool {
	// Check if node is NotReady for too long
	if isNodeNotReady(node) {
		fmt.Printf("   [Check] 🚨 Node %s failed check: NotReady for %v.\n",
			node.Name, getNodeNotReadyDuration(node))
		return true
	}

	// Check if node is Unknown for too long
	if isNodeUnknown(node) {
		fmt.Printf("   [Check] 🚨 Node %s failed check: Unknown for %v.\n",
			node.Name, getNodeUnknownDuration(node))
		return true
	}

	// Check for other failure conditions like DiskPressure, MemoryPressure, etc.
	if hasNodePressureConditions(node) {
		fmt.Printf("   [Check] 🚨 Node %s failed check: Resource pressure conditions detected.\n", node.Name)
		return true
	}

	return false
}

// isNodeNotReady checks if a node has been NotReady for longer than the threshold
func isNodeNotReady(node *v1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == v1.NodeReady && condition.Status == v1.ConditionFalse {
			// Check if the node has been NotReady for longer than threshold
			if time.Since(condition.LastTransitionTime.Time) > DefaultNodeNotReadyThreshold {
				return true
			}
		}
	}
	return false
}

// isNodeUnknown checks if a node has been Unknown for longer than the threshold
func isNodeUnknown(node *v1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == v1.NodeReady && condition.Status == v1.ConditionUnknown {
			// Check if the node has been Unknown for longer than threshold
			if time.Since(condition.LastTransitionTime.Time) > DefaultNodeUnknownThreshold {
				return true
			}
		}
	}
	return false
}

// hasNodePressureConditions checks if a node has resource pressure conditions
func hasNodePressureConditions(node *v1.Node) bool {
	pressureTypes := []v1.NodeConditionType{
		v1.NodeDiskPressure,
		v1.NodeMemoryPressure,
		v1.NodePIDPressure,
	}

	for _, condition := range node.Status.Conditions {
		for _, pressureType := range pressureTypes {
			if condition.Type == pressureType && condition.Status == v1.ConditionTrue {
				// If pressure condition has been true for more than 10 minutes
				if time.Since(condition.LastTransitionTime.Time) > 10*time.Minute {
					return true
				}
			}
		}
	}
	return false
}

// getNodeNotReadyDuration returns how long a node has been NotReady
func getNodeNotReadyDuration(node *v1.Node) time.Duration {
	for _, condition := range node.Status.Conditions {
		if condition.Type == v1.NodeReady && condition.Status == v1.ConditionFalse {
			return time.Since(condition.LastTransitionTime.Time)
		}
	}
	return 0
}

// getNodeUnknownDuration returns how long a node has been Unknown
func getNodeUnknownDuration(node *v1.Node) time.Duration {
	for _, condition := range node.Status.Conditions {
		if condition.Type == v1.NodeReady && condition.Status == v1.ConditionUnknown {
			return time.Since(condition.LastTransitionTime.Time)
		}
	}
	return 0
}

// GetNodeHealReason retrieves the specific reason for healing a node
func GetNodeHealReason(node *v1.Node) string {
	if isNodeNotReady(node) {
		return fmt.Sprintf("Node NotReady for %v", getNodeNotReadyDuration(node))
	}
	if isNodeUnknown(node) {
		return fmt.Sprintf("Node Unknown for %v", getNodeUnknownDuration(node))
	}
	if hasNodePressureConditions(node) {
		return "Node resource pressure conditions"
	}
	return "Unspecified Node Failure"
}

// VM-specific constants for VirtualMachine resources
const (
	// DefaultVMErrorThreshold is the duration a VM must be in error state before considering it unhealthy
	DefaultVMErrorThreshold = 2 * time.Minute
	// ErrorUnschedulableCleanupThreshold is the duration a VM must be in ErrorUnschedulable before cleanup
	ErrorUnschedulableCleanupThreshold = 3 * time.Minute
)

// IsVirtualMachineUnhealthy checks if a VirtualMachine exhibits signs of persistent failure
func IsVirtualMachineUnhealthy(vm *unstructured.Unstructured) bool {
	vmName := vm.GetName()
	vmNamespace := vm.GetNamespace()

	// Get the status field
	status, found, err := unstructured.NestedMap(vm.Object, "status")
	if !found || err != nil {
		return false
	}

	// Check PrintableStatus field
	printableStatus, found, err := unstructured.NestedString(status, "printableStatus")
	if !found || err != nil {
		return false
	}

	// Check if VM is in ErrorUnschedulable state
	// Clean up after 30 seconds in this state (faster than general stale age)
	if printableStatus == "ErrorUnschedulable" {
		// Try to find when the VM entered ErrorUnschedulable state
		errorUnschedulableDuration := getErrorUnschedulableDuration(vm)
		if errorUnschedulableDuration >= ErrorUnschedulableCleanupThreshold {
			fmt.Printf("   [Check] 🚨 VM %s/%s failed check: ErrorUnschedulable for %v (threshold: %v).\n",
				vmNamespace, vmName, errorUnschedulableDuration.Round(time.Second), ErrorUnschedulableCleanupThreshold)
			return true
		} else {
			// VM is in ErrorUnschedulable but hasn't been there long enough yet
			return false
		}
	}

	// Check if VM is in Error state
	if printableStatus == "Error" {
		fmt.Printf("   [Check] 🚨 VM %s/%s failed check: Error state.\n", vmNamespace, vmName)
		return true
	}

	// Check if VM has been in Provisioning state for too long
	if printableStatus == "Provisioning" {
		creationTime := vm.GetCreationTimestamp()
		if creationTime.Time.Add(DefaultVMErrorThreshold).Before(time.Now()) {
			fmt.Printf("   [Check] 🚨 VM %s/%s failed check: Stuck in Provisioning for %v.\n",
				vmNamespace, vmName, time.Since(creationTime.Time))
			return true
		}
	}

	// Check for VM conditions that indicate failure
	if hasVirtualMachineErrorConditions(vm) {
		fmt.Printf("   [Check] 🚨 VM %s/%s failed check: Error conditions detected.\n", vmNamespace, vmName)
		return true
	}

	return false
}

// hasVirtualMachineErrorConditions checks if a VM has error conditions
func hasVirtualMachineErrorConditions(vm *unstructured.Unstructured) bool {
	status, found, err := unstructured.NestedMap(vm.Object, "status")
	if !found || err != nil {
		return false
	}

	conditions, found, err := unstructured.NestedSlice(status, "conditions")
	if !found || err != nil {
		return false
	}

	for _, condition := range conditions {
		conditionMap, ok := condition.(map[string]interface{})
		if !ok {
			continue
		}

		conditionType, found, err := unstructured.NestedString(conditionMap, "type")
		if !found || err != nil {
			continue
		}

		conditionStatus, found, err := unstructured.NestedString(conditionMap, "status")
		if !found || err != nil {
			continue
		}

		// Check for failure conditions
		if conditionType == "Failure" && conditionStatus == "True" {
			lastTransitionTimeStr, found, err := unstructured.NestedString(conditionMap, "lastTransitionTime")
			if !found || err != nil {
				continue
			}

			lastTransitionTime, err := time.Parse(time.RFC3339, lastTransitionTimeStr)
			if err != nil {
				continue
			}

			// If failure condition has been true for more than threshold
			if time.Since(lastTransitionTime) > DefaultVMErrorThreshold {
				return true
			}
		}
	}
	return false
}

// getErrorUnschedulableDuration returns how long a VM has been in ErrorUnschedulable state
func getErrorUnschedulableDuration(vm *unstructured.Unstructured) time.Duration {
	status, found, err := unstructured.NestedMap(vm.Object, "status")
	if !found || err != nil {
		// Fallback to creation time if we can't find status
		return time.Since(vm.GetCreationTimestamp().Time)
	}

	// Check conditions for when the VM entered ErrorUnschedulable state
	conditions, found, err := unstructured.NestedSlice(status, "conditions")
	if found && err == nil {
		var mostRecentTransition time.Time
		for _, condition := range conditions {
			conditionMap, ok := condition.(map[string]interface{})
			if !ok {
				continue
			}

			// Look for conditions that might indicate when ErrorUnschedulable started
			// Common conditions: Ready, Created, Scheduled, etc.
			conditionStatus, found, err := unstructured.NestedString(conditionMap, "status")
			if !found || err != nil {
				continue
			}

			// If condition is False or Unknown, check when it transitioned
			// This helps us determine when the VM entered ErrorUnschedulable
			if conditionStatus == "False" || conditionStatus == "Unknown" {
				lastTransitionTimeStr, found, err := unstructured.NestedString(conditionMap, "lastTransitionTime")
				if found && err == nil {
					lastTransitionTime, err := time.Parse(time.RFC3339, lastTransitionTimeStr)
					if err == nil {
						// Track the most recent transition time
						if lastTransitionTime.After(mostRecentTransition) {
							mostRecentTransition = lastTransitionTime
						}
					}
				}
			}
		}
		// If we found a transition time, use it
		if !mostRecentTransition.IsZero() {
			return time.Since(mostRecentTransition)
		}
	}

	// Fallback: use creation time (ErrorUnschedulable typically happens soon after creation)
	// This is reasonable since ErrorUnschedulable usually occurs during initial scheduling
	return time.Since(vm.GetCreationTimestamp().Time)
}

// GetVirtualMachineHealReason retrieves the specific reason for healing a VM
func GetVirtualMachineHealReason(vm *unstructured.Unstructured) string {
	status, found, err := unstructured.NestedMap(vm.Object, "status")
	if !found || err != nil {
		return "Unspecified VM Failure"
	}

	printableStatus, found, err := unstructured.NestedString(status, "printableStatus")
	if !found || err != nil {
		return "Unspecified VM Failure"
	}

	switch printableStatus {
	case "ErrorUnschedulable":
		duration := getErrorUnschedulableDuration(vm)
		return fmt.Sprintf("VM ErrorUnschedulable for %v (cannot be scheduled to any node)", duration.Round(time.Second))
	case "Error":
		return "VM Error state"
	case "Provisioning":
		creationTime := vm.GetCreationTimestamp()
		return fmt.Sprintf("VM stuck in Provisioning for %v", time.Since(creationTime.Time))
	default:
		if hasVirtualMachineErrorConditions(vm) {
			return "VM error conditions detected"
		}
		return "Unspecified VM Failure"
	}
}

// IsCRDResourceStale checks if a CRD resource is stale and should be cleaned up
func IsCRDResourceStale(resource *unstructured.Unstructured, staleAge time.Duration, checkFinalizers bool) bool {
	resourceName := resource.GetName()
	resourceNamespace := resource.GetNamespace()

	// Check if resource is older than stale age threshold
	creationTime := resource.GetCreationTimestamp()
	if creationTime.Time.Add(staleAge).Before(time.Now()) {
		fmt.Printf("   [Check] 🚨 CRD Resource %s/%s failed check: Older than stale age threshold (%v old).\n",
			resourceNamespace, resourceName, time.Since(creationTime.Time))
		return true
	}

	// Check for stuck finalizers
	if checkFinalizers && len(resource.GetFinalizers()) > 0 {
		// Check if resource has been in terminating state for too long (indicates stuck finalizers)
		deletionTimestamp := resource.GetDeletionTimestamp()
		if deletionTimestamp != nil {
			terminatingDuration := time.Since(deletionTimestamp.Time)
			if terminatingDuration > staleAge {
				fmt.Printf("   [Check] 🚨 CRD Resource %s/%s failed check: Stuck in terminating state for %v (likely stuck finalizers).\n",
					resourceNamespace, resourceName, terminatingDuration)
				return true
			}
		}
	}

	// Check for error conditions in status
	if hasCRDResourceErrorConditions(resource) {
		fmt.Printf("   [Check] 🚨 CRD Resource %s/%s failed check: Error conditions detected in status.\n",
			resourceNamespace, resourceName)
		return true
	}

	// Check for resources in failed/error phases
	if hasCRDResourceFailedPhase(resource) {
		fmt.Printf("   [Check] 🚨 CRD Resource %s/%s failed check: Resource in failed/error phase.\n",
			resourceNamespace, resourceName)
		return true
	}

	return false
}

// hasCRDResourceErrorConditions checks if a CRD resource has error conditions in its status
func hasCRDResourceErrorConditions(resource *unstructured.Unstructured) bool {
	status, found, err := unstructured.NestedMap(resource.Object, "status")
	if !found || err != nil {
		return false
	}

	conditions, found, err := unstructured.NestedSlice(status, "conditions")
	if !found || err != nil {
		return false
	}

	for _, condition := range conditions {
		conditionMap, ok := condition.(map[string]interface{})
		if !ok {
			continue
		}

		conditionType, found, err := unstructured.NestedString(conditionMap, "type")
		if !found || err != nil {
			continue
		}

		conditionStatus, found, err := unstructured.NestedString(conditionMap, "status")
		if !found || err != nil {
			continue
		}

		// Check for error/failure conditions
		errorTypes := []string{"Error", "Failed", "Failure", "Degraded"}
		for _, errorType := range errorTypes {
			if strings.Contains(conditionType, errorType) && conditionStatus == "True" {
				// Check if condition has been true for a while
				lastTransitionTimeStr, found, err := unstructured.NestedString(conditionMap, "lastTransitionTime")
				if !found || err != nil {
					continue
				}

				lastTransitionTime, err := time.Parse(time.RFC3339, lastTransitionTimeStr)
				if err != nil {
					continue
				}

				// If error condition has been true for more than 5 minutes
				if time.Since(lastTransitionTime) > 5*time.Minute {
					return true
				}
			}
		}
	}

	return false
}

// hasCRDResourceFailedPhase checks if a CRD resource is in a failed/error phase
func hasCRDResourceFailedPhase(resource *unstructured.Unstructured) bool {
	status, found, err := unstructured.NestedMap(resource.Object, "status")
	if !found || err != nil {
		return false
	}

	// Check phase field
	phase, found, err := unstructured.NestedString(status, "phase")
	if found && err == nil {
		failedPhases := []string{"Failed", "Error", "Terminated", "Terminating"}
		for _, failedPhase := range failedPhases {
			if phase == failedPhase {
				return true
			}
		}
	}

	// Check printableStatus field (common in KubeVirt resources)
	printableStatus, found, err := unstructured.NestedString(status, "printableStatus")
	if found && err == nil {
		errorStatuses := []string{"Error", "ErrorUnschedulable", "ErrorPvcNotFound", "ErrorDataVolumeNotFound"}
		for _, errorStatus := range errorStatuses {
			if printableStatus == errorStatus {
				return true
			}
		}
	}

	return false
}

// GetCRDResourceStaleReason retrieves the specific reason why a CRD resource is considered stale
func GetCRDResourceStaleReason(resource *unstructured.Unstructured, staleAge time.Duration, checkFinalizers bool) string {
	// Check age
	creationTime := resource.GetCreationTimestamp()
	if creationTime.Time.Add(staleAge).Before(time.Now()) {
		return fmt.Sprintf("Resource older than stale age threshold (%v old)", time.Since(creationTime.Time))
	}

	// Check stuck finalizers
	if checkFinalizers {
		deletionTimestamp := resource.GetDeletionTimestamp()
		if deletionTimestamp != nil {
			terminatingDuration := time.Since(deletionTimestamp.Time)
			if terminatingDuration > staleAge {
				return fmt.Sprintf("Stuck in terminating state for %v (likely stuck finalizers: %v)", terminatingDuration, resource.GetFinalizers())
			}
		}
		if len(resource.GetFinalizers()) > 0 {
			return fmt.Sprintf("Has finalizers that may be stuck: %v", resource.GetFinalizers())
		}
	}

	// Check error conditions
	if hasCRDResourceErrorConditions(resource) {
		return "Error conditions detected in status"
	}

	// Check failed phase
	if hasCRDResourceFailedPhase(resource) {
		status, found, _ := unstructured.NestedMap(resource.Object, "status")
		if found {
			if phase, found, _ := unstructured.NestedString(status, "phase"); found {
				return fmt.Sprintf("Resource in failed phase: %s", phase)
			}
			if printableStatus, found, _ := unstructured.NestedString(status, "printableStatus"); found {
				return fmt.Sprintf("Resource in error status: %s", printableStatus)
			}
		}
		return "Resource in failed/error state"
	}

	return "Unspecified stale condition"
}

// Resource optimization constants
const (
	// DefaultClusterStrainThreshold is the percentage of nodes under pressure before considering cluster strained
	DefaultClusterStrainThreshold = 30.0 // 30% of nodes under pressure
	// DefaultPodCPUThrottleThreshold is the percentage of CPU throttling before considering a pod resource-constrained
	DefaultPodCPUThrottleThreshold = 10.0 // 10% throttling
	// DefaultPodMemoryPressureThreshold is the memory pressure threshold (OOMKilled or high usage)
	DefaultPodMemoryPressureThreshold = 80.0 // 80% memory usage
	// DefaultResourceOptimizationCooldown is the cooldown period between resource optimizations
	DefaultResourceOptimizationCooldown = 5 * time.Minute
)

// ClusterStrainInfo holds information about cluster resource strain
type ClusterStrainInfo struct {
	StrainedNodesCount int
	TotalNodesCount    int
	StrainPercentage   float64
	NodesUnderPressure []string
	HasStrain          bool
}

// IsClusterUnderStrain checks if the cluster is under resource strain
func IsClusterUnderStrain(nodes []*v1.Node, threshold float64) ClusterStrainInfo {
	strainedNodes := []string{}
	totalNodes := len(nodes)

	for _, node := range nodes {
		if isNodeUnderResourcePressure(node) {
			strainedNodes = append(strainedNodes, node.Name)
		}
	}

	strainPercentage := 0.0
	if totalNodes > 0 {
		strainPercentage = (float64(len(strainedNodes)) / float64(totalNodes)) * 100.0
	}

	hasStrain := strainPercentage >= threshold

	return ClusterStrainInfo{
		StrainedNodesCount: len(strainedNodes),
		TotalNodesCount:    totalNodes,
		StrainPercentage:   strainPercentage,
		NodesUnderPressure: strainedNodes,
		HasStrain:          hasStrain,
	}
}

// isNodeUnderResourcePressure checks if a node is currently under resource pressure
func isNodeUnderResourcePressure(node *v1.Node) bool {
	for _, condition := range node.Status.Conditions {
		// Check for immediate pressure conditions (not just persistent ones)
		pressureTypes := []v1.NodeConditionType{
			v1.NodeDiskPressure,
			v1.NodeMemoryPressure,
			v1.NodePIDPressure,
		}

		for _, pressureType := range pressureTypes {
			if condition.Type == pressureType && condition.Status == v1.ConditionTrue {
				// If pressure condition is true (even if recent), node is under pressure
				return true
			}
		}
	}
	return false
}

// PodResourceIssue represents a resource-related issue with a pod
type PodResourceIssue struct {
	HasIssue            bool
	IsOOMKilled         bool
	IsCPUThrottled      bool
	IsMemoryPressure    bool
	IsHighResourceUsage bool
	Reason              string
	RestartCount        int32
}

// IsPodResourceConstrained checks if a pod has resource-related issues that indicate it should be optimized
func IsPodResourceConstrained(pod *v1.Pod) PodResourceIssue {
	issue := PodResourceIssue{
		HasIssue: false,
	}

	// Check container statuses for resource issues
	for _, status := range pod.Status.ContainerStatuses {
		// Check for OOMKilled
		if status.LastTerminationState.Terminated != nil {
			if status.LastTerminationState.Terminated.Reason == "OOMKilled" {
				issue.HasIssue = true
				issue.IsOOMKilled = true
				issue.Reason = fmt.Sprintf("Container %s was OOMKilled", status.Name)
				issue.RestartCount = status.RestartCount
				return issue
			}
		}

		// Check current state for OOMKilled
		if status.State.Terminated != nil && status.State.Terminated.Reason == "OOMKilled" {
			issue.HasIssue = true
			issue.IsOOMKilled = true
			issue.Reason = fmt.Sprintf("Container %s is OOMKilled", status.Name)
			issue.RestartCount = status.RestartCount
			return issue
		}

		// Check for high restart count (indicates resource pressure)
		if status.RestartCount >= 5 {
			issue.HasIssue = true
			issue.Reason = fmt.Sprintf("Container %s has high restart count (%d)", status.Name, status.RestartCount)
			issue.RestartCount = status.RestartCount
		}
	}

	// Check pod conditions for resource pressure
	for _, condition := range pod.Status.Conditions {
		if condition.Type == v1.PodScheduled && condition.Status == v1.ConditionFalse {
			if strings.Contains(condition.Reason, "Insufficient") {
				issue.HasIssue = true
				issue.Reason = fmt.Sprintf("Pod cannot be scheduled: %s", condition.Reason)
			}
		}
	}

	// Check if pod is in Pending state for too long (likely resource constraints)
	if pod.Status.Phase == v1.PodPending {
		creationTime := pod.CreationTimestamp.Time
		if time.Since(creationTime) > 5*time.Minute {
			issue.HasIssue = true
			issue.Reason = fmt.Sprintf("Pod stuck in Pending for %v (likely resource constraints)", time.Since(creationTime))
		}
	}

	return issue
}

// GetPodPriority returns a priority score for eviction (lower = higher priority to evict)
// Priority classes: system-cluster-critical > system-node-critical > high > medium > low > unset
func GetPodPriority(pod *v1.Pod) int {
	priorityClass := pod.Spec.PriorityClassName

	// Map priority classes to scores (lower score = higher priority to evict)
	priorityMap := map[string]int{
		"system-cluster-critical": 100,
		"system-node-critical":    90,
		"high":                    70,
		"medium":                  50,
		"low":                     20,
		"":                        30, // Default priority
	}

	if score, ok := priorityMap[priorityClass]; ok {
		return score
	}

	// Unknown priority class - treat as medium
	return 30
}

// ShouldEvictPodForResourceOptimization determines if a pod should be evicted for resource optimization
func ShouldEvictPodForResourceOptimization(pod *v1.Pod, clusterStrain ClusterStrainInfo) bool {
	// Only evict if cluster is under strain
	if !clusterStrain.HasStrain {
		return false
	}

	// Never evict system-critical pods
	priority := GetPodPriority(pod)
	if priority >= 90 {
		return false
	}

	// Check for resource issues
	resourceIssue := IsPodResourceConstrained(pod)
	if !resourceIssue.HasIssue {
		return false
	}

	// Evict pods with OOMKilled
	if resourceIssue.IsOOMKilled {
		return true
	}

	// Evict pods with high restart count (indicates persistent resource issues)
	if resourceIssue.RestartCount >= 5 {
		return true
	}

	// Evict pods stuck in Pending (likely resource constraints)
	if pod.Status.Phase == v1.PodPending && time.Since(pod.CreationTimestamp.Time) > 5*time.Minute {
		return true
	}

	return false
}

// GetPodEvictionReason returns a human-readable reason for evicting a pod
func GetPodEvictionReason(pod *v1.Pod, clusterStrain ClusterStrainInfo) string {
	resourceIssue := IsPodResourceConstrained(pod)

	if resourceIssue.IsOOMKilled {
		return fmt.Sprintf("Pod OOMKilled (cluster strain: %.1f%%)", clusterStrain.StrainPercentage)
	}

	if resourceIssue.RestartCount >= 5 {
		return fmt.Sprintf("High restart count (%d) indicating resource pressure (cluster strain: %.1f%%)",
			resourceIssue.RestartCount, clusterStrain.StrainPercentage)
	}

	if pod.Status.Phase == v1.PodPending && time.Since(pod.CreationTimestamp.Time) > 5*time.Minute {
		return fmt.Sprintf("Pod stuck in Pending for %v (cluster strain: %.1f%%)",
			time.Since(pod.CreationTimestamp.Time), clusterStrain.StrainPercentage)
	}

	return fmt.Sprintf("Resource optimization (cluster strain: %.1f%%)", clusterStrain.StrainPercentage)
}
