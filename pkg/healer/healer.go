package healer

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/daigoro86dev/k8s-healer/pkg/util"
	v1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// Healer holds the Kubernetes client and configuration for watching.
type Healer struct {
	ClientSet                  *kubernetes.Clientset
	DynamicClient              dynamic.Interface
	Namespaces                 []string
	StopCh                     chan struct{}
	HealedPods                 map[string]time.Time // Tracks recently healed pods
	HealedNodes                map[string]time.Time // Tracks recently healed nodes
	HealedVMs                  map[string]time.Time // Tracks recently healed VMs
	HealedCRDs                 map[string]time.Time // Tracks recently cleaned CRDs
	TrackedCRDs                map[string]bool      // Tracks all CRD resources we've seen (for creation logging)
	HealCooldown               time.Duration
	EnableVMHealing            bool                 // Flag to enable VM healing
	EnableCRDCleanup           bool                 // Flag to enable CRD cleanup
	CRDResources               []string             // List of CRD resources to monitor (e.g., ["virtualmachines.virtualmachine.kubevirt.io"])
	StaleAge                   time.Duration        // Age threshold for stale resources
	CleanupFinalizers          bool                 // Whether to remove finalizers before deletion
	EnableResourceOptimization bool                 // Flag to enable resource optimization during cluster strain
	StrainThreshold            float64              // Percentage of nodes under pressure to trigger optimization
	OptimizedPods              map[string]time.Time // Tracks recently optimized pods
	EnableNamespacePolling          bool                 // Flag to enable namespace polling
	NamespacePattern                string               // Pattern to match namespaces (e.g., "test-*")
	NamespacePollInterval           time.Duration        // How often to poll for new namespaces
	WatchedNamespaces               map[string]bool      // Tracks namespaces we're currently watching
	EnableResourceCreationThrottling bool                 // Flag to enable resource creation throttling during cluster strain
	CurrentClusterStrain             *util.ClusterStrainInfo // Current cluster strain state (updated by resource optimization)
}

// NewHealer initializes the Kubernetes client configuration using kubeconfig or in-cluster settings.
func NewHealer(kubeconfigPath string, namespaces []string, enableVMHealing bool, enableCRDCleanup bool, crdResources []string, enableNamespacePolling bool, namespacePattern string, namespacePollInterval time.Duration) (*Healer, error) {
	var config *rest.Config
	var err error

	// Try to load configuration from the specified path, or default locations
	if kubeconfigPath != "" {
		// Use explicit kubeconfig path if provided
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	} else {
		// Fallback to in-cluster config or default ~/.kube/config
		config, err = clientcmd.BuildConfigFromFlags("", "")
	}

	if err != nil {
		return nil, fmt.Errorf("failed to build Kubernetes config: %w", err)
	}

	// Create the clientset used for making API calls
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes clientset: %w", err)
	}

	// Create dynamic client for VM and CRD operations
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return &Healer{
		ClientSet:                  clientset,
		DynamicClient:              dynamicClient,
		Namespaces:                 namespaces,
		StopCh:                     make(chan struct{}),
		HealedPods:                 make(map[string]time.Time),
		HealedNodes:                make(map[string]time.Time),
		HealedVMs:                  make(map[string]time.Time),
		HealedCRDs:                 make(map[string]time.Time),
		TrackedCRDs:                make(map[string]bool),
		HealCooldown:               10 * time.Minute, // default cooldown
		EnableVMHealing:            enableVMHealing,
		EnableCRDCleanup:           enableCRDCleanup,
		CRDResources:               crdResources,
		StaleAge:                   6 * time.Minute,                    // default stale age
		CleanupFinalizers:          true,                               // default to cleaning up finalizers
		EnableResourceOptimization: true,                               // default to enabled
		StrainThreshold:            util.DefaultClusterStrainThreshold, // default 30%
		OptimizedPods:              make(map[string]time.Time),
		EnableNamespacePolling:          enableNamespacePolling,
		NamespacePattern:                namespacePattern,
		NamespacePollInterval:           namespacePollInterval,
		EnableResourceCreationThrottling: true, // default to enabled
		CurrentClusterStrain:            nil,  // Will be updated by resource optimization checks
	}, nil
}

