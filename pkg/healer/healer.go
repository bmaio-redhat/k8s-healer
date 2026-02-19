package healer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bmaio-redhat/k8s-healer/pkg/healthcheck"
	"github.com/bmaio-redhat/k8s-healer/pkg/util"
	v1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// Healer holds the Kubernetes client and configuration for watching.
type Healer struct {
	ClientSet                        kubernetes.Interface
	DynamicClient                    dynamic.Interface
	Namespaces                       []string
	namespacesMu                     sync.RWMutex // Protects Namespaces slice
	StopCh                           chan struct{}
	HealedPods                       map[string]time.Time // Tracks recently healed pods
	healedPodsMu                     sync.RWMutex         // Protects HealedPods map
	HealedNodes                      map[string]time.Time // Tracks recently healed nodes
	healedNodesMu                    sync.RWMutex         // Protects HealedNodes map
	HealedVMs                        map[string]time.Time // Tracks recently healed VMs
	healedVMsMu                      sync.RWMutex         // Protects HealedVMs map
	HealedCRDs                       map[string]time.Time // Tracks recently cleaned CRDs
	healedCRDsMu                     sync.RWMutex         // Protects HealedCRDs map
	CRDCleanupCounts                 map[string]int      // Total CRD resources cleaned per type (e.g. "virtualmachines.kubevirt.io/v1")
	crdCleanupCountsMu               sync.RWMutex       // Protects CRDCleanupCounts
	TrackedCRDs                      map[string]bool     // Tracks all CRD resources we've seen (for creation logging)
	trackedCRDsMu                    sync.RWMutex        // Protects TrackedCRDs map
	HealCooldown                     time.Duration
	EnableVMHealing                  bool                    // Flag to enable VM healing
	EnableCRDCleanup                 bool                    // Flag to enable CRD cleanup
	CRDResources                     []string                // List of CRD resources to monitor (e.g., ["virtualmachines.virtualmachine.kubevirt.io"])
	StaleAge                         time.Duration           // Age threshold for stale resources
	CleanupFinalizers                bool                    // Whether to remove finalizers before deletion
	EnableResourceOptimization       bool                    // Flag to enable resource optimization during cluster strain
	StrainThreshold                  float64                 // Percentage of nodes under pressure to trigger optimization
	OptimizedPods                    map[string]time.Time    // Tracks recently optimized pods
	optimizedPodsMu                  sync.RWMutex            // Protects OptimizedPods map
	EnableNamespacePolling           bool                    // Flag to enable namespace polling
	NamespacePattern                 string                  // Pattern to match namespaces (e.g., "test-*")
	NamespacePollInterval            time.Duration           // How often to poll for new namespaces
	WatchedNamespaces                map[string]bool         // Tracks namespaces we're currently watching
	watchedNamespacesMu              sync.RWMutex            // Protects WatchedNamespaces map
	EnableResourceCreationThrottling bool                    // Flag to enable resource creation throttling during cluster strain
	CurrentClusterStrain             *util.ClusterStrainInfo // Current cluster strain state (updated by resource optimization)
	currentClusterStrainMu           sync.RWMutex            // Protects CurrentClusterStrain
	ExcludedNamespaces               []string                // Namespaces to exclude from prefix-based discovery

	// Memory limit: when heap exceeds MemoryLimitMB, references are sanitized and optionally process exits for restart
	MemoryLimitMB        uint64        // Heap limit in MB (0 = disabled)
	MemoryCheckInterval  time.Duration // How often to check memory (e.g. 1m)
	RestartOnMemoryLimit bool          // If true, exit process when over limit after sanitization (for process manager to restart)
	RestartRequested     chan struct{} // Closed when memory limit exceeded and restart requested; main should exit(0)
	restartRequestedFlag int32         // Atomic: 1 when RestartRequested was closed (so main can os.Exit(0))
	// MemoryReadFunc is optional; when set (e.g. in tests), checkMemory uses it instead of runtime.ReadMemStats
	MemoryReadFunc func() uint64
}

// forceDeleteOptions is used for all deletions so stale/terminating resources are removed immediately (no grace period).
var gracePeriodZero int64 = 0
var forceDeleteOptions = metav1.DeleteOptions{GracePeriodSeconds: &gracePeriodZero}

// NewHealer initializes the Kubernetes client configuration using kubeconfig or in-cluster settings.
func NewHealer(kubeconfigPath string, namespaces []string, enableVMHealing bool, enableCRDCleanup bool, crdResources []string, enableNamespacePolling bool, namespacePattern string, namespacePollInterval time.Duration, excludedNamespaces []string) (*Healer, error) {
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

	// Raise client rate limits to reduce "rate limiter Wait would exceed context deadline" under load.
	// Defaults are low; we do many parallel list/delete operations across namespaces.
	if config.QPS == 0 {
		config.QPS = 20
	}
	if config.Burst == 0 {
		config.Burst = 50
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

	// Initialize WatchedNamespaces map and mark initial namespaces as watched
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
		CRDCleanupCounts:                 make(map[string]int),
		TrackedCRDs:                      make(map[string]bool),
		HealCooldown:                     10 * time.Minute, // default cooldown
		EnableVMHealing:                  enableVMHealing,
		EnableCRDCleanup:                 enableCRDCleanup,
		CRDResources:                     crdResources,
		StaleAge:                         6 * time.Minute,                    // default stale age
		CleanupFinalizers:                true,                               // default to cleaning up finalizers
		EnableResourceOptimization:       true,                               // default to enabled
		StrainThreshold:                  util.DefaultClusterStrainThreshold, // default 30%
		OptimizedPods:                    make(map[string]time.Time),
		EnableNamespacePolling:           enableNamespacePolling,
		NamespacePattern:                 namespacePattern,
		NamespacePollInterval:            namespacePollInterval,
		WatchedNamespaces:                watchedNamespaces,
		EnableResourceCreationThrottling: true, // default to enabled
		CurrentClusterStrain:             nil,  // Will be updated by resource optimization checks
		ExcludedNamespaces:               excludedNamespaces,
		RestartRequested:                 make(chan struct{}), // closed when memory limit exceeded and restart requested
	}

	// Display cluster information after successful connection
	healer.DisplayClusterInfo(config, kubeconfigPath)

	return healer, nil
}

