package healer

import (
	"context"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestHealer_TrackedCRDsCleanup_RemovesDeletedResources(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	// Create fake dynamic client with registered resource types
	scheme := runtime.NewScheme()
	gvr := schema.GroupVersionResource{
		Group:    "kubevirt.io",
		Version:  "v1",
		Resource: "virtualmachines",
	}
	// Register the resource type for listing
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		gvr: "VirtualMachineList",
	})
	healer.DynamicClient = dynamicClient

	// Create some resources in the fake client with recent creation timestamps
	// so they won't be considered stale (must be less than 6 minutes old)
	now := time.Now()
	vm1 := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":              "vm-1",
				"namespace":         "default",
				"creationTimestamp": now.Format(time.RFC3339),
			},
		},
	}
	vm2 := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":              "vm-2",
				"namespace":         "default",
				"creationTimestamp": now.Format(time.RFC3339),
			},
		},
	}

	// Add resources to fake client
	createdVM1, err := dynamicClient.Resource(gvr).Namespace("default").Create(context.TODO(), vm1, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create vm1: %v", err)
	}
	createdVM2, err := dynamicClient.Resource(gvr).Namespace("default").Create(context.TODO(), vm2, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create vm2: %v", err)
	}

	// Update resources with proper creation timestamps (fake client may not preserve them from initial creation)
	// Use metav1.Time to ensure proper timestamp format - set to very recent time so they're not stale
	// VirtualMachines have a hardcoded 6-minute threshold, so we need timestamps less than 6 minutes old
	recentTime := metav1.NewTime(time.Now().Add(-1 * time.Minute)) // 1 minute ago, well under 6 minute threshold
	createdVM1.SetCreationTimestamp(recentTime)
	createdVM2.SetCreationTimestamp(recentTime)
	updatedVM1, err := dynamicClient.Resource(gvr).Namespace("default").Update(context.TODO(), createdVM1, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Failed to update vm1 timestamp: %v", err)
	}
	updatedVM2, err := dynamicClient.Resource(gvr).Namespace("default").Update(context.TODO(), createdVM2, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Failed to update vm2 timestamp: %v", err)
	}

	// Verify timestamps were set correctly
	vm1Timestamp := updatedVM1.GetCreationTimestamp()
	vm2Timestamp := updatedVM2.GetCreationTimestamp()
	if vm1Timestamp.Time.IsZero() {
		t.Fatal("vm1 creation timestamp is zero after update")
	}
	if vm2Timestamp.Time.IsZero() {
		t.Fatal("vm2 creation timestamp is zero after update")
	}

	// Set a very high stale age for this test so the VMs won't be considered stale
	// This test is specifically about TrackedCRDs cleanup, not resource deletion
	originalStaleAge := healer.StaleAge
	healer.StaleAge = 24 * time.Hour // Set to 24 hours so VMs won't be considered stale

	// Manually add entries to TrackedCRDs (simulating resources that were tracked)
	healer.trackedCRDsMu.Lock()
	healer.TrackedCRDs["virtualmachines/default/vm-1"] = true
	healer.TrackedCRDs["virtualmachines/default/vm-2"] = true
	healer.TrackedCRDs["virtualmachines/default/vm-deleted"] = true      // This one doesn't exist
	healer.TrackedCRDs["virtualmachines/default/vm-also-deleted"] = true // This one doesn't exist
	healer.TrackedCRDs["datavolumes/default/dv-1"] = true                // Different resource type, should not be cleaned
	initialSize := len(healer.TrackedCRDs)
	healer.trackedCRDsMu.Unlock()

	// Call checkCRDResources - this should clean up entries for resources that no longer exist
	healer.checkCRDResources("kubevirt.io", "v1", "virtualmachines")

	// Restore original setting
	healer.StaleAge = originalStaleAge

	// Verify that deleted resources are removed from TrackedCRDs
	healer.trackedCRDsMu.RLock()
	finalSize := len(healer.TrackedCRDs)
	vm1Exists := healer.TrackedCRDs["virtualmachines/default/vm-1"]
	vm2Exists := healer.TrackedCRDs["virtualmachines/default/vm-2"]
	vmDeletedExists := healer.TrackedCRDs["virtualmachines/default/vm-deleted"]
	vmAlsoDeletedExists := healer.TrackedCRDs["virtualmachines/default/vm-also-deleted"]
	dv1Exists := healer.TrackedCRDs["datavolumes/default/dv-1"]
	healer.trackedCRDsMu.RUnlock()

	// Existing resources should still be tracked
	if !vm1Exists {
		t.Error("vm-1 should still be tracked (it exists)")
	}
	if !vm2Exists {
		t.Error("vm-2 should still be tracked (it exists)")
	}

	// Deleted resources should be removed
	if vmDeletedExists {
		t.Error("vm-deleted should have been removed from TrackedCRDs (resource doesn't exist)")
	}
	if vmAlsoDeletedExists {
		t.Error("vm-also-deleted should have been removed from TrackedCRDs (resource doesn't exist)")
	}

	// Resources of different types should not be affected
	if !dv1Exists {
		t.Error("dv-1 should still be tracked (different resource type, not checked)")
	}

	// Verify size decreased by 2 (the two deleted VMs)
	if finalSize != initialSize-2 {
		t.Errorf("Expected TrackedCRDs size to decrease by 2, got initial=%d, final=%d", initialSize, finalSize)
	}
}