// Watch starts the informer loop for all configured namespaces concurrently.
func (h *Healer) Watch() {
	// If no namespaces are provided, default to watching all namespaces
	if len(h.Namespaces) == 0 {
		fmt.Println("No namespaces specified. Watching all namespaces (using NamespaceAll).")
		h.Namespaces = []string{metav1.NamespaceAll}
	}

	fmt.Printf("Starting healer to watch namespaces: [%s]\n", strings.Join(h.Namespaces, ", "))
	if h.EnableVMHealing {
		fmt.Println("VM healing is ENABLED - monitoring nodes and VirtualMachines")
	} else {
		fmt.Println("VM healing is DISABLED - monitoring pods only")
	}
	if h.EnableCRDCleanup {
		fmt.Printf("CRD cleanup is ENABLED - monitoring %d CRD resource types: [%s]\n", len(h.CRDResources), strings.Join(h.CRDResources, ", "))
		fmt.Printf("  Stale age threshold: %v\n", h.StaleAge)
		fmt.Printf("  Cleanup finalizers: %v\n", h.CleanupFinalizers)
	} else {
		fmt.Println("CRD cleanup is DISABLED")
	}
	if h.EnableResourceOptimization {
		fmt.Printf("Resource optimization is ENABLED - will optimize resources during cluster strain\n")
		fmt.Printf("  Strain threshold: %.1f%% of nodes under pressure\n", h.StrainThreshold)
		if h.EnableResourceCreationThrottling {
			fmt.Printf("  Resource creation throttling: ENABLED - will warn when resources are created during cluster strain\n")
		} else {
			fmt.Printf("  Resource creation throttling: DISABLED\n")
		}
	} else {
		fmt.Println("Resource optimization is DISABLED")
		if h.EnableResourceCreationThrottling {
			fmt.Printf("Resource creation throttling is ENABLED - will warn when resources are created during cluster strain\n")
		}
	}

	h.startHealCacheCleaner()

	// Start a separate goroutine for the informer watch in each namespace
	for _, ns := range h.Namespaces {
		go h.watchSingleNamespace(ns)
	}

	// Start VM monitoring if enabled
	if h.EnableVMHealing {
		go h.watchNodes()
		go h.watchVirtualMachines()
	}

	// Start CRD cleanup monitoring if enabled
	if h.EnableCRDCleanup {
		for _, crdResource := range h.CRDResources {
			go h.watchCRDResource(crdResource)
		}
	}

	// Start resource optimization monitoring if enabled
	if h.EnableResourceOptimization {
		go h.watchResourceOptimization()
	}

	// Start namespace polling if enabled
	if h.EnableNamespacePolling && h.NamespacePattern != "" {
		go h.pollForNewNamespaces()
	}

	// Block the main goroutine until the StopCh channel is closed (on SIGINT/SIGTERM)
	<-h.StopCh
}

// watchSingleNamespace sets up a Pod Informer for one namespace.
func (h *Healer) watchSingleNamespace(namespace string) {
	// Create a SharedInformerFactory scoped to the namespace, with a 30s resync period
	factory := informers.NewSharedInformerFactoryWithOptions(h.ClientSet, time.Second*30, informers.WithNamespace(namespace))

	// Get the Pod Informer
	podInformer := factory.Core().V1().Pods().Informer()

	// Register event handlers
	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		// Detect new pod creation for throttling
		AddFunc: func(obj interface{}) {
			pod := obj.(*v1.Pod)
			h.handleResourceCreation("pod", pod.Namespace, pod.Name)
		},
		// We use UpdateFunc because a Pod becomes unhealthy (e.g., CrashLoopBackOff) after its initial creation
		UpdateFunc: func(oldObj, newObj interface{}) {
			newPod := newObj.(*v1.Pod)
			h.checkAndHealPod(newPod)
		},
	})

	// Start the informer and wait for the cache to be synced
	factory.Start(h.StopCh)
	if !cache.WaitForCacheSync(h.StopCh, podInformer.HasSynced) {
		fmt.Printf("Error syncing cache for namespace %s. Exiting watch.\n", namespace)
		return
	}

	fmt.Printf("✅ Successfully synced cache and started watching namespace: %s\n", namespace)
}

// checkAndHealPod checks a Pod's health and executes deletion if necessary.
func (h *Healer) checkAndHealPod(pod *v1.Pod) {
	// Skip unmanaged pods
	if len(pod.OwnerReferences) == 0 {
		return
	}

	// Skip if recently healed
	podKey := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
	if lastHeal, ok := h.HealedPods[podKey]; ok {
		if time.Since(lastHeal) < h.HealCooldown {
			fmt.Printf("   [SKIP] ⏳ Pod %s was healed %.0f seconds ago — skipping re-heal.\n",
				podKey, time.Since(lastHeal).Seconds())
			return
		}
	}

	if util.IsUnhealthy(pod) {
		reason := util.GetHealReason(pod)
		fmt.Printf("\n!!! HEALING ACTION REQUIRED !!!\n")
		fmt.Printf("    Pod: %s\n", podKey)
		fmt.Printf("    Reason: %s\n", reason)

		h.triggerPodDeletion(pod)

		// Record the healing timestamp
		h.HealedPods[podKey] = time.Now()

		fmt.Printf("!!! HEALING ACTION COMPLETE !!!\n\n")
	}
}

func (h *Healer) startHealCacheCleaner() {
	ticker := time.NewTicker(30 * time.Minute)
	go func() {
		for {
			select {
			case <-ticker.C:
				now := time.Now()
				// Clean up healed pods
				for key, t := range h.HealedPods {
					if now.Sub(t) > 2*h.HealCooldown {
						delete(h.HealedPods, key)
					}
				}
				// Clean up healed nodes
				for key, t := range h.HealedNodes {
					if now.Sub(t) > 2*h.HealCooldown {
						delete(h.HealedNodes, key)
					}
				}
				// Clean up healed VMs
				for key, t := range h.HealedVMs {
					if now.Sub(t) > 2*h.HealCooldown {
						delete(h.HealedVMs, key)
					}
				}
				// Clean up healed CRDs
				for key, t := range h.HealedCRDs {
					if now.Sub(t) > 2*h.HealCooldown {
						delete(h.HealedCRDs, key)
					}
				}
				// Clean up optimized pods
				for key, t := range h.OptimizedPods {
					if now.Sub(t) > 2*h.HealCooldown {
						delete(h.OptimizedPods, key)
					}
				}
			case <-h.StopCh:
				ticker.Stop()
				return
			}
		}
	}()
}

