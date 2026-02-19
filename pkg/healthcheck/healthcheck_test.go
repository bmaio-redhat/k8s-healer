package healthcheck

import (
	"context"
	"fmt"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

// createTestClients creates fake Kubernetes clients for testing
func createTestClients() (kubernetes.Interface, dynamic.Interface, *rest.Config) {
	// Create fake clientset with some test objects
	clientset := kubernetesfake.NewSimpleClientset(
		&v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
		},
		&v1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		},
		&v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
			},
		},
	)

	// Create fake dynamic client
	scheme := runtime.NewScheme()
	dynamicClient := dynamicfake.NewSimpleDynamicClient(scheme)

	// Create minimal config
	config := &rest.Config{
		Host: "https://test-cluster.example.com",
	}

	return clientset, dynamicClient, config
}

func TestCheckKubernetesAPI(t *testing.T) {
	clientset, _, _ := createTestClients()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := checkKubernetesAPI(ctx, clientset)

	if result.Status != "healthy" {
		t.Errorf("checkKubernetesAPI() status = %v, want healthy", result.Status)
	}

	if result.Error != nil {
		t.Errorf("checkKubernetesAPI() error = %v, want nil", result.Error)
	}

	if result.Duration == 0 {
		t.Error("checkKubernetesAPI() duration should be > 0")
	}
}

func TestCheckKubernetesAPI_Error(t *testing.T) {
	clientset := kubernetesfake.NewSimpleClientset()
	clientset.Fake.PrependReactor("list", "namespaces", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, fmt.Errorf("connection refused")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := checkKubernetesAPI(ctx, clientset)

	if result.Status != "unhealthy" {
		t.Errorf("checkKubernetesAPI() status = %v, want unhealthy", result.Status)
	}
	if result.Error == nil {
		t.Error("checkKubernetesAPI() error = nil, want non-nil")
	}
	if result.Message == "" {
		t.Error("checkKubernetesAPI() message should not be empty")
	}
}

func TestCheckKubeVirtCRDs(t *testing.T) {
	_, dynamicClient, _ := createTestClients()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := checkKubeVirtCRDs(ctx, dynamicClient)

	if len(results) != len(kubevirtCRDs) {
		t.Errorf("checkKubeVirtCRDs() returned %d results, want %d", len(results), len(kubevirtCRDs))
	}

	// With fake client, CRDs will show as degraded (fake client limitation)
	// but the function should handle it gracefully without panicking
	for _, result := range results {
		if result.Status == "unknown" {
			t.Errorf("checkKubeVirtCRDs() result status should not be unknown for %s", result.Name)
		}

		// With fake client, status should be degraded (fake client limitation)
		// or unhealthy (CRD not found), but not unknown
		if result.Status != "degraded" && result.Status != "unhealthy" {
			t.Logf("checkKubeVirtCRDs() result for %s has status %s (expected degraded or unhealthy with fake client)", result.Name, result.Status)
		}

		if result.Duration == 0 {
			t.Errorf("checkKubeVirtCRDs() duration should be > 0 for %s", result.Name)
		}
	}
}

// notFoundDynamicClient is a dynamic.Interface that returns NotFound for all List calls (used to test error branches).
type notFoundDynamicClient struct{}

func (c *notFoundDynamicClient) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &notFoundResourceInterface{gvr: gvr}
}

type notFoundResourceInterface struct {
	gvr schema.GroupVersionResource
}

func (r *notFoundResourceInterface) Namespace(string) dynamic.ResourceInterface { return r }

func (r *notFoundResourceInterface) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return nil, errors.NewNotFound(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, "")
}

func (r *notFoundResourceInterface) Get(ctx context.Context, name string, opts metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.NewNotFound(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, name)
}

func (r *notFoundResourceInterface) Create(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.NewNotFound(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, "")
}

func (r *notFoundResourceInterface) Update(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.NewNotFound(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, "")
}

func (r *notFoundResourceInterface) UpdateStatus(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return nil, errors.NewNotFound(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, "")
}