// Warmup validates cluster readiness before starting heavy operations
// It checks CRD availability and API connectivity to ensure the cluster is ready
// for concurrent resource creation (e.g., before automated tests begin)
func (h *Healer) Warmup(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	fmt.Printf("   [1/3] Validating Kubernetes API connectivity...\n")
	// Test basic API connectivity
	_, err := h.ClientSet.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return fmt.Errorf("failed to connect to Kubernetes API: %w", err)
	}
	fmt.Printf("   ✅ Kubernetes API is accessible\n")

	// If CRD cleanup is enabled, validate CRD resources are available
	if h.EnableCRDCleanup && len(h.CRDResources) > 0 {
		fmt.Printf("   [2/3] Validating CRD resource availability (%d resource types)...\n", len(h.CRDResources))

		var unavailableMu sync.Mutex
		var availableMu sync.Mutex
		unavailable := []string{}
		available := 0

		var wg sync.WaitGroup
		for _, crdResource := range h.CRDResources {
			wg.Add(1)
			go func(crdRes string) {
				defer wg.Done()
				// Parse the resource string (format: "resource.group/version" or "resource.group")
				parts := strings.Split(crdRes, ".")
				if len(parts) < 2 {
					return
				}

				resource := parts[0]
				groupVersion := strings.Join(parts[1:], ".")

				// Split group and version
				gvParts := strings.Split(groupVersion, "/")
				group := gvParts[0]
				version := "v1" // default
				if len(gvParts) > 1 {
					version = gvParts[1]
				}

				gvr := schema.GroupVersionResource{
					Group:    group,
					Version:  version,
					Resource: resource,
				}

				// Try to list resources (with limit 1 to minimize load)
				_, err := h.DynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
				if err != nil {
					if apierrors.IsNotFound(err) {
						unavailableMu.Lock()
						unavailable = append(unavailable, crdRes)
						unavailableMu.Unlock()
						return
					}
					// For other errors (like forbidden), we'll still consider it available
					// as the CRD exists, we just can't access it
				}
				availableMu.Lock()
				available++
				availableMu.Unlock()
			}(crdResource)
		}
		wg.Wait()

		if len(unavailable) > 0 {
			fmt.Printf("   ⚠️  %d/%d CRD resource types available\n", available, len(h.CRDResources))
			fmt.Printf("   ⚠️  Unavailable CRD resources: %s\n", strings.Join(unavailable, ", "))
			fmt.Printf("   💡 These may become available later - continuing anyway\n")
		} else {
			fmt.Printf("   ✅ All %d CRD resource types are available\n", len(h.CRDResources))
		}
	} else {
		fmt.Printf("   [2/3] Skipping CRD validation (CRD cleanup disabled or no resources specified)\n")
	}

	fmt.Printf("   [3/3] Performing final readiness check...\n")
	// Final connectivity check
	_, err = h.ClientSet.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return fmt.Errorf("failed final readiness check: %w", err)
	}
	fmt.Printf("   ✅ Cluster is ready for heavy operations\n")

	return nil
}

// DisplayClusterInfo displays a summary of the cluster being monitored
func (h *Healer) DisplayClusterInfo(config *rest.Config, kubeconfigPath string) {
	// Use fmt.Fprintf to ensure output goes to the correct stream (works in daemon mode)
	// In daemon mode, os.Stdout is redirected to the log file, so this will write to the log
	output := func(format string, args ...interface{}) {
		fmt.Fprintf(os.Stdout, format, args...)
		// Flush output immediately to ensure it's written to log file in daemon mode
		os.Stdout.Sync()
	}

	output("\n" + strings.Repeat("=", 70) + "\n")
	output("🔗 Connected to Kubernetes Cluster\n")
	output(strings.Repeat("=", 70) + "\n")

	// Get server version
	// Try to get RESTClient from the clientset (works with real clientset, may fail with fake)
	var serverVersion *version.Info
	var err error
	if realClientset, ok := h.ClientSet.(*kubernetes.Clientset); ok {
		discoveryClient := discovery.NewDiscoveryClient(realClientset.RESTClient())
		serverVersion, err = discoveryClient.ServerVersion()
	} else {
		// For fake clientset in tests, skip version check
		err = fmt.Errorf("cannot get server version from fake clientset")
	}

	if err == nil && serverVersion != nil {
		output("📦 Kubernetes Version: %s\n", serverVersion.GitVersion)
		output("   Platform: %s/%s\n", serverVersion.Platform, serverVersion.GoVersion)
	} else {
		output("📦 Kubernetes Version: Unable to retrieve (error: %v)\n", err)
	}

	// Get cluster host
	output("🌐 Cluster Host: %s\n", config.Host)

	// Get current context from kubeconfig if available
	if kubeconfigPath != "" {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		loadingRules.ExplicitPath = kubeconfigPath
		configOverrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
		if rawConfig, err := kubeConfig.RawConfig(); err == nil {
			if rawConfig.CurrentContext != "" {
				output("🔑 Current Context: %s\n", rawConfig.CurrentContext)
				if ctx, ok := rawConfig.Contexts[rawConfig.CurrentContext]; ok && ctx.Cluster != "" {
					output("   Cluster: %s\n", ctx.Cluster)
					if cluster, ok := rawConfig.Clusters[ctx.Cluster]; ok {
						output("   Server: %s\n", cluster.Server)
					}
				}
			}
		}
	}

	// Get node count
	nodes, err := h.ClientSet.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err == nil {
		readyNodes := 0
		for _, node := range nodes.Items {
			for _, condition := range node.Status.Conditions {
				if condition.Type == v1.NodeReady && condition.Status == v1.ConditionTrue {
					readyNodes++
					break
				}
			}
		}
		output("🖥️  Nodes: %d total (%d ready)\n", len(nodes.Items), readyNodes)
	} else {
		output("🖥️  Nodes: Unable to retrieve (error: %v)\n", err)
	}

	// Display namespaces being watched
	if len(h.Namespaces) == 0 || (len(h.Namespaces) == 1 && h.Namespaces[0] == metav1.NamespaceAll) {
		output("📁 Namespaces: All namespaces\n")
	} else {
		output("📁 Namespaces: %d namespace(s) - [%s]\n", len(h.Namespaces), strings.Join(h.Namespaces, ", "))
	}

	// Display enabled features
	output("\n⚙️  Enabled Features:\n")
	output("   • VM Healing: %s\n", formatBool(h.EnableVMHealing))
	output("   • CRD Cleanup: %s", formatBool(h.EnableCRDCleanup))
	if h.EnableCRDCleanup {
		output(" (%d resource types)", len(h.CRDResources))
	}
	output("\n")
	output("   • Resource Optimization: %s", formatBool(h.EnableResourceOptimization))
	if h.EnableResourceOptimization {
		output(" (threshold: %.1f%%)", h.StrainThreshold)
	}
	output("\n")
	output("   • Resource Creation Throttling: %s\n", formatBool(h.EnableResourceCreationThrottling))
	if h.EnableNamespacePolling {
		output("   • Namespace Polling: %s (pattern: %s, interval: %v)\n", formatBool(h.EnableNamespacePolling), h.NamespacePattern, h.NamespacePollInterval)
	} else {
		output("   • Namespace Polling: %s\n", formatBool(h.EnableNamespacePolling))
	}

	// Display configuration
	output("\n🔧 Configuration:\n")
	output("   • Stale Age Threshold: %v\n", h.StaleAge)
	output("   • Heal Cooldown: %v\n", h.HealCooldown)
	output("   • Cleanup Finalizers: %s\n", formatBool(h.CleanupFinalizers))

	output(strings.Repeat("=", 70) + "\n")

	// Perform pre-start health checks
	output("\n🔍 Performing Pre-Start Health Checks...\n")
	healthStatus, err := healthcheck.PerformClusterHealthCheck(h.ClientSet, h.DynamicClient, config)
	if err != nil {
		output("   [WARN] ⚠️ Failed to perform health checks: %v\n", err)
	} else {
		output(healthcheck.FormatHealthCheckStatus(healthStatus))
	}

	output(strings.Repeat("=", 70) + "\n")
	output("\n")
}

// formatBool returns a formatted string for boolean values
func formatBool(b bool) string {
	if b {
		return "✅ Enabled"
	}
	return "❌ Disabled"
}

