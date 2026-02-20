package healer

import (
	"bytes"
	"context"
	"encoding/json"
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
				"name":       "nonexistent-vm",
				"namespace":  "default",
				"finalizers": []interface{}{"test"},
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
	st := healer.CRDCleanupStats["virtualmachines.kubevirt.io/v1"]
	healer.crdCleanupCountsMu.RUnlock()
	if st == nil || st.Count != 1 {
		count := 0
		if st != nil {
			count = st.Count
		}
		t.Errorf("CRDCleanupStats[virtualmachines.kubevirt.io/v1].Count = %d, want 1", count)
	}
}

func TestHealer_DoCRDCleanup_WithFinalizers(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	scheme := runtime.NewScheme()
	gvr := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{gvr: "VirtualMachineList"})
	healer.DynamicClient = dynamicClient
	healer.CleanupFinalizers = true

	vm := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":       "vm-with-finalizers",
				"namespace":  "default",
				"finalizers": []interface{}{"test-finalizer"},
			},
		},
	}
	_, err = dynamicClient.Resource(gvr).Namespace("default").Create(context.TODO(), vm, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create VM: %v", err)
	}

	resourceKey := "virtualmachines/default/vm-with-finalizers"
	healer.trackedCRDsMu.Lock()
	healer.TrackedCRDs[resourceKey] = true
	healer.trackedCRDsMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done, err := healer.doCRDCleanup(ctx, vm, gvr, "vm-with-finalizers", "default", resourceKey)
	if err != nil {
		t.Errorf("doCRDCleanup() err = %v", err)
	}
	if !done {
		t.Error("doCRDCleanup() done = false, want true")
	}
	_, getErr := dynamicClient.Resource(gvr).Namespace("default").Get(ctx, "vm-with-finalizers", metav1.GetOptions{})
	if !apierrors.IsNotFound(getErr) {
		t.Errorf("Resource should be deleted after finalizer removal; Get err = %v", getErr)
	}
}

func TestHealer_RecordCRDCleanup(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	gvr1 := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	gvr2 := schema.GroupVersionResource{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "datavolumes"}

	healer.RecordCRDCleanup(nil, gvr1)
	healer.RecordCRDCleanup(nil, gvr1)
	healer.RecordCRDCleanup(nil, gvr2)

	healer.crdCleanupCountsMu.RLock()
	defer healer.crdCleanupCountsMu.RUnlock()
	if got := healer.CRDCleanupStats["virtualmachines.kubevirt.io/v1"]; got == nil || got.Count != 2 {
		count := 0
		if got != nil {
			count = got.Count
		}
		t.Errorf("CRDCleanupStats[virtualmachines.kubevirt.io/v1].Count = %d, want 2", count)
	}
	if got := healer.CRDCleanupStats["datavolumes.cdi.kubevirt.io/v1beta1"]; got == nil || got.Count != 1 {
		count := 0
		if got != nil {
			count = got.Count
		}
		t.Errorf("CRDCleanupStats[datavolumes.cdi.kubevirt.io/v1beta1].Count = %d, want 1", count)
	}
}

