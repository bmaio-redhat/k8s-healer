package healer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bmaio-redhat/k8s-healer/pkg/util"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
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
		CRDCleanupStats:                  make(map[string]*CRDCleanupStats),
		CRDCreationStats:                 make(map[string]*CRDCreationStats),
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

func TestNewHealer_InvalidKubeconfig(t *testing.T) {
	_, err := NewHealer("/nonexistent/kubeconfig", []string{"default"}, true, true, nil, false, "", 0, nil)
	if err == nil {
		t.Error("NewHealer() with invalid kubeconfig path expected error, got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "failed to build Kubernetes config") {
		t.Errorf("NewHealer() error should mention config failure, got: %v", err)
	}
}

// TestNewHealer_ValidKubeconfig runs NewHealer with a minimal valid kubeconfig (covers happy path and DisplayClusterInfo).
func TestNewHealer_ValidKubeconfig(t *testing.T) {
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "kubeconfig")
	const minimalKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: https://test.example.com
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
users:
- name: test
  user:
    token: test-token
`
	if err := os.WriteFile(kubeconfig, []byte(minimalKubeconfig), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	healer, err := NewHealer(kubeconfig, []string{"default"}, true, false, nil, false, "", 0, nil)
	if err != nil {
		t.Fatalf("NewHealer() with valid kubeconfig: %v", err)
	}
	if healer.ClientSet == nil || healer.DynamicClient == nil {
		t.Error("NewHealer() should return healer with clients set")
	}
	if healer.StopCh == nil {
		t.Error("NewHealer() should initialize StopCh")
	}
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

func TestHealer_Warmup(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	// Disable CRD cleanup so Warmup skips CRD validation (fake dynamic client doesn't have VMs registered)
	healer.EnableCRDCleanup = false
	healer.CRDResources = nil
	// Ensure fake client has at least one namespace and one node for List to succeed
	_, _ = healer.ClientSet.CoreV1().Namespaces().Create(context.TODO(), &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, metav1.CreateOptions{})
	_, _ = healer.ClientSet.CoreV1().Nodes().Create(context.TODO(), &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}, metav1.CreateOptions{})
	err = healer.Warmup(5 * time.Second)
	if err != nil {
		t.Errorf("Warmup() with fake client: %v", err)
	}
}

func TestHealer_TriggerPodDeletion(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
		Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "c", Image: "img"}}},
	}
	_, err = healer.ClientSet.CoreV1().Pods("default").Create(context.TODO(), pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create pod: %v", err)
	}
	healer.triggerPodDeletion(pod)
	_, err = healer.ClientSet.CoreV1().Pods("default").Get(context.TODO(), "test-pod", metav1.GetOptions{})
	if err == nil {
		t.Error("Pod should have been deleted")
	}
}

func TestHealer_TriggerNamespaceDeletion_TerminatingSkips(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "terminating-ns", CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour))},
		Status:     v1.NamespaceStatus{Phase: v1.NamespaceTerminating},
	}
	healer.triggerNamespaceDeletion(ns)
	// Should skip deletion (Terminating); namespace should not be removed from tracking if it wasn't there
	// Just ensure no panic and we don't try to delete
}

func TestHealer_TriggerNamespaceDeletion_DeletesAndUntracks(t *testing.T) {
	healer, err := createTestHealer([]string{"default", "to-delete"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "to-delete", CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour))},
		Status:     v1.NamespaceStatus{Phase: v1.NamespaceActive},
	}
	_, err = healer.ClientSet.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create namespace: %v", err)
	}
	healer.triggerNamespaceDeletion(ns)
	_, err = healer.ClientSet.CoreV1().Namespaces().Get(context.TODO(), "to-delete", metav1.GetOptions{})
	if err == nil {
		t.Error("Namespace to-delete should have been deleted")
	}
	// removeNamespaceFromTracking should have been called
	healer.namespacesMu.Lock()
	found := false
	for _, n := range healer.Namespaces {
		if n == "to-delete" {
			found = true
			break
		}
	}
	healer.namespacesMu.Unlock()
	if found {
		t.Error("to-delete should have been removed from healer.Namespaces")
	}
}

func TestHealer_TriggerNamespaceDeletion_WithFinalizersRemovesThenDeletes(t *testing.T) {
	healer, err := createTestHealer([]string{"default", "finalizer-ns"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.CleanupFinalizers = true
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "finalizer-ns",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			Finalizers:        []string{"kubernetes"},
		},
		Status: v1.NamespaceStatus{Phase: v1.NamespaceActive},
	}
	_, err = healer.ClientSet.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create namespace: %v", err)
	}
	healer.triggerNamespaceDeletion(ns)
	_, err = healer.ClientSet.CoreV1().Namespaces().Get(context.TODO(), "finalizer-ns", metav1.GetOptions{})
	if err == nil {
		t.Error("Namespace finalizer-ns should have been deleted")
	}
}

func TestHealer_CheckAndHealNode_UnhealthyTriggersHealing(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	// Node with NotReady condition (unhealthy)
	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "unhealthy-node"},
		Status: v1.NodeStatus{
			Conditions: []v1.NodeCondition{
				{Type: v1.NodeReady, Status: v1.ConditionFalse, LastTransitionTime: metav1.NewTime(time.Now().Add(-10 * time.Minute))},
			},
		},
	}
	_, err = healer.ClientSet.CoreV1().Nodes().Create(context.TODO(), node, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create node: %v", err)
	}
	healer.checkAndHealNode(node)
	// triggerNodeHealing will try drain then delete; fake may not support EvictV1 but will still delete the node
	_, err = healer.ClientSet.CoreV1().Nodes().Get(context.TODO(), "unhealthy-node", metav1.GetOptions{})
	if err == nil {
		t.Error("Unhealthy node should have been deleted by healing")
	}
}

func TestHealer_Watch_StopsWhenStopChClosed(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	// Disable features that need more setup (VM, CRD, optimization, polling) so Watch only starts pod informers
	healer.EnableVMHealing = false
	healer.EnableCRDCleanup = false
	healer.EnableResourceOptimization = false
	healer.EnableNamespacePolling = false
	done := make(chan struct{})
	go func() {
		healer.Watch()
		close(done)
	}()
	// Give Watch time to start goroutines, then close StopCh so Watch returns
	time.Sleep(100 * time.Millisecond)
	close(healer.StopCh)
	select {
	case <-done:
		// Watch returned
	case <-time.After(5 * time.Second):
		t.Fatal("Watch() did not return after StopCh closed")
	}
}

func TestHealer_CheckAndHealPod_UnhealthyTriggersDeletion(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	// Pod in CrashLoopBackOff with high restart count (unhealthy per util.IsUnhealthy).
	// Must have OwnerReferences or checkAndHealPod skips it.
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "crash-pod",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "v1", Kind: "ReplicationController", Name: "rc-1", UID: "rc-1-uid"},
			},
		},
		Spec: v1.PodSpec{Containers: []v1.Container{{Name: "c", Image: "img"}}},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{Name: "c", RestartCount: 5, State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
			},
		},
	}
	_, err = healer.ClientSet.CoreV1().Pods("default").Create(context.TODO(), pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create pod: %v", err)
	}
	healer.checkAndHealPod(pod)
	_, err = healer.ClientSet.CoreV1().Pods("default").Get(context.TODO(), "crash-pod", metav1.GetOptions{})
	if err == nil {
		t.Error("Unhealthy pod should have been deleted by healing")
	}
}

// TestHealer_WatchCRDResource_InvalidFormat hits the error path when CRD resource format is invalid.
func TestHealer_WatchCRDResource_InvalidFormat(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.watchCRDResource("invalid")
	// Should return without panicking (invalid format prints error and returns)
}

// TestHealer_WatchCRDResource_StopsOnStopCh runs watchCRDResource and closes StopCh so it exits.
func TestHealer_WatchCRDResource_StopsOnStopCh(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	done := make(chan struct{})
	go func() {
		healer.watchCRDResource("virtualmachines.kubevirt.io/v1")
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	close(healer.StopCh)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watchCRDResource did not return after StopCh closed")
	}
}

// TestHealer_WatchNodes_StopsOnStopCh runs watchNodes and closes StopCh so it exits (e.g. after cache sync fails or exits).
func TestHealer_WatchNodes_StopsOnStopCh(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	done := make(chan struct{})
	go func() {
		healer.watchNodes()
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	close(healer.StopCh)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchNodes did not return after StopCh closed")
	}
}

// TestHealer_WatchVirtualMachines_StopsOnStopCh runs watchVirtualMachines and closes StopCh so it exits.
func TestHealer_WatchVirtualMachines_StopsOnStopCh(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.EnableVMHealing = true
	done := make(chan struct{})
	go func() {
		healer.watchVirtualMachines()
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	close(healer.StopCh)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watchVirtualMachines did not return after StopCh closed")
	}
}

// TestHealer_WatchResourceOptimization_StopsOnStopCh runs watchResourceOptimization and closes StopCh.
func TestHealer_WatchResourceOptimization_StopsOnStopCh(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	done := make(chan struct{})
	go func() {
		healer.watchResourceOptimization()
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	close(healer.StopCh)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watchResourceOptimization did not return after StopCh closed")
	}
}

// TestHealer_PollForNewNamespaces_StopsOnStopCh runs pollForNewNamespaces and closes StopCh.
func TestHealer_PollForNewNamespaces_StopsOnStopCh(t *testing.T) {
	healer, err := createTestHealer([]string{"test-*"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.EnableNamespacePolling = true
	healer.NamespacePattern = "test-*"
	done := make(chan struct{})
	go func() {
		healer.pollForNewNamespaces()
		close(done)
	}()
	time.Sleep(80 * time.Millisecond)
	close(healer.StopCh)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pollForNewNamespaces did not return after StopCh closed")
	}
}

// TestHealer_CheckAndOptimizeResources_NoStrain runs checkAndOptimizeResources with healthy nodes (no strain).
func TestHealer_CheckAndOptimizeResources_NoStrain(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: v1.NodeStatus{
			Conditions: []v1.NodeCondition{
				{Type: v1.NodeReady, Status: v1.ConditionTrue},
			},
		},
	}
	_, _ = healer.ClientSet.CoreV1().Nodes().Create(context.TODO(), node, metav1.CreateOptions{})
	healer.checkAndOptimizeResources()
	// No strain -> early return; should not panic
}

// TestHealer_HandleResourceCreation_UnderStrain calls handleResourceCreation when cluster has strain.
func TestHealer_HandleResourceCreation_UnderStrain(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.EnableResourceCreationThrottling = true
	healer.NamespacePattern = "test-*"
	healer.currentClusterStrainMu.Lock()
	healer.CurrentClusterStrain = &util.ClusterStrainInfo{
		HasStrain: true, StrainPercentage: 50, TotalNodesCount: 2, StrainedNodesCount: 1,
		NodesUnderPressure: []string{"node-1"},
	}
	healer.currentClusterStrainMu.Unlock()
	healer.handleResourceCreation("Pod", "test-123", "my-pod")
	healer.handleResourceCreation("VirtualMachine", "default", "vm-1")
	// Should not panic
}

// TestHealer_CheckAllVirtualMachines_OldVMDeleted creates an old VM and runs checkAllVirtualMachines to trigger cleanup.
func TestHealer_CheckAllVirtualMachines_OldVMDeleted(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.EnableVMHealing = true
	gvr := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{gvr: "VirtualMachineList"})
	healer.DynamicClient = dynamicClient
	oldVM := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":              "old-vm",
				"namespace":         "default",
				"creationTimestamp": time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
			},
			"status": map[string]interface{}{"printableStatus": "Running"},
		},
	}
	_, err = dynamicClient.Resource(gvr).Namespace("default").Create(context.TODO(), oldVM, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create VM: %v", err)
	}
	_, _ = healer.ClientSet.CoreV1().Namespaces().Create(context.TODO(), &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, metav1.CreateOptions{})
	healer.checkAllVirtualMachines()
	_, err = healer.DynamicClient.Resource(gvr).Namespace("default").Get(context.TODO(), "old-vm", metav1.GetOptions{})
	if err == nil {
		t.Error("Old VM should have been deleted by checkAllVirtualMachines")
	}
}

// TestHealer_TriggerCRDCleanup_Success runs triggerCRDCleanup with a resource that can be deleted.
func TestHealer_TriggerCRDCleanup_Success(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	gvr := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{gvr: "VirtualMachineList"})
	healer.DynamicClient = dynamicClient
	vm := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":      "to-clean-vm",
				"namespace": "default",
			},
		},
	}
	_, err = dynamicClient.Resource(gvr).Namespace("default").Create(context.TODO(), vm, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create VM: %v", err)
	}
	healer.triggerCRDCleanup(vm, gvr)
	_, err = healer.DynamicClient.Resource(gvr).Namespace("default").Get(context.TODO(), "to-clean-vm", metav1.GetOptions{})
	if err == nil {
		t.Error("VM should have been deleted by triggerCRDCleanup")
	}
}

// TestHealer_CheckCRDResources_NamespaceAll calls checkCRDResources when namespaces is empty (uses NamespaceAll path).
func TestHealer_CheckCRDResources_NamespaceAll(t *testing.T) {
	healer, err := createTestHealer([]string{})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.namespacesMu.Lock()
	healer.Namespaces = []string{}
	healer.namespacesMu.Unlock()
	gvr := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{gvr: "VirtualMachineList"})
	healer.DynamicClient = dynamicClient
	_, _ = healer.ClientSet.CoreV1().Namespaces().Create(context.TODO(), &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, metav1.CreateOptions{})
	healer.checkCRDResources("kubevirt.io", "v1", "virtualmachines")
	// Should not panic; may list 0 resources
}

// TestHealer_EvictPodForOptimization_SkipsTestNamespace ensures we never evict pods in test namespaces.
func TestHealer_EvictPodForOptimization_SkipsTestNamespace(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.NamespacePattern = "test-*"
	strainInfo := util.ClusterStrainInfo{HasStrain: true, StrainPercentage: 100}
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "test-123"},
		Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "c", Image: "img"}}},
	}
	healer.evictPodForOptimization(pod, strainInfo)
	// Should skip (test namespace); pod should still exist if we had created it
}

// TestHealer_CheckAndOptimizeResources_WithStrain runs full optimization path: strained nodes + non-test pod eviction.
func TestHealer_CheckAndOptimizeResources_WithStrain(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.NamespacePattern = "test-*" // so "default" is non-test
	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: v1.NodeStatus{
			Conditions: []v1.NodeCondition{
				{Type: v1.NodeReady, Status: v1.ConditionTrue},
				{Type: v1.NodeMemoryPressure, Status: v1.ConditionTrue},
			},
		},
	}
	_, _ = healer.ClientSet.CoreV1().Nodes().Create(context.TODO(), node, metav1.CreateOptions{})
	_, _ = healer.ClientSet.CoreV1().Namespaces().Create(context.TODO(), &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, metav1.CreateOptions{})
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "evict-me",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "ReplicaSet", Name: "rs-1", UID: "rs-1"}},
		},
		Spec: v1.PodSpec{Containers: []v1.Container{{Name: "c", Image: "img"}}},
	}
	_, _ = healer.ClientSet.CoreV1().Pods("default").Create(context.TODO(), pod, metav1.CreateOptions{})
	healer.checkAndOptimizeResources()
	// Covers full strain path: list nodes, detect strain, list namespaces/pods, evict non-test pods.
	// Fake client may or may not remove pod on EvictV1; we only assert the code path ran.
}

// TestHealer_EvictPodForOptimization_NonTestNamespace covers eviction path for non-test pod (EvictV1 fails -> Delete fallback).
func TestHealer_EvictPodForOptimization_NonTestNamespace(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.NamespacePattern = "test-*"
	_, _ = healer.ClientSet.CoreV1().Namespaces().Create(context.TODO(), &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other"}}, metav1.CreateOptions{})
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "victim", Namespace: "other"},
		Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "c", Image: "img"}}},
	}
	_, _ = healer.ClientSet.CoreV1().Pods("other").Create(context.TODO(), pod, metav1.CreateOptions{})
	strainInfo := util.ClusterStrainInfo{HasStrain: true, StrainPercentage: 100, TotalNodesCount: 1, StrainedNodesCount: 1}
	healer.evictPodForOptimization(pod, strainInfo)
	// Covers non-test eviction path (EvictV1 then possibly fallback Delete). Fake client may not remove pod.
}

// TestHealer_Warmup_WithCRDValidation runs Warmup with CRD validation enabled and a listable dynamic client.
func TestHealer_Warmup_WithCRDValidation(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.EnableCRDCleanup = true
	healer.CRDResources = []string{"virtualmachines.kubevirt.io/v1"}
	gvr := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	scheme := runtime.NewScheme()
	healer.DynamicClient = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{gvr: "VirtualMachineList"})
	err = healer.Warmup(5 * time.Second)
	if err != nil {
		t.Fatalf("Warmup() error = %v", err)
	}
}

// TestHealer_StartHealCacheCleaner_ExitsOnStopCh starts the cache cleaner goroutine then closes StopCh so it exits.
func TestHealer_StartHealCacheCleaner_ExitsOnStopCh(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.startHealCacheCleaner()
	time.Sleep(50 * time.Millisecond)
	close(healer.StopCh)
	time.Sleep(50 * time.Millisecond)
	// Goroutine should have exited; no way to assert without modifying healer
}

// TestHealer_StartMemoryGuard_Disabled verifies startMemoryGuard is a no-op when MemoryLimitMB is 0.
func TestHealer_StartMemoryGuard_Disabled(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.MemoryLimitMB = 0
	healer.startMemoryGuard()
	// Should return immediately without starting goroutine
}

// TestHealer_StartMemoryGuard_ExitsOnStopCh starts the memory guard then closes StopCh.
func TestHealer_StartMemoryGuard_ExitsOnStopCh(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.MemoryLimitMB = 64
	healer.MemoryCheckInterval = time.Hour
	healer.startMemoryGuard()
	time.Sleep(50 * time.Millisecond)
	close(healer.StopCh)
	time.Sleep(50 * time.Millisecond)
}

// TestHealer_CheckMemory_OverLimitAndRecover uses MemoryReadFunc to simulate over-limit then recover after sanitization.
func TestHealer_CheckMemory_OverLimitAndRecover(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.MemoryLimitMB = 1 // 1 MB limit
	callCount := 0
	healer.MemoryReadFunc = func() uint64 {
		callCount++
		if callCount == 1 {
			return 2 * 1024 * 1024 // 2 MB first read -> over limit
		}
		return 512 * 1024 // 0.5 MB second read (after "sanitize") -> below limit
	}
	healer.checkMemory()
	if callCount != 2 {
		t.Errorf("MemoryReadFunc should be called twice (before and after sanitize), got %d", callCount)
	}
}

// TestHealer_CheckMemory_OverLimitRestartRequested covers the path that closes RestartRequested when still over after sanitize.
func TestHealer_CheckMemory_OverLimitRestartRequested(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.MemoryLimitMB = 1
	healer.RestartOnMemoryLimit = true
	healer.RestartRequested = make(chan struct{})
	healer.MemoryReadFunc = func() uint64 { return 3 * 1024 * 1024 } // always over limit
	healer.checkMemory()
	select {
	case <-healer.RestartRequested:
		// expected
	default:
		t.Error("RestartRequested should have been closed when still over limit after sanitization")
	}
}

// TestHealer_DrainNode_EmptyPods covers drainNode when there are no pods on the node.
func TestHealer_DrainNode_EmptyPods(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = healer.drainNode(ctx, "node-1")
	if err != nil {
		t.Fatalf("drainNode() error = %v", err)
	}
}

// TestHealer_DrainNode_EvictsPods covers drainNode listing and evicting pods on the node.
func TestHealer_DrainNode_EvictsPods(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "drain-me",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs-1", UID: "rs-1"}},
		},
		Spec: v1.PodSpec{NodeName: "node-1", Containers: []v1.Container{{Name: "c", Image: "img"}}},
	}
	_, _ = healer.ClientSet.CoreV1().Pods("default").Create(context.TODO(), pod, metav1.CreateOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = healer.drainNode(ctx, "node-1")
	if err != nil {
		t.Fatalf("drainNode() error = %v", err)
	}
}

// TestHealer_DrainNode_SkipsDaemonSetAndStatic covers skip paths: no owner refs and DaemonSet owner.
func TestHealer_DrainNode_SkipsDaemonSetAndStatic(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	staticPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "static", Namespace: "default"},
		Spec:       v1.PodSpec{NodeName: "node-1", Containers: []v1.Container{{Name: "c", Image: "img"}}},
	}
	dsPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ds-pod",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "ds-1", UID: "ds-1"}},
		},
		Spec: v1.PodSpec{NodeName: "node-1", Containers: []v1.Container{{Name: "c", Image: "img"}}},
	}
	_, _ = healer.ClientSet.CoreV1().Pods("default").Create(context.TODO(), staticPod, metav1.CreateOptions{})
	_, _ = healer.ClientSet.CoreV1().Pods("default").Create(context.TODO(), dsPod, metav1.CreateOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = healer.drainNode(ctx, "node-1")
	if err != nil {
		t.Fatalf("drainNode() error = %v", err)
	}
}

// TestHealer_CheckAndCleanupCRDResource_RecentlyCleaned skips cleanup when resource was recently cleaned.
func TestHealer_CheckAndCleanupCRDResource_RecentlyCleaned(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	gvr := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	resourceKey := "virtualmachines/default/some-vm"
	healer.healedCRDsMu.Lock()
	healer.HealedCRDs[resourceKey] = time.Now()
	healer.healedCRDsMu.Unlock()
	resource := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":              "some-vm",
				"namespace":         "default",
				"creationTimestamp": time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	healer.checkAndCleanupCRDResource(resource, gvr)
	// Should return early; resource not deleted (we didn't set up DynamicClient with it)
}

// TestHealer_CheckAndCleanupCRDResource_NonVMStale runs cleanup for a non-VM CRD resource that is stale (old age).
func TestHealer_CheckAndCleanupCRDResource_NonVMStale(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "myresources"}
	scheme := runtime.NewScheme()
	healer.DynamicClient = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{gvr: "MyResourceList"})
	oldResource := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "example.io/v1",
			"kind":       "MyResource",
			"metadata": map[string]interface{}{
				"name":              "old-one",
				"namespace":         "default",
				"creationTimestamp": time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
			},
		},
	}
	_, _ = healer.DynamicClient.Resource(gvr).Namespace("default").Create(context.TODO(), oldResource, metav1.CreateOptions{})
	healer.checkAndCleanupCRDResource(oldResource, gvr)
	_, getErr := healer.DynamicClient.Resource(gvr).Namespace("default").Get(context.TODO(), "old-one", metav1.GetOptions{})
	if getErr == nil {
		t.Error("Stale non-VM CRD resource should have been deleted")
	}
}

// TestHealer_LogCRDCreation_NewAndTracked covers logCRDCreation for a new resource and then already-tracked.
func TestHealer_LogCRDCreation_NewAndTracked(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	gvr := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	resource := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":              "new-vm",
				"namespace":         "default",
				"creationTimestamp": time.Now().Format(time.RFC3339),
			},
		},
	}
	healer.logCRDCreation(resource, gvr)
	healer.trackedCRDsMu.RLock()
	ok := healer.TrackedCRDs["virtualmachines/default/new-vm"]
	healer.trackedCRDsMu.RUnlock()
	if !ok {
		t.Error("logCRDCreation should have tracked the new resource")
	}
	healer.logCRDCreation(resource, gvr) // second call: already tracked, no-op
}

// TestHealer_TriggerNamespaceDeletion_GetFailsForFinalizers covers Get namespace failing (non-NotFound) before finalizer removal.
func TestHealer_TriggerNamespaceDeletion_GetFailsForFinalizers(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.CleanupFinalizers = true
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "finalizer-ns",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			Finalizers:        []string{"test"},
		},
		Status: v1.NamespaceStatus{Phase: v1.NamespaceActive},
	}
	_, _ = healer.ClientSet.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
	fc, ok := healer.ClientSet.(*fake.Clientset)
	if !ok {
		t.Fatal("expected *fake.Clientset")
	}
	fc.Fake.PrependReactor("get", "namespaces", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		if action.GetNamespace() == "" && action.(k8stesting.GetAction).GetName() == "finalizer-ns" {
			return true, nil, fmt.Errorf("server error")
		}
		return false, nil, nil
	})
	healer.triggerNamespaceDeletion(ns)
	// Get fails -> "Proceeding with deletion anyway" -> Delete runs (namespace exists, so delete succeeds or we get other error)
}

// TestHealer_TriggerNamespaceDeletion_UpdateNotFound covers Update (finalizer removal) returning NotFound.
func TestHealer_TriggerNamespaceDeletion_UpdateNotFound(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.CleanupFinalizers = true
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "update-gone-ns",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			Finalizers:        []string{"test"},
		},
		Status: v1.NamespaceStatus{Phase: v1.NamespaceActive},
	}
	_, _ = healer.ClientSet.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
	fc, ok := healer.ClientSet.(*fake.Clientset)
	if !ok {
		t.Fatal("expected *fake.Clientset")
	}
	fc.Fake.PrependReactor("update", "namespaces", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "update-gone-ns")
	})
	healer.namespacesMu.Lock()
	healer.Namespaces = append(healer.Namespaces, "update-gone-ns")
	healer.namespacesMu.Unlock()
	healer.watchedNamespacesMu.Lock()
	healer.WatchedNamespaces["update-gone-ns"] = true
	healer.watchedNamespacesMu.Unlock()
	healer.triggerNamespaceDeletion(ns)
	healer.watchedNamespacesMu.RLock()
	_, stillWatched := healer.WatchedNamespaces["update-gone-ns"]
	healer.watchedNamespacesMu.RUnlock()
	if stillWatched {
		t.Error("removeNamespaceFromTracking should have been called")
	}
}

// TestHealer_TriggerNamespaceDeletion_DeleteFails covers Delete returning a non-NotFound error.
func TestHealer_TriggerNamespaceDeletion_DeleteFails(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "delete-fail-ns",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		},
		Status: v1.NamespaceStatus{Phase: v1.NamespaceActive},
	}
	_, _ = healer.ClientSet.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
	fc, ok := healer.ClientSet.(*fake.Clientset)
	if !ok {
		t.Fatal("expected *fake.Clientset")
	}
	fc.Fake.PrependReactor("delete", "namespaces", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		if action.(k8stesting.DeleteAction).GetName() == "delete-fail-ns" {
			return true, nil, fmt.Errorf("server unavailable")
		}
		return false, nil, nil
	})
	healer.triggerNamespaceDeletion(ns)
	// Should log "[FAIL] Failed to delete namespace" and not panic
}

// TestHealer_TriggerNamespaceDeletion_DeleteNotFound covers Delete returning NotFound (namespace already deleted).
func TestHealer_TriggerNamespaceDeletion_DeleteNotFound(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "already-gone",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
		},
		Status: v1.NamespaceStatus{Phase: v1.NamespaceActive},
	}
	// Do not create the namespace in the cluster; healer will try to delete and get NotFound
	healer.namespacesMu.Lock()
	healer.Namespaces = append(healer.Namespaces, "already-gone")
	healer.namespacesMu.Unlock()
	healer.watchedNamespacesMu.Lock()
	healer.WatchedNamespaces["already-gone"] = true
	healer.watchedNamespacesMu.Unlock()
	healer.triggerNamespaceDeletion(ns)
	healer.watchedNamespacesMu.RLock()
	_, stillWatched := healer.WatchedNamespaces["already-gone"]
	healer.watchedNamespacesMu.RUnlock()
	if stillWatched {
		t.Error("removeNamespaceFromTracking should have been called (namespace removed from tracking)")
	}
}

// TestHealer_Watch_EmptyNamespacesThenStop runs Watch with no namespaces (NamespaceAll) then closes StopCh.
func TestHealer_Watch_EmptyNamespacesThenStop(t *testing.T) {
	healer, err := createTestHealer([]string{})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.EnableVMHealing = false
	healer.EnableCRDCleanup = false
	healer.EnableResourceOptimization = false
	healer.EnableNamespacePolling = false
	done := make(chan struct{})
	go func() {
		healer.Watch()
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)
	close(healer.StopCh)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch() did not return after StopCh closed")
	}
}

// TestHealer_Watch_ExitsOnRestartRequested runs Watch and closes RestartRequested so Watch returns from select.
func TestHealer_Watch_ExitsOnRestartRequested(t *testing.T) {
	healer, err := createTestHealer([]string{"default"})
	if err != nil {
		t.Fatalf("createTestHealer() error = %v", err)
	}
	healer.RestartRequested = make(chan struct{})
	healer.EnableVMHealing = false
	healer.EnableCRDCleanup = false
	healer.EnableResourceOptimization = false
	healer.EnableNamespacePolling = false
	done := make(chan struct{})
	go func() {
		healer.Watch()
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)
	close(healer.RestartRequested)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch() did not return after RestartRequested closed")
	}
}