// triggerPodDeletion deletes the Pod, relying on the managing controller to recreate a fresh one.
func (h *Healer) triggerPodDeletion(pod *v1.Pod) {
	// Use a context with timeout for the API call to prevent indefinite hangs
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	// Perform the API Delete call
	err := h.ClientSet.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})

	if err != nil {
		fmt.Printf("   [FAIL] ❌ Failed to delete pod %s/%s: %v\n", pod.Namespace, pod.Name, err)
	} else {
		fmt.Printf("   [SUCCESS] ✅ Deleted pod %s/%s. Controller is expected to recreate the Pod immediately.\n", pod.Namespace, pod.Name)
	}
}

// watchNodes sets up a Node Informer to monitor node health
func (h *Healer) watchNodes() {
	// Create a SharedInformerFactory for nodes (cluster-scoped)
	factory := informers.NewSharedInformerFactory(h.ClientSet, time.Second*30)

	// Get the Node Informer
	nodeInformer := factory.Core().V1().Nodes().Informer()

	// Register event handlers
	nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			newNode := newObj.(*v1.Node)
			h.checkAndHealNode(newNode)
		},
	})

	// Start the informer and wait for the cache to be synced
	factory.Start(h.StopCh)
	if !cache.WaitForCacheSync(h.StopCh, nodeInformer.HasSynced) {
		fmt.Println("Error syncing node cache. Exiting node watch.")
		return
	}

	fmt.Println("✅ Successfully synced node cache and started watching nodes")
}

// checkAndHealNode checks a Node's health and executes healing if necessary
func (h *Healer) checkAndHealNode(node *v1.Node) {
	// Skip if recently healed
	nodeKey := node.Name
	if lastHeal, ok := h.HealedNodes[nodeKey]; ok {
		if time.Since(lastHeal) < h.HealCooldown {
			fmt.Printf("   [SKIP] ⏳ Node %s was healed %.0f seconds ago — skipping re-heal.\n",
				nodeKey, time.Since(lastHeal).Seconds())
			return
		}
	}

	if util.IsNodeUnhealthy(node) {
		reason := util.GetNodeHealReason(node)
		fmt.Printf("\n!!! NODE HEALING ACTION REQUIRED !!!\n")
		fmt.Printf("    Node: %s\n", nodeKey)
		fmt.Printf("    Reason: %s\n", reason)

		h.triggerNodeHealing(node)

		// Record the healing timestamp
		h.HealedNodes[nodeKey] = time.Now()

		fmt.Printf("!!! NODE HEALING ACTION COMPLETE !!!\n\n")
	}
}

// triggerNodeHealing performs healing actions on a failing node
func (h *Healer) triggerNodeHealing(node *v1.Node) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	// First, try to drain the node to safely evict pods
	fmt.Printf("   [INFO] 🔄 Attempting to drain node %s...\n", node.Name)

	// Create a drain helper (simplified version)
	err := h.drainNode(ctx, node.Name)
	if err != nil {
		fmt.Printf("   [WARN] ⚠️ Failed to drain node %s: %v\n", node.Name, err)
		fmt.Printf("   [INFO] 🔄 Proceeding with node deletion anyway...\n")
	}

	// Delete the node
	err = h.ClientSet.CoreV1().Nodes().Delete(ctx, node.Name, metav1.DeleteOptions{})
	if err != nil {
		fmt.Printf("   [FAIL] ❌ Failed to delete node %s: %v\n", node.Name, err)
	} else {
		fmt.Printf("   [SUCCESS] ✅ Deleted node %s. Machine controller should recreate it.\n", node.Name)
	}
}

// drainNode attempts to drain a node by evicting pods
func (h *Healer) drainNode(ctx context.Context, nodeName string) error {
	// Get all pods on the node
	pods, err := h.ClientSet.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", nodeName),
	})
	if err != nil {
		return fmt.Errorf("failed to list pods on node: %w", err)
	}

	// Evict each pod
	for _, pod := range pods.Items {
		// Skip pods without owner references (static pods)
		if len(pod.OwnerReferences) == 0 {
			continue
		}

		// Skip DaemonSet pods
		for _, ownerRef := range pod.OwnerReferences {
			if ownerRef.Kind == "DaemonSet" {
				continue
			}
		}

		// Evict the pod using the eviction API
		err := h.ClientSet.CoreV1().Pods(pod.Namespace).EvictV1(ctx, &policyv1.Eviction{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pod.Name,
				Namespace: pod.Namespace,
			},
		})
		if err != nil {
			fmt.Printf("   [WARN] ⚠️ Failed to evict pod %s/%s: %v\n", pod.Namespace, pod.Name, err)
		} else {
			fmt.Printf("   [INFO] ✅ Evicted pod %s/%s from node %s\n", pod.Namespace, pod.Name, nodeName)
		}
	}

	return nil
}