// Watch starts the informer loop for all configured namespaces concurrently.
func (h *Healer) Watch() {
	// If no namespaces are provided, default to watching all namespaces
	h.namespacesMu.Lock()
	if len(h.Namespaces) == 0 {
		fmt.Println("No namespaces specified. Watching all namespaces (using NamespaceAll).")
		h.Namespaces = []string{metav1.NamespaceAll}
	}
	namespacesCopy := make([]string, len(h.Namespaces))
	copy(namespacesCopy, h.Namespaces)
	h.namespacesMu.Unlock()

	fmt.Printf("Starting healer to watch namespaces: [%s]\n", strings.Join(namespacesCopy, ", "))
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
	h.startMemoryGuard()

	// Start a separate goroutine for the informer watch in each namespace
	for _, ns := range namespacesCopy {
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

	// Start namespace polling if enabled and we have patterns (either explicit or from namespaces)
	if h.EnableNamespacePolling {
		hasPattern := h.NamespacePattern != ""
		hasWildcardInNamespaces := false
		for _, ns := range h.Namespaces {
			if strings.Contains(ns, "*") {
				hasWildcardInNamespaces = true
				break
			}
		}
		if hasPattern || hasWildcardInNamespaces {
			go h.pollForNewNamespaces()
		}
	}

	// Block until shutdown (SIGINT/SIGTERM) or memory-limit restart requested
	select {
	case <-h.StopCh:
	case <-h.RestartRequested:
	}
}

// watchSingleNamespace sets up a Pod Informer for one namespace.
func (h *Healer) watchSingleNamespace(namespace string) {
	// Create a SharedInformerFactory scoped to the namespace, with a 5m resync period
	// Increased from 30s to reduce memory churn and improve performance
	factory := informers.NewSharedInformerFactoryWithOptions(h.ClientSet, 5*time.Minute, informers.WithNamespace(namespace))

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
	h.healedPodsMu.RLock()
	lastHeal, recentlyHealed := h.HealedPods[podKey]
	h.healedPodsMu.RUnlock()
	if recentlyHealed {
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
		h.healedPodsMu.Lock()
		h.HealedPods[podKey] = time.Now()
		h.healedPodsMu.Unlock()

		fmt.Printf("!!! HEALING ACTION COMPLETE !!!\n\n")
	}
}

func (h *Healer) startHealCacheCleaner() {
	// Run cleanup every 15 minutes to prevent unbounded map growth
	ticker := time.NewTicker(15 * time.Minute)
	go func() {
		for {
			select {
			case <-ticker.C:
				now := time.Now()
				// Clean up healed pods
				h.healedPodsMu.Lock()
				for key, t := range h.HealedPods {
					if now.Sub(t) > 2*h.HealCooldown {
						delete(h.HealedPods, key)
					}
				}
				h.healedPodsMu.Unlock()
				// Clean up healed nodes
				h.healedNodesMu.Lock()
				for key, t := range h.HealedNodes {
					if now.Sub(t) > 2*h.HealCooldown {
						delete(h.HealedNodes, key)
					}
				}
				h.healedNodesMu.Unlock()
				// Clean up healed VMs
				h.healedVMsMu.Lock()
				for key, t := range h.HealedVMs {
					if now.Sub(t) > 2*h.HealCooldown {
						delete(h.HealedVMs, key)
					}
				}
				h.healedVMsMu.Unlock()
				// Clean up healed CRDs
				h.healedCRDsMu.Lock()
				for key, t := range h.HealedCRDs {
					if now.Sub(t) > 2*h.HealCooldown {
						delete(h.HealedCRDs, key)
					}
				}
				h.healedCRDsMu.Unlock()
				// Clean up optimized pods
				h.optimizedPodsMu.Lock()
				for key, t := range h.OptimizedPods {
					if now.Sub(t) > 2*h.HealCooldown {
						delete(h.OptimizedPods, key)
					}
				}
				h.optimizedPodsMu.Unlock()
				// Limit TrackedCRDs map size to prevent unbounded growth
				// If map exceeds 5000 entries, remove 30% of entries (more aggressive cleanup)
				// This prevents memory from growing unbounded in long-running processes
				// Note: The periodic cleanup in checkCRDResources will also remove entries for
				// resources that no longer exist, but this provides a safety net
				h.trackedCRDsMu.Lock()
				if len(h.TrackedCRDs) > 5000 {
					removed := 0
					targetRemoval := len(h.TrackedCRDs) * 3 / 10 // Remove 30%
					for key := range h.TrackedCRDs {
						if removed >= targetRemoval {
							break
						}
						delete(h.TrackedCRDs, key)
						removed++
					}
					if removed > 0 {
						fmt.Printf("   [INFO] 🧹 Cleaned up %d old CRD resource reference(s) to prevent memory growth (map size: %d)\n",
							removed, len(h.TrackedCRDs))
					}
				}
				h.trackedCRDsMu.Unlock()
			case <-h.StopCh:
				ticker.Stop()
				return
			}
		}
	}()
}

// SanitizeReferences clears all in-memory tracking maps to free memory. Call when approaching memory limits.
// Monitoring continues; only historical references (healed/tracked resources) are cleared.
func (h *Healer) SanitizeReferences() {
	h.healedPodsMu.Lock()
	h.HealedPods = make(map[string]time.Time)
	h.healedPodsMu.Unlock()

	h.healedNodesMu.Lock()
	h.HealedNodes = make(map[string]time.Time)
	h.healedNodesMu.Unlock()

	h.healedVMsMu.Lock()
	h.HealedVMs = make(map[string]time.Time)
	h.healedVMsMu.Unlock()

	h.healedCRDsMu.Lock()
	h.HealedCRDs = make(map[string]time.Time)
	h.healedCRDsMu.Unlock()

	h.trackedCRDsMu.Lock()
	h.TrackedCRDs = make(map[string]bool)
	h.trackedCRDsMu.Unlock()

	h.optimizedPodsMu.Lock()
	h.OptimizedPods = make(map[string]time.Time)
	h.optimizedPodsMu.Unlock()

	h.currentClusterStrainMu.Lock()
	h.CurrentClusterStrain = nil
	h.currentClusterStrainMu.Unlock()

	runtime.GC()
	fmt.Printf("   [INFO] 🧹 Sanitized all tracking references and ran GC\n")
}

// checkMemory reads current heap (or uses MemoryReadFunc if set), and if over MemoryLimitMB
// runs SanitizeReferences; if still over limit and RestartOnMemoryLimit, closes RestartRequested.
// Used by startMemoryGuard; also callable from tests when MemoryReadFunc is set.
func (h *Healer) checkMemory() {
	if h.MemoryLimitMB == 0 {
		return
	}
	limitBytes := h.MemoryLimitMB * 1024 * 1024
	var heapAlloc uint64
	if h.MemoryReadFunc != nil {
		heapAlloc = h.MemoryReadFunc()
	} else {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		heapAlloc = m.HeapAlloc
	}
	if heapAlloc <= limitBytes {
		return
	}
	fmt.Printf("   [WARN] ⚠️ Memory limit exceeded: heap %.1f MB > limit %d MB — sanitizing references\n",
		float64(heapAlloc)/(1024*1024), h.MemoryLimitMB)
	h.SanitizeReferences()
	if h.MemoryReadFunc != nil {
		heapAlloc = h.MemoryReadFunc()
	} else {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		heapAlloc = m.HeapAlloc
	}
	if heapAlloc <= limitBytes {
		fmt.Printf("   [INFO] ✅ Memory below limit after sanitization (%.1f MB)\n", float64(heapAlloc)/(1024*1024))
		return
	}
	fmt.Printf("   [WARN] ⚠️ Memory still above limit after sanitization: %.1f MB — requesting restart\n",
		float64(heapAlloc)/(1024*1024))
	if h.RestartOnMemoryLimit && h.RestartRequested != nil {
		if atomic.CompareAndSwapInt32(&h.restartRequestedFlag, 0, 1) {
			close(h.RestartRequested)
		}
	}
}

// startMemoryGuard runs a goroutine that periodically calls checkMemory.
func (h *Healer) startMemoryGuard() {
	if h.MemoryLimitMB == 0 {
		return
	}
	interval := h.MemoryCheckInterval
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				h.checkMemory()
			case <-h.StopCh:
				return
			}
		}
	}()
}

// IsRestartRequested returns true if the memory guard has requested a process restart (main should exit(0)).
func (h *Healer) IsRestartRequested() bool {
	return atomic.LoadInt32(&h.restartRequestedFlag) == 1
}