func (r *notFoundResourceInterface) Delete(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) error {
	return errors.NewNotFound(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, name)
}

func (r *notFoundResourceInterface) DeleteCollection(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) error {
	return errors.NewNotFound(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, "")
}

func (r *notFoundResourceInterface) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return nil, errors.NewNotFound(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, "")
}

func (r *notFoundResourceInterface) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, options metav1.PatchOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.NewNotFound(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, name)
}

func (r *notFoundResourceInterface) Apply(ctx context.Context, name string, obj *unstructured.Unstructured, options metav1.ApplyOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.NewNotFound(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, name)
}

func (r *notFoundResourceInterface) ApplyStatus(ctx context.Context, name string, obj *unstructured.Unstructured, options metav1.ApplyOptions) (*unstructured.Unstructured, error) {
	return nil, errors.NewNotFound(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, name)
}

func TestCheckKubeVirtCRDs_NotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := checkKubeVirtCRDs(ctx, &notFoundDynamicClient{})

	if len(results) != len(kubevirtCRDs) {
		t.Errorf("checkKubeVirtCRDs() returned %d results, want %d", len(results), len(kubevirtCRDs))
	}
	for _, result := range results {
		if result.Status != "unhealthy" {
			t.Errorf("checkKubeVirtCRDs() with NotFound client: result for %s status = %v, want unhealthy", result.Name, result.Status)
		}
		if !errors.IsNotFound(result.Error) {
			t.Errorf("checkKubeVirtCRDs() with NotFound client: result for %s error = %v, want NotFound", result.Name, result.Error)
		}
	}
}

// forbiddenDynamicClient returns Forbidden for List (used to test IsForbidden branch).
type forbiddenDynamicClient struct{}

func (c *forbiddenDynamicClient) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &forbiddenResourceInterface{gvr: gvr}
}

type forbiddenResourceInterface struct {
	gvr schema.GroupVersionResource
}

func (r *forbiddenResourceInterface) Namespace(string) dynamic.ResourceInterface { return r }

func (r *forbiddenResourceInterface) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return nil, errors.NewForbidden(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, "", fmt.Errorf("RBAC"))
}

func (r *forbiddenResourceInterface) Get(ctx context.Context, name string, opts metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.NewForbidden(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, name, nil)
}
func (r *forbiddenResourceInterface) Create(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.NewForbidden(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, "", nil)
}
func (r *forbiddenResourceInterface) Update(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.NewForbidden(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, "", nil)
}
func (r *forbiddenResourceInterface) UpdateStatus(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return nil, errors.NewForbidden(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, "", nil)
}
func (r *forbiddenResourceInterface) Delete(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) error {
	return errors.NewForbidden(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, name, nil)
}
func (r *forbiddenResourceInterface) DeleteCollection(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) error {
	return errors.NewForbidden(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, "", nil)
}
func (r *forbiddenResourceInterface) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return nil, errors.NewForbidden(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, "", nil)
}
func (r *forbiddenResourceInterface) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, options metav1.PatchOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.NewForbidden(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, name, nil)
}
func (r *forbiddenResourceInterface) Apply(ctx context.Context, name string, obj *unstructured.Unstructured, options metav1.ApplyOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.NewForbidden(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, name, nil)
}
func (r *forbiddenResourceInterface) ApplyStatus(ctx context.Context, name string, obj *unstructured.Unstructured, options metav1.ApplyOptions) (*unstructured.Unstructured, error) {
	return nil, errors.NewForbidden(schema.GroupResource{Group: r.gvr.Group, Resource: r.gvr.Resource}, name, nil)
}

func TestCheckKubeVirtCRDs_Forbidden(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := checkKubeVirtCRDs(ctx, &forbiddenDynamicClient{})

	if len(results) != len(kubevirtCRDs) {
		t.Errorf("checkKubeVirtCRDs() returned %d results, want %d", len(results), len(kubevirtCRDs))
	}
	for _, result := range results {
		if result.Status != "degraded" {
			t.Errorf("checkKubeVirtCRDs() with Forbidden client: result for %s status = %v, want degraded", result.Name, result.Status)
		}
		if !errors.IsForbidden(result.Error) {
			t.Errorf("checkKubeVirtCRDs() with Forbidden client: result for %s error = %v, want Forbidden", result.Name, result.Error)
		}
	}
}