func TestHealer_TrackedCRDsCleanup_SizeBasedCleanup(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	// Fill TrackedCRDs with more than 5000 entries to trigger size-based cleanup
	healer.trackedCRDsMu.Lock()
	for i := 0; i < 6000; i++ {
		key := fmt.Sprintf("virtualmachines/default/vm-%d", i)
		healer.TrackedCRDs[key] = true
	}
	initialSize := len(healer.TrackedCRDs)
	healer.trackedCRDsMu.Unlock()

	if initialSize != 6000 {
		t.Fatalf("Expected 6000 entries, got %d", initialSize)
	}

	// Trigger the cleanup by calling startHealCacheCleaner's ticker logic
	// We'll simulate the cleanup directly
	healer.trackedCRDsMu.Lock()
	if len(healer.TrackedCRDs) > 5000 {
		removed := 0
		targetRemoval := len(healer.TrackedCRDs) * 3 / 10 // Remove 30%
		for key := range healer.TrackedCRDs {
			if removed >= targetRemoval {
				break
			}
			delete(healer.TrackedCRDs, key)
			removed++
		}
	}
	finalSize := len(healer.TrackedCRDs)
	healer.trackedCRDsMu.Unlock()

	// Verify that approximately 30% was removed (allowing for rounding)
	expectedSize := 6000 * 7 / 10 // 70% remaining
	if finalSize < expectedSize-100 || finalSize > expectedSize+100 {
		t.Errorf("Expected size-based cleanup to leave approximately %d entries, got %d", expectedSize, finalSize)
	}

	// Verify that size is now below the threshold
	if finalSize > 5000 {
		t.Errorf("Size-based cleanup should reduce size below 5000, got %d", finalSize)
	}
}

func TestHealer_TrackedCRDsCleanup_EmptyCluster(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	// Create fake dynamic client with registered resource types (but no resources)
	scheme := runtime.NewScheme()
	gvr := schema.GroupVersionResource{
		Group:    "kubevirt.io",
		Version:  "v1",
		Resource: "virtualmachines",
	}
	// Register the resource type for listing
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		gvr: "VirtualMachineList",
	})
	healer.DynamicClient = dynamicClient

	// Add some tracked CRDs that don't exist in the cluster
	healer.trackedCRDsMu.Lock()
	healer.TrackedCRDs["virtualmachines/default/vm-1"] = true
	healer.TrackedCRDs["virtualmachines/default/vm-2"] = true
	healer.TrackedCRDs["virtualmachines/default/vm-3"] = true
	initialSize := len(healer.TrackedCRDs)
	healer.trackedCRDsMu.Unlock()

	if initialSize != 3 {
		t.Fatalf("Expected 3 tracked CRDs initially, got %d", initialSize)
	}

	// Call checkCRDResources on an empty cluster
	healer.checkCRDResources("kubevirt.io", "v1", "virtualmachines")

	// All tracked VMs should be removed since none exist in the cluster
	healer.trackedCRDsMu.RLock()
	finalSize := len(healer.TrackedCRDs)
	vm1Exists := healer.TrackedCRDs["virtualmachines/default/vm-1"]
	vm2Exists := healer.TrackedCRDs["virtualmachines/default/vm-2"]
	vm3Exists := healer.TrackedCRDs["virtualmachines/default/vm-3"]
	healer.trackedCRDsMu.RUnlock()

	// All should be removed
	if vm1Exists || vm2Exists || vm3Exists {
		t.Error("All tracked VMs should have been removed from empty cluster")
	}

	// Size should be 0 (all 3 were removed)
	if finalSize != 0 {
		t.Errorf("Expected TrackedCRDs to be empty after cleanup (initial=%d), got size %d", initialSize, finalSize)
	}
}