// watchVirtualMachines sets up a VirtualMachine informer to monitor VM health
func (h *Healer) watchVirtualMachines() {
	// We'll use a different approach - list VMs periodically since dynamic informers are complex
	go h.periodicVirtualMachineCheck()
}

// periodicVirtualMachineCheck periodically checks VirtualMachine health
func (h *Healer) periodicVirtualMachineCheck() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			h.checkAllVirtualMachines()
		case <-h.StopCh:
			return
		}
	}
}

// checkAllVirtualMachines checks all VirtualMachines in watched namespaces
func (h *Healer) checkAllVirtualMachines() {
	vmGVR := schema.GroupVersionResource{
		Group:    "kubevirt.io",
		Version:  "v1",
		Resource: "virtualmachines",
	}

	namespaces := h.Namespaces
	if len(namespaces) == 0 || (len(namespaces) == 1 && namespaces[0] == metav1.NamespaceAll) {
		// Get all namespaces
		nsList, err := h.ClientSet.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			fmt.Printf("Error listing namespaces for VM check: %v\n", err)
			return
		}
		namespaces = make([]string, len(nsList.Items))
		for i, ns := range nsList.Items {
			namespaces[i] = ns.Name
		}
	}

	for _, ns := range namespaces {
		vms, err := h.DynamicClient.Resource(vmGVR).Namespace(ns).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			// Skip if VirtualMachine CRD is not available
			continue
		}

		for _, vmUnstructured := range vms.Items {
			h.checkAndHealVirtualMachine(&vmUnstructured)
		}
	}
}

// checkAndHealVirtualMachine checks a VirtualMachine's health and executes healing if necessary
func (h *Healer) checkAndHealVirtualMachine(vm *unstructured.Unstructured) {
	vmKey := fmt.Sprintf("%s/%s", vm.GetNamespace(), vm.GetName())
	
	// Continue monitoring VM health (for logging purposes)
	isUnhealthy := util.IsVirtualMachineUnhealthy(vm)
	if isUnhealthy {
		reason := util.GetVirtualMachineHealReason(vm)
		fmt.Printf("   [MONITOR] 🔍 VM %s health check: %s\n", vmKey, reason)
	}

	// Skip if recently healed
	if lastHeal, ok := h.HealedVMs[vmKey]; ok {
		if time.Since(lastHeal) < h.HealCooldown {
			fmt.Printf("   [SKIP] ⏳ VM %s was healed %.0f seconds ago — skipping re-heal.\n",
				vmKey, time.Since(lastHeal).Seconds())
			return
		}
	}

	// Only delete VMs that are older than 6 minutes (regardless of health state)
	// This allows tests to do self-cleanup, but cleans up truly stale VMs
	vmAge := time.Since(vm.GetCreationTimestamp().Time)
	vmMaxAge := 6 * time.Minute
	
	if vmAge > vmMaxAge {
		fmt.Printf("\n!!! VM CLEANUP ACTION REQUIRED !!!\n")
		fmt.Printf("    VM: %s\n", vmKey)
		fmt.Printf("    Age: %v (threshold: %v)\n", vmAge.Round(time.Second), vmMaxAge)
		if isUnhealthy {
			reason := util.GetVirtualMachineHealReason(vm)
			fmt.Printf("    Health Status: %s\n", reason)
		} else {
			fmt.Printf("    Health Status: Appears healthy, but VM is older than threshold\n")
		}

		h.triggerVirtualMachineHealing(vm)

		// Record the healing timestamp
		h.HealedVMs[vmKey] = time.Now()

		fmt.Printf("!!! VM CLEANUP ACTION COMPLETE !!!\n\n")
	}
}

// triggerVirtualMachineHealing performs healing actions on a failing VirtualMachine
func (h *Healer) triggerVirtualMachineHealing(vm *unstructured.Unstructured) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	vmGVR := schema.GroupVersionResource{
		Group:    "kubevirt.io",
		Version:  "v1",
		Resource: "virtualmachines",
	}

	// Delete the VirtualMachine
	err := h.DynamicClient.Resource(vmGVR).Namespace(vm.GetNamespace()).Delete(ctx, vm.GetName(), metav1.DeleteOptions{})
	if err != nil {
		fmt.Printf("   [FAIL] ❌ Failed to delete VM %s/%s: %v\n", vm.GetNamespace(), vm.GetName(), err)
	} else {
		fmt.Printf("   [SUCCESS] ✅ Deleted VM %s/%s. Controller should recreate it.\n", vm.GetNamespace(), vm.GetName())
	}
}