// triggerPodDeletion deletes the Pod, relying on the managing controller to recreate a fresh one.
func (h *Healer) triggerPodDeletion(pod *v1.Pod) {
	// Use a context with timeout for the API call to prevent indefinite hangs
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	// Perform the API Delete call (force delete: no grace period, so terminating pods are removed immediately)
	err := h.ClientSet.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, forceDeleteOptions)

	if err != nil {
		fmt.Printf("   [FAIL] ❌ Failed to delete pod %s/%s: %v\n", pod.Namespace, pod.Name, err)
	} else {
		fmt.Printf("   [SUCCESS] ✅ Deleted pod %s/%s. Controller is expected to recreate the Pod immediately.\n", pod.Namespace, pod.Name)
	}
}

// watchNodes sets up a Node Informer to monitor node health
func (h *Healer) watchNodes() {
	// Create a SharedInformerFactory for nodes (cluster-scoped)
	// Increased resync period to 5m to reduce memory churn
	factory := informers.NewSharedInformerFactory(h.ClientSet, 5*time.Minute)

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
	h.healedNodesMu.RLock()
	lastHeal, recentlyHealed := h.HealedNodes[nodeKey]
	h.healedNodesMu.RUnlock()
	if recentlyHealed {
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
		h.healedNodesMu.Lock()
		h.HealedNodes[nodeKey] = time.Now()
		h.healedNodesMu.Unlock()

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

	// Delete the node (force delete: no grace period)
	err = h.ClientSet.CoreV1().Nodes().Delete(ctx, node.Name, forceDeleteOptions)
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

	// Evict each pod in parallel
	var wg sync.WaitGroup
	for _, pod := range pods.Items {
		// Skip pods without owner references (static pods)
		if len(pod.OwnerReferences) == 0 {
			continue
		}

		// Skip DaemonSet pods
		skipPod := false
		for _, ownerRef := range pod.OwnerReferences {
			if ownerRef.Kind == "DaemonSet" {
				skipPod = true
				break
			}
		}
		if skipPod {
			continue
		}

		// Evict the pod using the eviction API (in parallel)
		wg.Add(1)
		go func(p v1.Pod) {
			defer wg.Done()
			err := h.ClientSet.CoreV1().Pods(p.Namespace).EvictV1(ctx, &policyv1.Eviction{
				ObjectMeta: metav1.ObjectMeta{
					Name:      p.Name,
					Namespace: p.Namespace,
				},
			})
			if err != nil {
				fmt.Printf("   [WARN] ⚠️ Failed to evict pod %s/%s: %v\n", p.Namespace, p.Name, err)
			} else {
				fmt.Printf("   [INFO] ✅ Evicted pod %s/%s from node %s\n", p.Namespace, p.Name, nodeName)
			}
		}(pod)
	}
	wg.Wait()

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

	h.namespacesMu.RLock()
	namespaces := make([]string, len(h.Namespaces))
	copy(namespaces, h.Namespaces)
	h.namespacesMu.RUnlock()

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

	// Process namespaces in parallel
	var wg sync.WaitGroup
	for _, ns := range namespaces {
		wg.Add(1)
		go func(namespace string) {
			defer wg.Done()
			vms, err := h.DynamicClient.Resource(vmGVR).Namespace(namespace).List(context.TODO(), metav1.ListOptions{})
			if err != nil {
				// Skip if VirtualMachine CRD is not available
				return
			}

			for _, vmUnstructured := range vms.Items {
				h.checkAndHealVirtualMachine(&vmUnstructured)
			}
		}(ns)
	}
	wg.Wait()
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
	h.healedVMsMu.RLock()
	lastHeal, recentlyHealed := h.HealedVMs[vmKey]
	h.healedVMsMu.RUnlock()
	if recentlyHealed {
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
		h.healedVMsMu.Lock()
		h.HealedVMs[vmKey] = time.Now()
		h.healedVMsMu.Unlock()

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

	// Delete the VirtualMachine (force delete: no grace period, so terminating VMs are removed immediately)
	err := h.DynamicClient.Resource(vmGVR).Namespace(vm.GetNamespace()).Delete(ctx, vm.GetName(), forceDeleteOptions)
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

	h.namespacesMu.RLock()
	namespaces := make([]string, len(h.Namespaces))
	copy(namespaces, h.Namespaces)
	h.namespacesMu.RUnlock()

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

	// Process namespaces in parallel and collect existing resources
	var wg sync.WaitGroup
	var existingResourcesMu sync.Mutex
	existingResources := make(map[string]bool) // Track resources that currently exist

	for _, ns := range namespaces {
		wg.Add(1)
		go func(namespace string) {
			defer wg.Done()
			resources, err := h.DynamicClient.Resource(gvr).Namespace(namespace).List(context.TODO(), metav1.ListOptions{})
			if err != nil {
				// Skip if CRD is not available or not accessible
				return
			}

			for _, resourceUnstructured := range resources.Items {
				resourceKey := fmt.Sprintf("%s/%s/%s", gvr.Resource, resourceUnstructured.GetNamespace(), resourceUnstructured.GetName())
				// Mark as existing
				existingResourcesMu.Lock()
				existingResources[resourceKey] = true
				existingResourcesMu.Unlock()

				// Log CRD creation if it's a new resource we haven't seen before
				h.logCRDCreation(&resourceUnstructured, gvr)
				// Check if resource is stale and needs cleanup
				h.checkAndCleanupCRDResource(&resourceUnstructured, gvr)
			}
		}(ns)
	}
	wg.Wait()

	// Clean up TrackedCRDs entries for resources that no longer exist
	// Only check resources for this specific GVR (format: "resource/namespace/name")
	h.trackedCRDsMu.Lock()
	removedCount := 0
	for key := range h.TrackedCRDs {
		// Check if this key matches the current GVR format
		// Format: "resource/namespace/name"
		parts := strings.Split(key, "/")
		if len(parts) == 3 && parts[0] == gvr.Resource {
			// This is a resource of the type we just checked
			if !existingResources[key] {
				// Resource no longer exists, remove from tracking
				delete(h.TrackedCRDs, key)
				removedCount++
			}
		}
	}
	h.trackedCRDsMu.Unlock()

	if removedCount > 0 {
		fmt.Printf("   [INFO] 🧹 Cleaned up %d stale CRD resource reference(s) for %s/%s/%s (resources no longer exist)\n",
			removedCount, gvr.Group, gvr.Version, gvr.Resource)
	}
}

// logCRDCreation logs when a new CRD resource is created
func (h *Healer) logCRDCreation(resource *unstructured.Unstructured, gvr schema.GroupVersionResource) {
	resourceKey := fmt.Sprintf("%s/%s/%s", gvr.Resource, resource.GetNamespace(), resource.GetName())

	// Check if we've seen this resource before
	h.trackedCRDsMu.RLock()
	alreadyTracked := h.TrackedCRDs[resourceKey]
	h.trackedCRDsMu.RUnlock()

	if !alreadyTracked {
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
		h.trackedCRDsMu.Lock()
		h.TrackedCRDs[resourceKey] = true
		h.trackedCRDsMu.Unlock()
	}
}

// checkAndCleanupCRDResource checks if a CRD resource is stale and cleans it up if needed
func (h *Healer) checkAndCleanupCRDResource(resource *unstructured.Unstructured, gvr schema.GroupVersionResource) {
	// Skip if recently cleaned
	resourceKey := fmt.Sprintf("%s/%s/%s", gvr.Resource, resource.GetNamespace(), resource.GetName())
	h.healedCRDsMu.RLock()
	lastClean, recentlyCleaned := h.HealedCRDs[resourceKey]
	h.healedCRDsMu.RUnlock()
	if recentlyCleaned {
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
			h.healedCRDsMu.Lock()
			h.HealedCRDs[resourceKey] = time.Now()
			h.healedCRDsMu.Unlock()

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
		h.healedCRDsMu.Lock()
		h.HealedCRDs[resourceKey] = time.Now()
		h.healedCRDsMu.Unlock()

		fmt.Printf("!!! CRD CLEANUP ACTION COMPLETE !!!\n\n")
	}
}

// isRateLimitError returns true if the error is from the client rate limiter (e.g. "would exceed context deadline").
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "rate") && (strings.Contains(s, "Wait") || strings.Contains(s, "limiter") || strings.Contains(s, "would exceed context deadline"))
}