func TestHealer_RecordCRDCleanup_NilMap(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.CRDCleanupStats = nil

	healer.RecordCRDCleanup(nil, schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"})

	healer.crdCleanupCountsMu.RLock()
	defer healer.crdCleanupCountsMu.RUnlock()
	if healer.CRDCleanupStats == nil {
		t.Fatal("RecordCRDCleanup should initialize CRDCleanupStats when nil")
	}
	if got := healer.CRDCleanupStats["virtualmachines.kubevirt.io/v1"]; got == nil || got.Count != 1 {
		count := 0
		if got != nil {
			count = got.Count
		}
		t.Errorf("CRDCleanupStats[virtualmachines.kubevirt.io/v1].Count = %d, want 1", count)
	}
}

func TestHealer_RecordCRDCreation(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	gvr1 := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	gvr2 := schema.GroupVersionResource{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "datavolumes"}
	res := &unstructured.Unstructured{Object: map[string]interface{}{"metadata": map[string]interface{}{"name": "vm1", "namespace": "default"}}}

	healer.RecordCRDCreation(res, gvr1)
	healer.RecordCRDCreation(res, gvr1)
	healer.RecordCRDCreation(res, gvr2)

	healer.crdCreationStatsMu.RLock()
	defer healer.crdCreationStatsMu.RUnlock()
	if got := healer.CRDCreationStats["virtualmachines.kubevirt.io/v1"]; got == nil || got.Count != 2 {
		count := 0
		if got != nil {
			count = got.Count
		}
		t.Errorf("CRDCreationStats[virtualmachines.kubevirt.io/v1].Count = %d, want 2", count)
	}
	if got := healer.CRDCreationStats["datavolumes.cdi.kubevirt.io/v1beta1"]; got == nil || got.Count != 1 {
		count := 0
		if got != nil {
			count = got.Count
		}
		t.Errorf("CRDCreationStats[datavolumes.cdi.kubevirt.io/v1beta1].Count = %d, want 1", count)
	}
}

func TestHealer_PrintCRDCleanupSummary(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	// Empty summary (creation-based)
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
	if !strings.Contains(out, "0 resources created") {
		t.Errorf("PrintCRDCleanupSummary() empty output should mention no creations; got %q", out)
	}
	if !strings.Contains(out, "CRD resource summary (created)") {
		t.Errorf("PrintCRDCleanupSummary() should output creation summary title; got %q", out)
	}

	// Non-empty summary (creation stats, ASCII table)
	healer.crdCreationStatsMu.Lock()
	healer.CRDCreationStats["virtualmachines.kubevirt.io/v1"] = &CRDCreationStats{Count: 3}
	healer.CRDCreationStats["datavolumes.cdi.kubevirt.io/v1beta1"] = &CRDCreationStats{Count: 2}
	healer.crdCreationStatsMu.Unlock()

	r2, w2, _ := os.Pipe()
	os.Stdout = w2
	healer.PrintCRDCleanupSummary()
	w2.Close()
	var buf2 bytes.Buffer
	io.Copy(&buf2, r2)
	os.Stdout = old
	out2 := buf2.String()
	if !strings.Contains(out2, "CRD resource summary (created)") {
		t.Errorf("PrintCRDCleanupSummary() should output creation summary title; got %q", out2)
	}
	if !strings.Contains(out2, "virtualmachines.kubevirt.io/v1") || !strings.Contains(out2, "3") {
		t.Errorf("PrintCRDCleanupSummary() should list virtualmachines count in table; got %q", out2)
	}
	if !strings.Contains(out2, "datavolumes.cdi.kubevirt.io/v1beta1") || !strings.Contains(out2, "2") {
		t.Errorf("PrintCRDCleanupSummary() should list datavolumes count in table; got %q", out2)
	}
	if !strings.Contains(out2, "Total") || !strings.Contains(out2, "5") {
		t.Errorf("PrintCRDCleanupSummary() should show total 5 in table; got %q", out2)
	}
	if !strings.Contains(out2, "+-") {
		t.Errorf("PrintCRDCleanupSummary() should output ASCII table; got %q", out2)
	}
}

func TestHealer_PrintCRDCleanupSummaryTable(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	var buf bytes.Buffer
	healer.PrintCRDCleanupSummaryTable(&buf)
	out := buf.String()
	if !strings.Contains(out, "CRD resource summary (created)") {
		t.Errorf("PrintCRDCleanupSummaryTable() should include title; got %q", out)
	}
	if !strings.Contains(out, "0 resources created") {
		t.Errorf("PrintCRDCleanupSummaryTable() empty should mention 0 creations; got %q", out)
	}

	healer.crdCreationStatsMu.Lock()
	healer.CRDCreationStats["virtualmachines.kubevirt.io/v1"] = &CRDCreationStats{Count: 2}
	healer.crdCreationStatsMu.Unlock()
	buf.Reset()
	healer.PrintCRDCleanupSummaryTable(&buf)
	out = buf.String()
	if !strings.Contains(out, "+-") || !strings.Contains(out, "Resource type") {
		t.Errorf("PrintCRDCleanupSummaryTable() should output ASCII table; got %q", out)
	}
	if !strings.Contains(out, "Total") || !strings.Contains(out, "2") {
		t.Errorf("PrintCRDCleanupSummaryTable() should show total 2; got %q", out)
	}
}

func TestHealer_PrintCRDCleanupSummaryToJSON(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}

	// Empty summary
	var buf bytes.Buffer
	if err := healer.PrintCRDCleanupSummaryToJSON(&buf); err != nil {
		t.Fatalf("PrintCRDCleanupSummaryToJSON() error = %v", err)
	}
	var out CRDSummaryJSON
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("JSON unmarshal: %v", err)
	}
	if out.Title != "CRD resource summary (created)" {
		t.Errorf("Title = %q, want CRD resource summary (created)", out.Title)
	}
	if out.Total.Count != 0 {
		t.Errorf("Total.Count = %d, want 0", out.Total.Count)
	}
	if len(out.Summary) != 0 {
		t.Errorf("len(Summary) = %d, want 0", len(out.Summary))
	}

	// Non-empty summary (same data as table test)
	healer.crdCreationStatsMu.Lock()
	healer.CRDCreationStats["virtualmachines.kubevirt.io/v1"] = &CRDCreationStats{Count: 3}
	healer.CRDCreationStats["datavolumes.cdi.kubevirt.io/v1beta1"] = &CRDCreationStats{Count: 2}
	healer.crdCreationStatsMu.Unlock()

	buf.Reset()
	if err := healer.PrintCRDCleanupSummaryToJSON(&buf); err != nil {
		t.Fatalf("PrintCRDCleanupSummaryToJSON() error = %v", err)
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("JSON unmarshal: %v", err)
	}
	if out.Total.Count != 5 {
		t.Errorf("Total.Count = %d, want 5", out.Total.Count)
	}
	if len(out.Summary) != 2 {
		t.Errorf("len(Summary) = %d, want 2", len(out.Summary))
	}
	var seenVM, seenDV bool
	for _, row := range out.Summary {
		if row.ResourceType == "virtualmachines.kubevirt.io/v1" && row.Count == 3 {
			seenVM = true
		}
		if row.ResourceType == "datavolumes.cdi.kubevirt.io/v1beta1" && row.Count == 2 {
			seenDV = true
		}
	}
	if !seenVM || !seenDV {
		t.Errorf("Summary missing expected rows; got %+v", out.Summary)
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