// watchCRDResource sets up periodic monitoring for a specific CRD resource type
func (h *Healer) watchCRDResource(crdResource string) {
	// Parse the CRD resource string (format: "resource.group/version" or "resource.group")
	// Examples: "virtualmachines.kubevirt.io/v1" or "datavolumes.cdi.kubevirt.io"
	parts := strings.Split(crdResource, "/")
	resourceAndGroup := parts[0]
	version := ""
	if len(parts) > 1 {
		version = parts[1]
	}

	// Parse resource and group
	resourceParts := strings.Split(resourceAndGroup, ".")
	if len(resourceParts) < 2 {
		fmt.Printf("   [ERROR] ❌ Invalid CRD resource format: %s (expected format: resource.group or resource.group/version)\n", crdResource)
		return
	}

	resource := resourceParts[0]
	group := strings.Join(resourceParts[1:], ".")

	// If version is not specified, try to discover it
	if version == "" {
		version = "v1" // Default to v1, could be enhanced to discover from CRD
	}

	fmt.Printf("   [INFO] 🔍 Starting CRD cleanup monitor for %s/%s/%s\n", group, version, resource)

	// Start periodic checks
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			h.checkCRDResources(group, version, resource)
		case <-h.StopCh:
			return
		}
	}
}

// checkCRDResources checks all instances of a CRD resource type for stale resources
func (h *Healer) checkCRDResources(group, version, resource string) {
	gvr := schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	}

	namespaces := h.Namespaces
	if len(namespaces) == 0 || (len(namespaces) == 1 && namespaces[0] == metav1.NamespaceAll) {
		// Get all namespaces
		nsList, err := h.ClientSet.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			fmt.Printf("   [ERROR] ❌ Error listing namespaces for CRD check: %v\n", err)
			return
		}
		namespaces = make([]string, len(nsList.Items))
		for i, ns := range nsList.Items {
			namespaces[i] = ns.Name
		}
	}

	for _, ns := range namespaces {
		resources, err := h.DynamicClient.Resource(gvr).Namespace(ns).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			// Skip if CRD is not available or not accessible
			continue
		}

		for _, resourceUnstructured := range resources.Items {
			// Log CRD creation if it's a new resource we haven't seen before
			h.logCRDCreation(&resourceUnstructured, gvr)
			// Check if resource is stale and needs cleanup
			h.checkAndCleanupCRDResource(&resourceUnstructured, gvr)
		}
	}
}

// logCRDCreation logs when a new CRD resource is created
func (h *Healer) logCRDCreation(resource *unstructured.Unstructured, gvr schema.GroupVersionResource) {
	resourceKey := fmt.Sprintf("%s/%s/%s", gvr.Resource, resource.GetNamespace(), resource.GetName())

	// Check if we've seen this resource before
	if !h.TrackedCRDs[resourceKey] {
		// New resource detected
		creationTime := resource.GetCreationTimestamp()
		age := time.Since(creationTime.Time)

		// Check for throttling if enabled
		if h.EnableResourceCreationThrottling {
			h.handleResourceCreation(gvr.Resource, resource.GetNamespace(), resource.GetName())
		}

		fmt.Printf("   [INFO] ✨ New CRD resource created: %s/%s/%s (age: %v)\n",
			gvr.Resource, resource.GetNamespace(), resource.GetName(), age.Round(time.Second))

		// Mark as tracked
		h.TrackedCRDs[resourceKey] = true
	}
}

// checkAndCleanupCRDResource checks if a CRD resource is stale and cleans it up if needed
func (h *Healer) checkAndCleanupCRDResource(resource *unstructured.Unstructured, gvr schema.GroupVersionResource) {
	// Skip if recently cleaned
	resourceKey := fmt.Sprintf("%s/%s/%s", gvr.Resource, resource.GetNamespace(), resource.GetName())
	if lastClean, ok := h.HealedCRDs[resourceKey]; ok {
		if time.Since(lastClean) < h.HealCooldown {
			return
		}
	}

	// Special handling for VirtualMachines: only delete based on age (6 minutes), not health status
	// This allows tests to do self-cleanup, but cleans up truly stale VMs
	if gvr.Resource == "virtualmachines" && gvr.Group == "kubevirt.io" {
		vmAge := time.Since(resource.GetCreationTimestamp().Time)
		vmMaxAge := 6 * time.Minute
		
		if vmAge > vmMaxAge {
			reason := fmt.Sprintf("VM older than age threshold (%v old, threshold: %v)", vmAge.Round(time.Second), vmMaxAge)
			fmt.Printf("\n!!! CRD CLEANUP ACTION REQUIRED !!!\n")
			fmt.Printf("    Resource: %s/%s/%s\n", gvr.Resource, resource.GetNamespace(), resource.GetName())
			fmt.Printf("    Reason: %s\n", reason)

			h.triggerCRDCleanup(resource, gvr)

			// Record the cleanup timestamp
			h.HealedCRDs[resourceKey] = time.Now()

			fmt.Printf("!!! CRD CLEANUP ACTION COMPLETE !!!\n\n")
		}
		return
	}

	// For other CRD resources, use the standard stale check (age + error conditions)
	if util.IsCRDResourceStale(resource, h.StaleAge, h.CleanupFinalizers) {
		reason := util.GetCRDResourceStaleReason(resource, h.StaleAge, h.CleanupFinalizers)
		fmt.Printf("\n!!! CRD CLEANUP ACTION REQUIRED !!!\n")
		fmt.Printf("    Resource: %s/%s/%s\n", gvr.Resource, resource.GetNamespace(), resource.GetName())
		fmt.Printf("    Reason: %s\n", reason)

		h.triggerCRDCleanup(resource, gvr)

		// Record the cleanup timestamp
		h.HealedCRDs[resourceKey] = time.Now()

		fmt.Printf("!!! CRD CLEANUP ACTION COMPLETE !!!\n\n")
	}
}