// triggerCRDCleanup performs cleanup actions on a stale CRD resource. Uses a longer timeout and retries
// on rate-limit errors so we don't fail when the cluster rate limiter is under load.
func (h *Healer) triggerCRDCleanup(resource *unstructured.Unstructured, gvr schema.GroupVersionResource) {
	resourceName := resource.GetName()
	resourceNamespace := resource.GetNamespace()
	resourceKey := fmt.Sprintf("%s/%s/%s", gvr.Resource, resourceNamespace, resourceName)

	// Short delay to spread load when many cleanups run in parallel (reduces rate limit spikes).
	time.Sleep(100 * time.Millisecond)

	const maxRetries = 3
	backoff := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Use a long timeout so rate limiter Wait has time to complete (default 30s was too short under load).
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		done, err := h.doCRDCleanup(ctx, resource, gvr, resourceName, resourceNamespace, resourceKey)
		cancel()
		if done {
			return
		}
		if err != nil && isRateLimitError(err) && attempt < maxRetries-1 {
			fmt.Printf("   [WARN] ⚠️ Rate limited, retrying in %v (attempt %d/%d): %v\n", backoff[attempt], attempt+1, maxRetries, err)
			time.Sleep(backoff[attempt])
			continue
		}
		if err != nil {
			fmt.Printf("   [FAIL] ❌ Failed to delete %s/%s/%s after %d attempt(s): %v\n", gvr.Resource, resourceNamespace, resourceName, attempt+1, err)
		}
		return
	}
}

// doCRDCleanup runs one attempt of CRD cleanup. Returns (true, nil) when done (success or skip), (false, err) when a retriable error occurred.
func (h *Healer) doCRDCleanup(ctx context.Context, resource *unstructured.Unstructured, gvr schema.GroupVersionResource, resourceName, resourceNamespace, resourceKey string) (done bool, err error) {
	// Remove finalizers when: cleanup is enabled, or resource is already terminating (force-delete stuck resources)
	forceFinalizerRemoval := len(resource.GetFinalizers()) > 0 && (h.CleanupFinalizers || resource.GetDeletionTimestamp() != nil)
	if forceFinalizerRemoval {
		fmt.Printf("   [INFO] 🔄 Removing finalizers from %s/%s/%s...\n", gvr.Resource, resourceNamespace, resourceName)

		currentResource, getErr := h.DynamicClient.Resource(gvr).Namespace(resourceNamespace).Get(ctx, resourceName, metav1.GetOptions{})
		if getErr != nil {
			if apierrors.IsNotFound(getErr) {
				fmt.Printf("   [SKIP] ⏭️ Resource %s/%s/%s no longer exists, skipping finalizer removal\n", gvr.Resource, resourceNamespace, resourceName)
				h.trackedCRDsMu.Lock()
				delete(h.TrackedCRDs, resourceKey)
				h.trackedCRDsMu.Unlock()
				return true, nil
			}
			fmt.Printf("   [WARN] ⚠️ Failed to get resource %s/%s/%s for finalizer removal: %v\n", gvr.Resource, resourceNamespace, resourceName, getErr)
			fmt.Printf("   [INFO] 🔄 Proceeding with deletion anyway...\n")
			if isRateLimitError(getErr) {
				return false, getErr
			}
		} else {
			currentResource.SetFinalizers([]string{})
			_, updateErr := h.DynamicClient.Resource(gvr).Namespace(resourceNamespace).Update(ctx, currentResource, metav1.UpdateOptions{})
			if updateErr != nil {
				if apierrors.IsNotFound(updateErr) {
					fmt.Printf("   [SKIP] ⏭️ Resource %s/%s/%s was deleted during finalizer removal\n", gvr.Resource, resourceNamespace, resourceName)
					h.trackedCRDsMu.Lock()
					delete(h.TrackedCRDs, resourceKey)
					h.trackedCRDsMu.Unlock()
					return true, nil
				}
				fmt.Printf("   [WARN] ⚠️ Failed to remove finalizers from %s/%s/%s: %v\n", gvr.Resource, resourceNamespace, resourceName, updateErr)
				fmt.Printf("   [INFO] 🔄 Proceeding with deletion anyway...\n")
				if isRateLimitError(updateErr) {
					return false, updateErr
				}
			} else {
				fmt.Printf("   [SUCCESS] ✅ Removed finalizers from %s/%s/%s\n", gvr.Resource, resourceNamespace, resourceName)
			}
		}
	}

	// Check if resource still exists before attempting deletion
	_, getErr := h.DynamicClient.Resource(gvr).Namespace(resourceNamespace).Get(ctx, resourceName, metav1.GetOptions{})
	if getErr != nil {
		if apierrors.IsNotFound(getErr) {
			fmt.Printf("   [SKIP] ⏭️ Resource %s/%s/%s no longer exists, skipping deletion\n", gvr.Resource, resourceNamespace, resourceName)
			h.trackedCRDsMu.Lock()
			delete(h.TrackedCRDs, resourceKey)
			h.trackedCRDsMu.Unlock()
			return true, nil
		}
		fmt.Printf("   [WARN] ⚠️ Failed to check if resource %s/%s/%s exists: %v\n", gvr.Resource, resourceNamespace, resourceName, getErr)
		if isRateLimitError(getErr) {
			return false, getErr
		}
	}

	// Delete the resource (force delete: no grace period, so terminating/stale resources are removed immediately)
	delErr := h.DynamicClient.Resource(gvr).Namespace(resourceNamespace).Delete(ctx, resourceName, forceDeleteOptions)
	if delErr != nil {
		if apierrors.IsNotFound(delErr) {
			fmt.Printf("   [SKIP] ⏭️ Resource %s/%s/%s was already deleted\n", gvr.Resource, resourceNamespace, resourceName)
			h.trackedCRDsMu.Lock()
			delete(h.TrackedCRDs, resourceKey)
			h.trackedCRDsMu.Unlock()
			return true, nil
		}
		if isRateLimitError(delErr) {
			return false, delErr
		}
		fmt.Printf("   [FAIL] ❌ Failed to delete %s/%s/%s: %v\n", gvr.Resource, resourceNamespace, resourceName, delErr)
		return true, nil
	}
	fmt.Printf("   [SUCCESS] ✅ Deleted stale resource %s/%s/%s\n", gvr.Resource, resourceNamespace, resourceName)
	h.RecordCRDCleanup(gvr)
	h.trackedCRDsMu.Lock()
	delete(h.TrackedCRDs, resourceKey)
	h.trackedCRDsMu.Unlock()
	return true, nil
}

// RecordCRDCleanup increments the cleanup count for the given GVR (for summary reporting).
func (h *Healer) RecordCRDCleanup(gvr schema.GroupVersionResource) {
	key := fmt.Sprintf("%s.%s/%s", gvr.Resource, gvr.Group, gvr.Version)
	h.crdCleanupCountsMu.Lock()
	defer h.crdCleanupCountsMu.Unlock()
	if h.CRDCleanupCounts == nil {
		h.CRDCleanupCounts = make(map[string]int)
	}
	h.CRDCleanupCounts[key]++
}

