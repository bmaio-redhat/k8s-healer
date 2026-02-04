package healer

import (
	"context"
	"fmt"
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
}