// triggerCRDCleanup performs cleanup actions on a stale CRD resource
func (h *Healer) triggerCRDCleanup(resource *unstructured.Unstructured, gvr schema.GroupVersionResource) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	resourceName := resource.GetName()
	resourceNamespace := resource.GetNamespace()

	// If cleanup finalizers is enabled and resource has finalizers, remove them first
	if h.CleanupFinalizers && len(resource.GetFinalizers()) > 0 {
		fmt.Printf("   [INFO] 🔄 Removing finalizers from %s/%s/%s...\n", gvr.Resource, resourceNamespace, resourceName)

		// Get the current resource to update
		currentResource, err := h.DynamicClient.Resource(gvr).Namespace(resourceNamespace).Get(ctx, resourceName, metav1.GetOptions{})
		if err != nil {
			fmt.Printf("   [WARN] ⚠️ Failed to get resource %s/%s/%s for finalizer removal: %v\n", gvr.Resource, resourceNamespace, resourceName, err)
		} else {
			// Remove all finalizers
			currentResource.SetFinalizers([]string{})
			_, err = h.DynamicClient.Resource(gvr).Namespace(resourceNamespace).Update(ctx, currentResource, metav1.UpdateOptions{})
			if err != nil {
				fmt.Printf("   [WARN] ⚠️ Failed to remove finalizers from %s/%s/%s: %v\n", gvr.Resource, resourceNamespace, resourceName, err)
				fmt.Printf("   [INFO] 🔄 Proceeding with deletion anyway...\n")
			} else {
				fmt.Printf("   [SUCCESS] ✅ Removed finalizers from %s/%s/%s\n", gvr.Resource, resourceNamespace, resourceName)
			}
		}
	}

	// Delete the resource
	err := h.DynamicClient.Resource(gvr).Namespace(resourceNamespace).Delete(ctx, resourceName, metav1.DeleteOptions{})
	if err != nil {
		fmt.Printf("   [FAIL] ❌ Failed to delete %s/%s/%s: %v\n", gvr.Resource, resourceNamespace, resourceName, err)
	} else {
		fmt.Printf("   [SUCCESS] ✅ Deleted stale resource %s/%s/%s\n", gvr.Resource, resourceNamespace, resourceName)
		// Remove from tracked CRDs when deleted
		resourceKey := fmt.Sprintf("%s/%s/%s", gvr.Resource, resourceNamespace, resourceName)
		delete(h.TrackedCRDs, resourceKey)
	}
}

// watchResourceOptimization periodically checks cluster resource strain and optimizes pods
func (h *Healer) watchResourceOptimization() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			h.checkAndOptimizeResources()
		case <-h.StopCh:
			return
		}
	}
}