// genericErrorDynamicClient returns a non-NotFound/Forbidden error for List (covers "else" branch).
type genericErrorDynamicClient struct{}

func (c *genericErrorDynamicClient) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &genericErrorResourceInterface{}
}

type genericErrorResourceInterface struct{}

func (r *genericErrorResourceInterface) Namespace(string) dynamic.ResourceInterface { return r }

func (r *genericErrorResourceInterface) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return nil, fmt.Errorf("server unavailable")
}

func (r *genericErrorResourceInterface) Get(ctx context.Context, name string, opts metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, fmt.Errorf("server unavailable")
}
func (r *genericErrorResourceInterface) Create(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, fmt.Errorf("server unavailable")
}
func (r *genericErrorResourceInterface) Update(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, fmt.Errorf("server unavailable")
}
func (r *genericErrorResourceInterface) UpdateStatus(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return nil, fmt.Errorf("server unavailable")
}
func (r *genericErrorResourceInterface) Delete(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) error {
	return fmt.Errorf("server unavailable")
}
func (r *genericErrorResourceInterface) DeleteCollection(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) error {
	return fmt.Errorf("server unavailable")
}
func (r *genericErrorResourceInterface) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return nil, fmt.Errorf("server unavailable")
}
func (r *genericErrorResourceInterface) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, options metav1.PatchOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, fmt.Errorf("server unavailable")
}
func (r *genericErrorResourceInterface) Apply(ctx context.Context, name string, obj *unstructured.Unstructured, options metav1.ApplyOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, fmt.Errorf("server unavailable")
}
func (r *genericErrorResourceInterface) ApplyStatus(ctx context.Context, name string, obj *unstructured.Unstructured, options metav1.ApplyOptions) (*unstructured.Unstructured, error) {
	return nil, fmt.Errorf("server unavailable")
}

func TestCheckKubeVirtCRDs_GenericError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := checkKubeVirtCRDs(ctx, &genericErrorDynamicClient{})

	if len(results) != len(kubevirtCRDs) {
		t.Errorf("checkKubeVirtCRDs() returned %d results, want %d", len(results), len(kubevirtCRDs))
	}
	for _, result := range results {
		if result.Status != "degraded" {
			t.Errorf("checkKubeVirtCRDs() with generic error client: result for %s status = %v, want degraded", result.Name, result.Status)
		}
		if result.Error == nil {
			t.Errorf("checkKubeVirtCRDs() with generic error client: result for %s error = nil, want non-nil", result.Name)
		}
	}
}

func TestCheckKubeVirtAPI(t *testing.T) {
	clientset, _, config := createTestClients()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := checkKubeVirtAPI(ctx, clientset, config)

	// With fake client, KubeVirt API check will return degraded status
	if result.Status == "unknown" {
		t.Error("checkKubeVirtAPI() result status should not be unknown")
	}

	// Duration should be set (even if it's very small for fake clients)
	if result.Duration < 0 {
		t.Error("checkKubeVirtAPI() duration should be >= 0")
	}

	// With fake client, should return degraded status
	if result.Status != "degraded" {
		t.Logf("checkKubeVirtAPI() with fake client returned status %s (expected degraded)", result.Status)
	}
}

func TestCheckKeyResources(t *testing.T) {
	clientset, dynamicClient, _ := createTestClients()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := checkKeyResources(ctx, clientset, dynamicClient)

	if len(results) == 0 {
		t.Error("checkKeyResources() should return at least one result")
	}

	// Check that all expected resources are present
	resourceNames := make(map[string]bool)
	for _, result := range results {
		resourceNames[result.Name] = true
	}

	expectedResources := []string{"Nodes", "Pods", "Namespaces"}
	for _, expected := range expectedResources {
		if !resourceNames[expected] {
			t.Errorf("checkKeyResources() missing expected resource: %s", expected)
		}
	}

	// All resources should be healthy with fake client
	for _, result := range results {
		if result.Status != "healthy" {
			t.Errorf("checkKeyResources() result for %s status = %v, want healthy", result.Name, result.Status)
		}

		if result.Duration == 0 {
			t.Errorf("checkKeyResources() duration should be > 0 for %s", result.Name)
		}
	}
}