// RunFullCRDCleanup runs cleanup for all registered CRD resource types once (all watched namespaces).
// Can be triggered by signal (e.g. SIGUSR1) to finish cleaning pending resources on demand.
func (h *Healer) RunFullCRDCleanup() {
	if !h.EnableCRDCleanup || len(h.CRDResources) == 0 {
		fmt.Printf("   [INFO] CRD cleanup is disabled or no CRD resources configured.\n")
		return
	}
	fmt.Printf("   [INFO] 🔄 Running full CRD cleanup for all %d resource types...\n", len(h.CRDResources))
	var wg sync.WaitGroup
	for _, crdResource := range h.CRDResources {
		parts := strings.Split(crdResource, "/")
		resourceAndGroup := parts[0]
		version := ""
		if len(parts) > 1 {
			version = parts[1]
		}
		resourceParts := strings.Split(resourceAndGroup, ".")
		if len(resourceParts) < 2 {
			continue
		}
		resource := resourceParts[0]
		group := strings.Join(resourceParts[1:], ".")
		if version == "" {
			version = "v1"
		}
		wg.Add(1)
		go func(g, v, r string) {
			defer wg.Done()
			h.checkCRDResources(g, v, r)
		}(group, version, resource)
	}
	wg.Wait()
	fmt.Printf("   [INFO] ✅ Full CRD cleanup completed.\n")
}

// PrintCRDCleanupSummary prints how many CRD resources were cleaned up per type since start to stdout.
func (h *Healer) PrintCRDCleanupSummary() {
	h.PrintCRDCleanupSummaryTo(os.Stdout)
}

// PrintCRDCleanupSummaryTo writes the CRD cleanup summary (counts per type and total) to w.
func (h *Healer) PrintCRDCleanupSummaryTo(w io.Writer) {
	h.crdCleanupCountsMu.RLock()
	defer h.crdCleanupCountsMu.RUnlock()
	if len(h.CRDCleanupCounts) == 0 {
		fmt.Fprintf(w, "CRD cleanup summary: 0 resources cleaned (no cleanups yet).\n")
		return
	}
	total := 0
	keys := make([]string, 0, len(h.CRDCleanupCounts))
	for k := range h.CRDCleanupCounts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(w, "CRD cleanup summary:\n")
	for _, key := range keys {
		c := h.CRDCleanupCounts[key]
		total += c
		fmt.Fprintf(w, "  %s: %d\n", key, c)
	}
	fmt.Fprintf(w, "Total: %d resources cleaned\n", total)
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
	h.currentClusterStrainMu.Lock()
	h.CurrentClusterStrain = &strainInfo
	h.currentClusterStrainMu.Unlock()

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
	var testNamespacePodsMu sync.Mutex
	var nonTestPodsMu sync.Mutex
	testNamespacePods := []*v1.Pod{}
	nonTestPods := []*v1.Pod{}

	// Check all namespaces to find test pods and non-test pods to evict (in parallel)
	var wg sync.WaitGroup
	for _, ns := range allNamespaces {
		wg.Add(1)
		go func(namespace string) {
			defer wg.Done()
			pods, err := h.ClientSet.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{})
			if err != nil {
				return
			}

			for i := range pods.Items {
				pod := &pods.Items[i]

				// Skip unmanaged pods
				if len(pod.OwnerReferences) == 0 {
					continue
				}

				// Skip if recently optimized
				podKey := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
				h.optimizedPodsMu.RLock()
				lastOpt, recentlyOptimized := h.OptimizedPods[podKey]
				h.optimizedPodsMu.RUnlock()
				if recentlyOptimized {
					if time.Since(lastOpt) < h.HealCooldown {
						continue
					}
				}

				// Check if this is a test namespace pod
				isTestNamespace := h.isTestNamespace(pod.Namespace)

				if isTestNamespace {
					// Track test namespace pods (we'll check if they need resources)
					// Always include test pods to monitor their resource needs
					testNamespacePodsMu.Lock()
					testNamespacePods = append(testNamespacePods, pod)
					testNamespacePodsMu.Unlock()
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
						nonTestPodsMu.Lock()
						nonTestPods = append(nonTestPods, pod)
						nonTestPodsMu.Unlock()
					}
				}
			}
		}(ns)
	}
	wg.Wait()

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
		DeleteOptions: &forceDeleteOptions,
	})

	if err != nil {
		fmt.Printf("   [FAIL] ❌ Failed to evict pod %s: %v\n", podKey, err)
		// Fallback to direct deletion if eviction fails
		err = h.ClientSet.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, forceDeleteOptions)
		if err != nil {
			fmt.Printf("   [FAIL] ❌ Failed to delete pod %s: %v\n", podKey, err)
		} else {
			fmt.Printf("   [SUCCESS] ✅ Deleted pod %s for resource optimization\n", podKey)
			h.optimizedPodsMu.Lock()
			h.OptimizedPods[podKey] = time.Now()
			h.optimizedPodsMu.Unlock()
		}
	} else {
		fmt.Printf("   [SUCCESS] ✅ Evicted pod %s for resource optimization\n", podKey)
		h.optimizedPodsMu.Lock()
		h.OptimizedPods[podKey] = time.Now()
		h.optimizedPodsMu.Unlock()
	}

	fmt.Printf("!!! RESOURCE OPTIMIZATION ACTION COMPLETE !!!\n\n")
}

// pollForNewNamespaces periodically checks for new namespaces matching the pattern
func (h *Healer) pollForNewNamespaces() {
	if h.NamespacePollInterval == 0 {
		h.NamespacePollInterval = 5 * time.Second // Default poll interval
	}

	ticker := time.NewTicker(h.NamespacePollInterval)
	defer ticker.Stop()

	// Determine pattern display
	patternDisplay := h.NamespacePattern
	if patternDisplay == "" {
		// Extract wildcard patterns from Namespaces
		wildcardPatterns := []string{}
		for _, ns := range h.Namespaces {
			if strings.Contains(ns, "*") {
				wildcardPatterns = append(wildcardPatterns, ns)
			}
		}
		if len(wildcardPatterns) > 0 {
			patternDisplay = strings.Join(wildcardPatterns, ",")
		} else {
			patternDisplay = "(derived from --namespaces)"
		}
	}
	fmt.Printf("   [INFO] 🔍 Starting namespace polling for pattern: %s (interval: %v)\n", patternDisplay, h.NamespacePollInterval)

	for {
		select {
		case <-ticker.C:
			h.discoverNewNamespaces()
		case <-h.StopCh:
			return
		}
	}
}

// extractPrefixesFromNamespaces extracts prefixes from namespace names for prefix-based discovery.
// For example, "test-123" -> "test-", "e2e-456" -> "e2e-".
// Returns a map of prefix -> true to avoid duplicates.
func (h *Healer) extractPrefixesFromNamespaces() map[string]bool {
	prefixes := make(map[string]bool)

	for _, ns := range h.Namespaces {
		// Skip wildcard patterns (they're handled separately)
		if strings.Contains(ns, "*") {
			continue
		}

		// Extract prefix: find the last separator (hyphen, underscore, or dot)
		// and use everything before it as the prefix
		lastHyphen := strings.LastIndex(ns, "-")
		lastUnderscore := strings.LastIndex(ns, "_")
		lastDot := strings.LastIndex(ns, ".")

		lastSeparator := -1
		if lastHyphen > lastSeparator {
			lastSeparator = lastHyphen
		}
		if lastUnderscore > lastSeparator {
			lastSeparator = lastUnderscore
		}
		if lastDot > lastSeparator {
			lastSeparator = lastDot
		}

		// If we found a separator, extract the prefix
		if lastSeparator > 0 && lastSeparator < len(ns)-1 {
			prefix := ns[:lastSeparator+1] // Include the separator
			prefixes[prefix] = true
		}
	}

	return prefixes
}