// checkAndOptimizeResources checks cluster strain and optimizes pods if needed
func (h *Healer) checkAndOptimizeResources() {
	// Get all nodes
	nodes, err := h.ClientSet.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("   [ERROR] ❌ Error listing nodes for resource optimization: %v\n", err)
		return
	}

	// Convert to slice of node pointers
	nodeList := make([]*v1.Node, len(nodes.Items))
	for i := range nodes.Items {
		nodeList[i] = &nodes.Items[i]
	}

	// Check cluster strain
	strainInfo := util.IsClusterUnderStrain(nodeList, h.StrainThreshold)
	
	// Update current cluster strain state for throttling checks
	h.CurrentClusterStrain = &strainInfo

	if !strainInfo.HasStrain {
		// Cluster is healthy, no optimization needed
		// Clear strain state for throttling (cluster recovered)
		return
	}

	fmt.Printf("   [INFO] ⚠️ Cluster under strain: %.1f%% of nodes (%d/%d) under resource pressure\n",
		strainInfo.StrainPercentage, strainInfo.StrainedNodesCount, strainInfo.TotalNodesCount)
	fmt.Printf("   [INFO] 🔍 Nodes under pressure: %s\n", strings.Join(strainInfo.NodesUnderPressure, ", "))

	// Get all namespaces to check for pods (we want to check all namespaces, not just watched ones)
	// This allows us to evict pods from any namespace to free resources for test pods
	nsList, err := h.ClientSet.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("   [ERROR] ❌ Error listing namespaces for resource optimization: %v\n", err)
		return
	}
	allNamespaces := make([]string, len(nsList.Items))
	for i, ns := range nsList.Items {
		allNamespaces[i] = ns.Name
	}

	// Find pods that should be evicted for resource optimization
	// Separate test namespace pods from non-test pods
	testNamespacePods := []*v1.Pod{}
	nonTestPods := []*v1.Pod{}

	// Check all namespaces to find test pods and non-test pods to evict
	for _, ns := range allNamespaces {
		pods, err := h.ClientSet.CoreV1().Pods(ns).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			continue
		}

		for i := range pods.Items {
			pod := &pods.Items[i]

			// Skip unmanaged pods
			if len(pod.OwnerReferences) == 0 {
				continue
			}

			// Skip if recently optimized
			podKey := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
			if lastOpt, ok := h.OptimizedPods[podKey]; ok {
				if time.Since(lastOpt) < h.HealCooldown {
					continue
				}
			}

			// Check if this is a test namespace pod
			isTestNamespace := h.isTestNamespace(pod.Namespace)

			if isTestNamespace {
				// Track test namespace pods (we'll check if they need resources)
				// Always include test pods to monitor their resource needs
				testNamespacePods = append(testNamespacePods, pod)
			} else {
				// For non-test pods, check if they should be evicted to free resources
				// We're more aggressive with non-test pods - evict them if:
				// 1. Cluster is under strain, OR
				// 2. They have resource issues, OR
				// 3. Test pods need resources
				shouldEvict := false
				if strainInfo.HasStrain {
					// If cluster is strained, consider evicting any non-test pod with resource issues
					if util.ShouldEvictPodForResourceOptimization(pod, strainInfo) {
						shouldEvict = true
					} else {
						// Even if not resource-constrained, consider evicting low-priority non-test pods when cluster is strained
						priority := util.GetPodPriority(pod)
						if priority < 50 { // Medium or lower priority
							shouldEvict = true
						}
					}
				} else {
					// If cluster is not strained, only evict non-test pods with clear resource issues
					if util.ShouldEvictPodForResourceOptimization(pod, strainInfo) {
						shouldEvict = true
					}
				}

				if shouldEvict {
					nonTestPods = append(nonTestPods, pod)
				}
			}
		}
	}

	// Check if test namespace pods need resources
	testPodsNeedResources := false
	if len(testNamespacePods) > 0 {
		for _, pod := range testNamespacePods {
			resourceIssue := util.IsPodResourceConstrained(pod)
			if resourceIssue.HasIssue {
				testPodsNeedResources = true
				fmt.Printf("   [INFO] ⚠️ Test pod %s/%s needs resources (OOMKilled: %v, Restarts: %d, Pending: %v)\n",
					pod.Namespace, pod.Name, resourceIssue.IsOOMKilled, resourceIssue.RestartCount,
					pod.Status.Phase == v1.PodPending)
				break
			}
		}
	}

	// If test pods need resources or cluster is under strain, evict non-test pods to free resources
	podsToEvict := []*v1.Pod{}
	if testPodsNeedResources || strainInfo.HasStrain {
		if len(nonTestPods) > 0 {
			fmt.Printf("   [INFO] 🎯 Prioritizing test namespace pods - considering evicting %d non-test pod(s) to free resources\n", len(nonTestPods))
			podsToEvict = nonTestPods
		} else {
			fmt.Printf("   [INFO] ℹ️ No non-test pods available to evict for resource optimization\n")
		}
	}

	// Sort pods by priority (evict lower priority first)
	if len(podsToEvict) > 0 {
		// Simple sort: lower priority score = evict first
		for i := 0; i < len(podsToEvict)-1; i++ {
			for j := i + 1; j < len(podsToEvict); j++ {
				priorityI := util.GetPodPriority(podsToEvict[i])
				priorityJ := util.GetPodPriority(podsToEvict[j])
				if priorityI > priorityJ {
					podsToEvict[i], podsToEvict[j] = podsToEvict[j], podsToEvict[i]
				}
			}
		}

		// Evict up to 5 pods at a time to avoid overwhelming the cluster
		maxEvictions := 5
		if len(podsToEvict) > maxEvictions {
			podsToEvict = podsToEvict[:maxEvictions]
		}

		// Evict pods
		for _, pod := range podsToEvict {
			h.evictPodForOptimization(pod, strainInfo)
		}
	}
}

// evictPodForOptimization evicts a pod for resource optimization
func (h *Healer) evictPodForOptimization(pod *v1.Pod, strainInfo util.ClusterStrainInfo) {
	// Safety check: Never evict test namespace pods
	if h.isTestNamespace(pod.Namespace) {
		fmt.Printf("   [SKIP] 🛡️ Skipping eviction of test namespace pod %s/%s (protected)\n", pod.Namespace, pod.Name)
		return
	}

	podKey := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
	reason := util.GetPodEvictionReason(pod, strainInfo)

	fmt.Printf("\n!!! RESOURCE OPTIMIZATION ACTION REQUIRED !!!\n")
	fmt.Printf("    Pod: %s (non-test namespace)\n", podKey)
	fmt.Printf("    Reason: %s\n", reason)
	fmt.Printf("    Cluster Strain: %.1f%% (%d/%d nodes under pressure)\n",
		strainInfo.StrainPercentage, strainInfo.StrainedNodesCount, strainInfo.TotalNodesCount)
	fmt.Printf("    Action: Evicting to free resources for test namespace pods\n")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	// Use eviction API for graceful eviction
	err := h.ClientSet.CoreV1().Pods(pod.Namespace).EvictV1(ctx, &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
		DeleteOptions: &metav1.DeleteOptions{},
	})

	if err != nil {
		fmt.Printf("   [FAIL] ❌ Failed to evict pod %s: %v\n", podKey, err)
		// Fallback to direct deletion if eviction fails
		err = h.ClientSet.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
		if err != nil {
			fmt.Printf("   [FAIL] ❌ Failed to delete pod %s: %v\n", podKey, err)
		} else {
			fmt.Printf("   [SUCCESS] ✅ Deleted pod %s for resource optimization\n", podKey)
			h.OptimizedPods[podKey] = time.Now()
		}
	} else {
		fmt.Printf("   [SUCCESS] ✅ Evicted pod %s for resource optimization\n", podKey)
		h.OptimizedPods[podKey] = time.Now()
	}

	fmt.Printf("!!! RESOURCE OPTIMIZATION ACTION COMPLETE !!!\n\n")
}