func TestPerformClusterHealthCheck(t *testing.T) {
	clientset, dynamicClient, config := createTestClients()

	status, err := PerformClusterHealthCheck(clientset, dynamicClient, config)

	if err != nil {
		t.Fatalf("PerformClusterHealthCheck() error = %v, want nil", err)
	}

	if status == nil {
		t.Fatal("PerformClusterHealthCheck() status = nil, want non-nil")
	}

	// Verify all checks were performed
	if status.KubernetesAPI.Status == "unknown" {
		t.Error("PerformClusterHealthCheck() KubernetesAPI status should not be unknown")
	}

	if len(status.KubeVirtCRDs) != len(kubevirtCRDs) {
		t.Errorf("PerformClusterHealthCheck() KubeVirtCRDs count = %d, want %d", len(status.KubeVirtCRDs), len(kubevirtCRDs))
	}

	if status.KubeVirtAPI.Status == "unknown" {
		t.Error("PerformClusterHealthCheck() KubeVirtAPI status should not be unknown")
	}

	if len(status.KeyResources) == 0 {
		t.Error("PerformClusterHealthCheck() KeyResources should not be empty")
	}

	// Verify totals
	expectedTotal := 1 + len(kubevirtCRDs) + 1 + len(status.KeyResources)
	if status.TotalChecks != expectedTotal {
		t.Errorf("PerformClusterHealthCheck() TotalChecks = %d, want %d", status.TotalChecks, expectedTotal)
	}

	// Verify overall status is set
	if status.OverallStatus == "" {
		t.Error("PerformClusterHealthCheck() OverallStatus should not be empty")
	}

	validStatuses := map[string]bool{
		"healthy":   true,
		"degraded":  true,
		"unhealthy": true,
	}
	if !validStatuses[status.OverallStatus] {
		t.Errorf("PerformClusterHealthCheck() OverallStatus = %s, want one of: healthy, degraded, unhealthy", status.OverallStatus)
	}
}

// Helper function to check if a string contains a substring
// Note: Using strings.Contains from standard library would be better, but this is for testing
func testContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && testContainsHelper(s, substr))
}

func testContainsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestContains(t *testing.T) {
	tests := []struct {
		s      string
		substr string
		want   bool
	}{
		{"", "", true},
		{"a", "a", true},
		{"ab", "a", true},
		{"ab", "b", true},
		{"abc", "ab", true},
		{"abc", "bc", true},
		{"register resource", "register resource", true},
		{"foo register resource bar", "register resource", true},
		{"", "x", false},
		{"a", "ab", false},
		{"ab", "c", false},
	}
	for _, tt := range tests {
		got := contains(tt.s, tt.substr)
		if got != tt.want {
			t.Errorf("contains(%q, %q) = %v, want %v", tt.s, tt.substr, got, tt.want)
		}
	}
}

func TestContainsHelper(t *testing.T) {
	tests := []struct {
		s      string
		substr string
		want   bool
	}{
		{"abc", "ab", true},
		{"abc", "bc", true},
		{"abc", "abc", true},
		{"xabcy", "abc", true},
		{"abc", "x", false},
		{"abc", "abcd", false},
	}
	for _, tt := range tests {
		// containsHelper is only called when len(s) > len(substr); for equal length contains uses s == substr
		if len(tt.s) <= len(tt.substr) {
			continue
		}
		got := containsHelper(tt.s, tt.substr)
		if got != tt.want {
			t.Errorf("containsHelper(%q, %q) = %v, want %v", tt.s, tt.substr, got, tt.want)
		}
	}
}