// isExcludedNamespace checks if a namespace is in the exclusion list
func (h *Healer) isExcludedNamespace(namespace string) bool {
	for _, excluded := range h.ExcludedNamespaces {
		if excluded == namespace {
			return true
		}
	}
	return false
}

// discoverNewNamespaces discovers new namespaces matching the pattern and starts watching them.
// It supports both wildcard patterns and prefix-based discovery.
func (h *Healer) discoverNewNamespaces() {
	// List all namespaces
	nsList, err := h.ClientSet.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		fmt.Printf("   [ERROR] ❌ Error listing namespaces for polling: %v\n", err)
		return
	}

	// Extract patterns from Namespaces (support multiple patterns like "test-*,e2e-*")
	patterns := []string{}
	if h.NamespacePattern != "" {
		// Use explicit pattern if set
		patterns = []string{h.NamespacePattern}
	} else {
		// Extract wildcard patterns from Namespaces list
		for _, ns := range h.Namespaces {
			if strings.Contains(ns, "*") {
				patterns = append(patterns, ns)
			}
		}
	}

	// Extract prefixes from non-wildcard namespaces for prefix-based discovery
	prefixes := h.extractPrefixesFromNamespaces()

	// If we have neither patterns nor prefixes, nothing to discover
	if len(patterns) == 0 && len(prefixes) == 0 {
		return
	}

	// Match namespaces against all patterns and prefixes
	// Note: We still discover excluded namespaces (they will be monitored but not deleted)
	matchedNamespaces := make(map[string]bool)
	for _, ns := range nsList.Items {
		// Check against wildcard patterns
		for _, pattern := range patterns {
			matched, err := filepath.Match(pattern, ns.Name)
			if err != nil {
				fmt.Printf("   [WARN] ⚠️ Invalid namespace pattern '%s': %v\n", pattern, err)
				continue
			}
			if matched {
				matchedNamespaces[ns.Name] = true
				break // Found a match, no need to check other patterns
			}
		}

		// Check against prefixes (if not already matched)
		if !matchedNamespaces[ns.Name] {
			for prefix := range prefixes {
				if strings.HasPrefix(ns.Name, prefix) {
					matchedNamespaces[ns.Name] = true
					break // Found a match, no need to check other prefixes
				}
			}
		}
	}

	// Check for new namespaces we're not watching yet
	newNamespaces := []string{}
	h.watchedNamespacesMu.RLock()
	for nsName := range matchedNamespaces {
		if !h.WatchedNamespaces[nsName] {
			newNamespaces = append(newNamespaces, nsName)
		}
	}
	h.watchedNamespacesMu.RUnlock()

	// Check for deleted namespaces that we're still watching
	// Build a set of existing namespace names for quick lookup
	existingNamespaces := make(map[string]bool)
	for _, ns := range nsList.Items {
		existingNamespaces[ns.Name] = true
	}

	// Find namespaces we're watching that no longer exist and match the pattern
	// We only remove namespaces that:
	// 1. Match the pattern (wildcard or prefix) - meaning they were dynamically discovered
	// 2. No longer exist in the cluster
	deletedNamespaces := []string{}
	h.watchedNamespacesMu.RLock()
	watchedNamespacesCopy := make(map[string]bool)
	for k, v := range h.WatchedNamespaces {
		watchedNamespacesCopy[k] = v
	}
	h.watchedNamespacesMu.RUnlock()
	for watchedNs := range watchedNamespacesCopy {
		// Skip if namespace still exists
		if existingNamespaces[watchedNs] {
			continue
		}

		// Check if it matches any pattern (wildcard)
		matchesPattern := false
		for _, pattern := range patterns {
			matched, err := filepath.Match(pattern, watchedNs)
			if err == nil && matched {
				matchesPattern = true
				break
			}
		}

		// Check if it matches any prefix (if not already matched by wildcard)
		if !matchesPattern {
			for prefix := range prefixes {
				if strings.HasPrefix(watchedNs, prefix) {
					matchesPattern = true
					break
				}
			}
		}

		// Only remove if it matches the pattern (was dynamically discovered)
		// Namespaces that don't match the pattern were explicitly specified and should be kept
		if matchesPattern {
			deletedNamespaces = append(deletedNamespaces, watchedNs)
		}
	}

	// Remove deleted namespaces from tracking
	if len(deletedNamespaces) > 0 {
		fmt.Printf("   [INFO] 🗑️  Detected %d deleted namespace(s) matching pattern: [%s]\n",
			len(deletedNamespaces), strings.Join(deletedNamespaces, ", "))

		for _, nsName := range deletedNamespaces {
			// Remove from watched namespaces map
			h.watchedNamespacesMu.Lock()
			delete(h.WatchedNamespaces, nsName)
			h.watchedNamespacesMu.Unlock()

			// Remove from Namespaces list
			h.namespacesMu.Lock()
			for i, ns := range h.Namespaces {
				if ns == nsName {
					h.Namespaces = append(h.Namespaces[:i], h.Namespaces[i+1:]...)
					break
				}
			}
			h.namespacesMu.Unlock()

			fmt.Printf("   [INFO] ⏹️  Stopped watching deleted namespace: %s\n", nsName)
		}
	}

	// Check for stale namespaces that should be deleted
	h.checkAndDeleteStaleNamespaces(nsList.Items, patterns, prefixes)

	// Start watching new namespaces
	if len(newNamespaces) > 0 {
		patternDisplay := h.NamespacePattern
		if patternDisplay == "" {
			// Build pattern display from extracted patterns and prefixes
			displayParts := []string{}
			if len(patterns) > 0 {
				displayParts = append(displayParts, strings.Join(patterns, ","))
			}
			if len(prefixes) > 0 {
				prefixList := []string{}
				for prefix := range prefixes {
					prefixList = append(prefixList, prefix+"*")
				}
				displayParts = append(displayParts, strings.Join(prefixList, ","))
			}
			if len(displayParts) > 0 {
				patternDisplay = strings.Join(displayParts, ",")
			} else {
				patternDisplay = "(from --namespaces)"
			}
		}
		fmt.Printf("   [INFO] ✨ Discovered %d new namespace(s) matching pattern '%s': [%s]\n",
			len(newNamespaces), patternDisplay, strings.Join(newNamespaces, ", "))

		for _, nsName := range newNamespaces {
			// Mark as watched
			h.watchedNamespacesMu.Lock()
			h.WatchedNamespaces[nsName] = true
			h.watchedNamespacesMu.Unlock()
			h.namespacesMu.Lock()
			h.Namespaces = append(h.Namespaces, nsName)
			h.namespacesMu.Unlock()

			// Start watching this namespace
			go h.watchSingleNamespace(nsName)
			fmt.Printf("   [INFO] ✅ Started watching new namespace: %s\n", nsName)
		}
	}
}

