package healer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestIsRateLimitError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"non-rate error", fmt.Errorf("resource not found"), false},
		{"rate only (no Wait/limiter/deadline)", fmt.Errorf("rate"), false},
		{"rate limiter Wait (client message)", fmt.Errorf("rate limiter Wait would exceed context deadline"), true},
		{"rate and Wait", fmt.Errorf("rate limiter Wait"), true},
		{"rate and limiter", fmt.Errorf("rate limiter refused"), true},
		{"rate and would exceed context deadline", fmt.Errorf("rate would exceed context deadline"), true},
		{"other error with rate in text", fmt.Errorf("operation not permitted"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRateLimitError(tt.err)
			if got != tt.want {
				t.Errorf("isRateLimitError() = %v, want %v (err: %v)", got, tt.want, tt.err)
			}
		})
	}
}

func TestHealer_DoCRDCleanup_ResourceNotFound(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	scheme := runtime.NewScheme()
	gvr := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{gvr: "VirtualMachineList"})
	healer.DynamicClient = dynamicClient

	resource := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":      "nonexistent-vm",
				"namespace": "default",
			},
		},
	}
	resourceKey := "virtualmachines/default/nonexistent-vm"
	healer.trackedCRDsMu.Lock()
	healer.TrackedCRDs[resourceKey] = true
	healer.trackedCRDsMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done, err := healer.doCRDCleanup(ctx, resource, gvr, "nonexistent-vm", "default", resourceKey)
	if err != nil {
		t.Errorf("doCRDCleanup() err = %v", err)
	}
	if !done {
		t.Error("doCRDCleanup() done = false, want true (resource not found should be treated as done)")
	}
	healer.trackedCRDsMu.RLock()
	_, stillTracked := healer.TrackedCRDs[resourceKey]
	healer.trackedCRDsMu.RUnlock()
	if stillTracked {
		t.Error("TrackedCRDs should no longer contain resource key when resource not found")
	}
}

func TestHealer_DoCRDCleanup_Success(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	scheme := runtime.NewScheme()
	gvr := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{gvr: "VirtualMachineList"})
	healer.DynamicClient = dynamicClient

	vm := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":      "to-delete-vm",
				"namespace": "default",
			},
		},
	}
	_, err = dynamicClient.Resource(gvr).Namespace("default").Create(context.TODO(), vm, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create VM: %v", err)
	}

	resourceKey := "virtualmachines/default/to-delete-vm"
	healer.trackedCRDsMu.Lock()
	healer.TrackedCRDs[resourceKey] = true
	healer.trackedCRDsMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done, err := healer.doCRDCleanup(ctx, vm, gvr, "to-delete-vm", "default", resourceKey)
	if err != nil {
		t.Errorf("doCRDCleanup() err = %v", err)
	}
	if !done {
		t.Error("doCRDCleanup() done = false, want true")
	}
	healer.trackedCRDsMu.RLock()
	_, stillTracked := healer.TrackedCRDs[resourceKey]
	healer.trackedCRDsMu.RUnlock()
	if stillTracked {
		t.Error("TrackedCRDs should no longer contain resource key after successful delete")
	}
	_, getErr := dynamicClient.Resource(gvr).Namespace("default").Get(ctx, "to-delete-vm", metav1.GetOptions{})
	if !apierrors.IsNotFound(getErr) {
		t.Errorf("Resource should be deleted; Get err = %v", getErr)
	}
	// Successful delete should have recorded cleanup count
	healer.crdCleanupCountsMu.RLock()
	count := healer.CRDCleanupCounts["virtualmachines.kubevirt.io/v1"]
	healer.crdCleanupCountsMu.RUnlock()
	if count != 1 {
		t.Errorf("CRDCleanupCounts[virtualmachines.kubevirt.io/v1] = %d, want 1", count)
	}
}

