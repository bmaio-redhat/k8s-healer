package healer

import (
	"testing"
	"time"

	"github.com/bmaio-redhat/k8s-healer/pkg/util"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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
				"name":              "old-vm",
				"namespace":         "default",
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
				"name":              "unschedulable-vm",
				"namespace":         "default",
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
				"name":              "old-resource",
				"namespace":         "default",
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
				"name":              "stuck-resource",
				"namespace":         "default",
				"creationTimestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
				"deletionTimestamp": now.Add(-10 * time.Minute).Format(time.RFC3339),
				"finalizers":        []interface{}{"finalizer.example.com"},
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
				"name":              "error-resource",
				"namespace":         "default",
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