// checkAndDeleteStaleNamespaces checks namespaces matching the pattern and deletes them if they're older than the threshold
func (h *Healer) checkAndDeleteStaleNamespaces(namespaces []v1.Namespace, patterns []string, prefixes map[string]bool) {
	now := time.Now()
	staleNamespaces := []v1.Namespace{}

	// Find namespaces that match the pattern and are older than the threshold
	for _, ns := range namespaces {
		// Skip excluded namespaces
		if h.isExcludedNamespace(ns.Name) {
			continue
		}

		// Check if namespace matches any pattern
		matchesPattern := false
		for _, pattern := range patterns {
			matched, err := filepath.Match(pattern, ns.Name)
			if err == nil && matched {
				matchesPattern = true
				break
			}
		}

		// Check if namespace matches any prefix (if not already matched by wildcard)
		if !matchesPattern {
			for prefix := range prefixes {
				if strings.HasPrefix(ns.Name, prefix) {
					matchesPattern = true
					break
				}
			}
		}

		// Only check age for namespaces that match the pattern
		if matchesPattern {
			// Ignore namespaces already in Terminating state (let them complete on their own)
			if ns.Status.Phase == v1.NamespaceTerminating {
				continue
			}

			// Check if namespace is older than the stale age threshold
			nsAge := now.Sub(ns.CreationTimestamp.Time)
			if nsAge > h.StaleAge {
				staleNamespaces = append(staleNamespaces, ns)
			}
		}
	}

	// Delete stale namespaces (in parallel)
	if len(staleNamespaces) > 0 {
		nsNames := make([]string, len(staleNamespaces))
		for i, ns := range staleNamespaces {
			nsNames[i] = ns.Name
		}
		fmt.Printf("   [INFO] 🗑️  Found %d stale namespace(s) matching pattern (older than %v): [%s]\n",
			len(staleNamespaces), h.StaleAge, strings.Join(nsNames, ", "))

		var wg sync.WaitGroup
		for _, ns := range staleNamespaces {
			wg.Add(1)
			go func(namespace v1.Namespace) {
				defer wg.Done()
				h.triggerNamespaceDeletion(&namespace)
			}(ns)
		}
		wg.Wait()
	}
}

// triggerNamespaceDeletion deletes a namespace
func (h *Healer) triggerNamespaceDeletion(ns *v1.Namespace) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	nsAge := time.Since(ns.CreationTimestamp.Time)
	fmt.Printf("\n!!! NAMESPACE CLEANUP ACTION REQUIRED !!!\n")
	fmt.Printf("    Namespace: %s\n", ns.Name)
	fmt.Printf("    Age: %v (threshold: %v)\n", nsAge.Round(time.Second), h.StaleAge)

	// Ignore namespaces already in Terminating state (not considered for cleanup)
	if ns.Status.Phase == v1.NamespaceTerminating {
		fmt.Printf("   [SKIP] ⏭️ Ignoring namespace %s (already in Terminating state)\n", ns.Name)
		return
	}

	// If cleanup finalizers is enabled and namespace has finalizers, remove them first
	if h.CleanupFinalizers && len(ns.Finalizers) > 0 {
		fmt.Printf("   [INFO] 🔄 Removing finalizers from namespace %s...\n", ns.Name)

		// Get the current namespace to update
		currentNs, err := h.ClientSet.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				fmt.Printf("   [SKIP] ⏭️ Namespace %s was already deleted\n", ns.Name)
				h.removeNamespaceFromTracking(ns.Name)
				return
			}
			fmt.Printf("   [WARN] ⚠️ Failed to get namespace %s for finalizer removal: %v\n", ns.Name, err)
			fmt.Printf("   [INFO] 🔄 Proceeding with deletion anyway...\n")
		} else {
			// Remove all finalizers
			currentNs.Finalizers = []string{}
			_, err = h.ClientSet.CoreV1().Namespaces().Update(ctx, currentNs, metav1.UpdateOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					fmt.Printf("   [SKIP] ⏭️ Namespace %s was deleted during finalizer removal\n", ns.Name)
					h.removeNamespaceFromTracking(ns.Name)
					return
				}
				fmt.Printf("   [WARN] ⚠️ Failed to remove finalizers from namespace %s: %v\n", ns.Name, err)
				fmt.Printf("   [INFO] 🔄 Proceeding with deletion anyway...\n")
			} else {
				fmt.Printf("   [SUCCESS] ✅ Removed finalizers from namespace %s\n", ns.Name)
			}
		}
	}

	// Attempt to delete the namespace (force delete: no grace period, so terminating namespaces are removed immediately)
	err := h.ClientSet.CoreV1().Namespaces().Delete(ctx, ns.Name, forceDeleteOptions)
	if err != nil {
		if apierrors.IsNotFound(err) {
			fmt.Printf("   [SKIP] ⏭️ Namespace %s was already deleted\n", ns.Name)
			h.removeNamespaceFromTracking(ns.Name)
		} else {
			fmt.Printf("   [FAIL] ❌ Failed to delete namespace %s: %v\n", ns.Name, err)
		}
	} else {
		fmt.Printf("   [SUCCESS] ✅ Deleted stale namespace %s\n", ns.Name)
		h.removeNamespaceFromTracking(ns.Name)
	}

	fmt.Printf("!!! NAMESPACE CLEANUP ACTION COMPLETE !!!\n\n")
}

// removeNamespaceFromTracking removes a namespace from tracking maps and lists
func (h *Healer) removeNamespaceFromTracking(nsName string) {
	h.watchedNamespacesMu.Lock()
	delete(h.WatchedNamespaces, nsName)
	h.watchedNamespacesMu.Unlock()
	h.namespacesMu.Lock()
	for i, watchedNs := range h.Namespaces {
		if watchedNs == nsName {
			h.Namespaces = append(h.Namespaces[:i], h.Namespaces[i+1:]...)
			break
		}
	}
	h.namespacesMu.Unlock()
}

// isTestNamespace checks if a namespace matches the test namespace pattern
func (h *Healer) isTestNamespace(namespace string) bool {
	// Build list of patterns to check
	patterns := []string{}

	// Add explicit pattern if set
	if h.NamespacePattern != "" {
		patterns = append(patterns, h.NamespacePattern)
	}

	// Extract wildcard patterns from Namespaces list (from --namespaces flag)
	h.namespacesMu.RLock()
	namespacesCopy := make([]string, len(h.Namespaces))
	copy(namespacesCopy, h.Namespaces)
	h.namespacesMu.RUnlock()
	for _, ns := range namespacesCopy {
		if strings.Contains(ns, "*") {
			patterns = append(patterns, ns)
		}
	}

	// If no patterns found, don't assume test namespaces
	if len(patterns) == 0 {
		return false
	}

	// Check against all patterns
	for _, pattern := range patterns {
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
	h.currentClusterStrainMu.RLock()
	clusterStrain := h.CurrentClusterStrain
	h.currentClusterStrainMu.RUnlock()
	if clusterStrain != nil && clusterStrain.HasStrain {
		// Check if this is a test namespace (test resources are allowed)
		isTestNamespace := h.isTestNamespace(namespace)

		if isTestNamespace {
			// Test namespace resources - warn but allow (tests need to run)
			fmt.Printf("   [WARN] ⚠️ New %s created in test namespace during cluster strain: %s/%s (strain: %.1f%%)\n",
				resourceType, namespace, name, clusterStrain.StrainPercentage)
			fmt.Printf("   [INFO] 💡 Consider reducing parallel test execution or waiting for cluster to recover\n")
		} else {
			// Non-test namespace resources - stronger warning
			fmt.Printf("   [WARN] 🚨 New %s created during cluster strain: %s/%s (strain: %.1f%%)\n",
				resourceType, namespace, name, clusterStrain.StrainPercentage)
			fmt.Printf("   [INFO] 💡 Resource creation throttling active - consider deferring resource creation until cluster recovers\n")
			fmt.Printf("   [INFO] 📊 Cluster status: %d/%d nodes under pressure: [%s]\n",
				clusterStrain.StrainedNodesCount, clusterStrain.TotalNodesCount,
				strings.Join(clusterStrain.NodesUnderPressure, ", "))
		}
	}
}