func TestHealer_RecordCRDCleanup(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	gvr1 := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	gvr2 := schema.GroupVersionResource{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "datavolumes"}

	healer.RecordCRDCleanup(gvr1)
	healer.RecordCRDCleanup(gvr1)
	healer.RecordCRDCleanup(gvr2)

	healer.crdCleanupCountsMu.RLock()
	defer healer.crdCleanupCountsMu.RUnlock()
	if got := healer.CRDCleanupCounts["virtualmachines.kubevirt.io/v1"]; got != 2 {
		t.Errorf("CRDCleanupCounts[virtualmachines.kubevirt.io/v1] = %d, want 2", got)
	}
	if got := healer.CRDCleanupCounts["datavolumes.cdi.kubevirt.io/v1beta1"]; got != 1 {
		t.Errorf("CRDCleanupCounts[datavolumes.cdi.kubevirt.io/v1beta1] = %d, want 1", got)
	}
}

func TestHealer_RecordCRDCleanup_NilMap(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.CRDCleanupCounts = nil

	healer.RecordCRDCleanup(schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"})

	healer.crdCleanupCountsMu.RLock()
	defer healer.crdCleanupCountsMu.RUnlock()
	if healer.CRDCleanupCounts == nil {
		t.Fatal("RecordCRDCleanup should initialize CRDCleanupCounts when nil")
	}
	if got := healer.CRDCleanupCounts["virtualmachines.kubevirt.io/v1"]; got != 1 {
		t.Errorf("CRDCleanupCounts[virtualmachines.kubevirt.io/v1] = %d, want 1", got)
	}
}

func TestHealer_PrintCRDCleanupSummary(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	// Empty summary
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	os.Stdout = w
	healer.PrintCRDCleanupSummary()
	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	os.Stdout = old
	out := buf.String()
	if !strings.Contains(out, "0 resources cleaned") {
		t.Errorf("PrintCRDCleanupSummary() empty output should mention no cleanups; got %q", out)
	}

	// Non-empty summary
	healer.crdCleanupCountsMu.Lock()
	healer.CRDCleanupCounts["virtualmachines.kubevirt.io/v1"] = 3
	healer.CRDCleanupCounts["datavolumes.cdi.kubevirt.io/v1beta1"] = 2
	healer.crdCleanupCountsMu.Unlock()

	r2, w2, _ := os.Pipe()
	os.Stdout = w2
	healer.PrintCRDCleanupSummary()
	w2.Close()
	var buf2 bytes.Buffer
	io.Copy(&buf2, r2)
	os.Stdout = old
	out2 := buf2.String()
	if !strings.Contains(out2, "virtualmachines.kubevirt.io/v1: 3") {
		t.Errorf("PrintCRDCleanupSummary() should list virtualmachines count; got %q", out2)
	}
	if !strings.Contains(out2, "datavolumes.cdi.kubevirt.io/v1beta1: 2") {
		t.Errorf("PrintCRDCleanupSummary() should list datavolumes count; got %q", out2)
	}
	if !strings.Contains(out2, "Total: 5 resources cleaned") {
		t.Errorf("PrintCRDCleanupSummary() should show total 5; got %q", out2)
	}
}

func TestHealer_RunFullCRDCleanup_Disabled(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.EnableCRDCleanup = false

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	healer.RunFullCRDCleanup()
	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	os.Stdout = old

	if !strings.Contains(buf.String(), "CRD cleanup is disabled") {
		t.Errorf("RunFullCRDCleanup() when disabled should print message; got %q", buf.String())
	}
}

func TestHealer_RunFullCRDCleanup_NoResources(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.CRDResources = nil

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	healer.RunFullCRDCleanup()
	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	os.Stdout = old

	if !strings.Contains(buf.String(), "no CRD resources configured") {
		t.Errorf("RunFullCRDCleanup() with no resources should print message; got %q", buf.String())
	}
}

func TestHealer_RunFullCRDCleanup_Enabled(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	// Fake dynamic client must register list kind for virtualmachines or List() panics
	gvr := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	healer.DynamicClient = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "VirtualMachineList"})
	// RunFullCRDCleanup should not panic; it will call checkCRDResources which lists resources
	healer.RunFullCRDCleanup()
}