// pollForNewNamespaces periodically checks for new namespaces matching the pattern
func (h *Healer) pollForNewNamespaces() {
	if h.NamespacePollInterval == 0 {
		h.NamespacePollInterval = 30 * time.Second // Default poll interval
	}

	ticker := time.NewTicker(h.NamespacePollInterval)
	defer ticker.Stop()

	fmt.Printf("   [INFO] 🔍 Starting namespace polling for pattern: %s (interval: %v)\n", h.NamespacePattern, h.NamespacePollInterval)

	for {
		select {
		case <-ticker.C:
			h.discoverNewNamespaces()
		case <-h.StopCh:
			return
		}
	}
}

// discoverNewNamespaces discovers new namespaces matching the pattern and starts watching them
func (h *Healer) discoverNewNamespaces() {
	// List all namespaces
	nsList, err := h.ClientSet.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("   [ERROR] ❌ Error listing namespaces for polling: %v\n", err)
		return
	}

	// Match namespaces against the pattern
	matchedNamespaces := make(map[string]bool)
	for _, ns := range nsList.Items {
		matched, err := filepath.Match(h.NamespacePattern, ns.Name)
		if err != nil {
			fmt.Printf("   [WARN] ⚠️ Invalid namespace pattern '%s': %v\n", h.NamespacePattern, err)
			continue
		}
		if matched {
			matchedNamespaces[ns.Name] = true
		}
	}

	// Check for new namespaces we're not watching yet
	newNamespaces := []string{}
	for nsName := range matchedNamespaces {
		if !h.WatchedNamespaces[nsName] {
			newNamespaces = append(newNamespaces, nsName)
		}
	}

	// Start watching new namespaces
	if len(newNamespaces) > 0 {
		fmt.Printf("   [INFO] ✨ Discovered %d new namespace(s) matching pattern '%s': [%s]\n",
			len(newNamespaces), h.NamespacePattern, strings.Join(newNamespaces, ", "))

		for _, nsName := range newNamespaces {
			// Mark as watched
			h.WatchedNamespaces[nsName] = true
			h.Namespaces = append(h.Namespaces, nsName)

			// Start watching this namespace
			go h.watchSingleNamespace(nsName)
			fmt.Printf("   [INFO] ✅ Started watching new namespace: %s\n", nsName)
		}
	}
}

// isTestNamespace checks if a namespace matches the test namespace pattern
func (h *Healer) isTestNamespace(namespace string) bool {
	// If namespace polling is enabled and pattern is set, check against pattern
	if h.EnableNamespacePolling && h.NamespacePattern != "" {
		matched, err := filepath.Match(h.NamespacePattern, namespace)
		if err == nil && matched {
			return true
		}
	}

	// Also check if namespace matches common test namespace patterns
	testPatterns := []string{"test-*", "e2e-*", "playwright-*", "gating-*"}
	for _, pattern := range testPatterns {
		matched, err := filepath.Match(pattern, namespace)
		if err == nil && matched {
			return true
		}
	}

	return false
}

// handleResourceCreation handles throttling warnings when resources are created during cluster strain
func (h *Healer) handleResourceCreation(resourceType, namespace, name string) {
	if !h.EnableResourceCreationThrottling {
		return
	}

	// Check if cluster is currently under strain
	if h.CurrentClusterStrain != nil && h.CurrentClusterStrain.HasStrain {
		// Check if this is a test namespace (test resources are allowed)
		isTestNamespace := h.isTestNamespace(namespace)
		
		if isTestNamespace {
			// Test namespace resources - warn but allow (tests need to run)
			fmt.Printf("   [WARN] ⚠️ New %s created in test namespace during cluster strain: %s/%s (strain: %.1f%%)\n",
				resourceType, namespace, name, h.CurrentClusterStrain.StrainPercentage)
			fmt.Printf("   [INFO] 💡 Consider reducing parallel test execution or waiting for cluster to recover\n")
		} else {
			// Non-test namespace resources - stronger warning
			fmt.Printf("   [WARN] 🚨 New %s created during cluster strain: %s/%s (strain: %.1f%%)\n",
				resourceType, namespace, name, h.CurrentClusterStrain.StrainPercentage)
			fmt.Printf("   [INFO] 💡 Resource creation throttling active - consider deferring resource creation until cluster recovers\n")
			fmt.Printf("   [INFO] 📊 Cluster status: %d/%d nodes under pressure: [%s]\n",
				h.CurrentClusterStrain.StrainedNodesCount, h.CurrentClusterStrain.TotalNodesCount,
				strings.Join(h.CurrentClusterStrain.NodesUnderPressure, ", "))
		}
	}
}
