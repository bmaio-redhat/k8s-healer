package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath" // Used for wildcard matching (Glob/Match)
	"strings"
	"syscall"
	"time"

	"github.com/bmaio-redhat/k8s-healer/pkg/daemon"
	"github.com/bmaio-redhat/k8s-healer/pkg/discovery"
	"github.com/bmaio-redhat/k8s-healer/pkg/healer"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	kubeconfigPath                   string
	namespaces                       string
	healCooldown                     time.Duration
	enableVMHealing                  bool
	enableCRDCleanup                 bool
	crdResources                     string
	staleAge                         time.Duration
	cleanupFinalizers                bool
	enableResourceOptimization       bool
	strainThreshold                  float64
	enableNamespacePolling           bool
	enableResourceCreationThrottling bool
	namespacePattern                 string
	namespacePollInterval            time.Duration
	excludeNamespaces                string
	daemonMode                       bool
	pidFile                          string
	logFile                          string
	discoverEndpoints                bool
	warmupTimeout                    time.Duration
	memoryLimitMB                    uint
	memoryCheckInterval              time.Duration
	restartOnMemoryLimit             bool
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "k8s-healer",
	Short: "A Kubernetes CLI tool that watches for and heals unhealthy pods, VMs, and stale CRDs.",
	Long: `k8s-healer monitors specified Kubernetes namespaces for persistently unhealthy pods (e.g., in CrashLoopBackOff) 
and performs a healing action by deleting the pod, forcing its controller to recreate it.

For OpenShift clusters, it can also monitor and heal failing VMs by detecting unhealthy nodes and machines.

For test environments, it can act as a janitor by cleaning up stale CRDs (Custom Resource Definitions) that are
stuck with finalizers, in error states, or older than a specified age threshold.

During cluster strain, it can proactively optimize resources by detecting cluster-wide resource pressure and evicting
resource-constrained pods (OOMKilled, high restart count, stuck in Pending) to keep the cluster healthy.

The -n/--namespaces flag supports comma-separated values and simple wildcards (*).

Usage Examples:
  k8s-healer -n prod,staging              # Watch specific namespaces
  k8s-healer -n 'app-*-dev,kube-*'        # Watch namespaces matching wildcards
  k8s-healer                              # Watch all namespaces
  k8s-healer -k /path/to/my/kubeconfig    # Use specific kubeconfig
  k8s-healer --enable-vm-healing=false    # Disable VM healing (enabled by default)
  k8s-healer --enable-crd-cleanup=false   # Disable CRD cleanup (enabled by default)
  k8s-healer --enable-resource-optimization=false  # Disable resource optimization (enabled by default)
  k8s-healer --strain-threshold 25.0     # Set custom strain threshold (default: 30.0%)
  k8s-healer --crd-resources "custom.resource/v1"  # Use custom CRD resources (KubeVirt defaults if not specified)
  k8s-healer start                         # Start as background daemon
  k8s-healer stop                          # Stop running daemon
  k8s-healer status                        # Check daemon status
  k8s-healer cleanup                       # Signal daemon to run full CRD cleanup
  k8s-healer summary                       # Signal daemon to print CRD cleanup summary
`,
	Run: func(cmd *cobra.Command, args []string) {
		startHealer()
	},
}

func init() {
	// Global flags handled by Cobra
	rootCmd.PersistentFlags().StringVarP(&kubeconfigPath, "kubeconfig", "k", "", "Path to the kubeconfig file (defaults to standard locations).")
	rootCmd.PersistentFlags().StringVarP(&namespaces, "namespaces", "n", "", "Comma-separated list of namespaces/workspaces to watch (e.g., 'prod,staging'). Supports wildcards (*). Defaults to all namespaces if empty.")
	rootCmd.PersistentFlags().DurationVar(&healCooldown, "heal-cooldown", 10*time.Minute,
		"Minimum time between healing the same Pod/Node/Machine (e.g. 10m, 30s).")
	rootCmd.PersistentFlags().BoolVar(&enableVMHealing, "enable-vm-healing", true,
		"Enable VM healing for OpenShift clusters (monitors nodes and machines). Default: enabled.")
	rootCmd.PersistentFlags().BoolVar(&enableCRDCleanup, "enable-crd-cleanup", true,
		"Enable CRD cleanup mode - acts as a janitor for stale CRDs in test environments. Default: enabled.")
	rootCmd.PersistentFlags().StringVar(&crdResources, "crd-resources", "",
		"Comma-separated list of CRD resources to monitor (e.g., 'virtualmachines.virtualmachine.kubevirt.io,datavolumes.cdi.kubevirt.io'). If not specified and CRD cleanup is enabled, defaults to KubeVirt resources.")
	rootCmd.PersistentFlags().DurationVar(&staleAge, "stale-age", 6*time.Minute,
		"Age threshold for considering a resource stale (e.g., 1h, 30m). Resources older than this will be cleaned up.")
	rootCmd.PersistentFlags().BoolVar(&cleanupFinalizers, "cleanup-finalizers", true,
		"Remove finalizers from resources before deletion to allow cleanup of stuck resources.")
	rootCmd.PersistentFlags().BoolVar(&enableNamespacePolling, "enable-namespace-polling", true,
		"Enable polling for new namespaces matching the pattern during test runs. Default: enabled.")
	rootCmd.PersistentFlags().StringVar(&namespacePattern, "namespace-pattern", "",
		"Pattern to match namespaces for polling (e.g., 'test-*'). If not specified and namespace polling is enabled, will be derived from --namespaces flag if it contains wildcards.")
	rootCmd.PersistentFlags().DurationVar(&namespacePollInterval, "namespace-poll-interval", 5*time.Second,
		"How often to poll for new namespaces (e.g., 5s, 1m). Default: 5s.")
	rootCmd.PersistentFlags().BoolVar(&enableResourceOptimization, "enable-resource-optimization", true,
		"Enable resource optimization during cluster strain - evicts resource-constrained pods when cluster is under pressure. Default: enabled.")
	rootCmd.PersistentFlags().Float64Var(&strainThreshold, "strain-threshold", 30.0,
		"Percentage of nodes under resource pressure to trigger optimization (default: 30.0%%).")
	rootCmd.PersistentFlags().BoolVar(&enableResourceCreationThrottling, "enable-resource-creation-throttling", true,
		"Enable resource creation throttling - warns when resources are created during cluster strain. Default: enabled.")
	rootCmd.PersistentFlags().BoolVar(&daemonMode, "daemon", false,
		"Run as a background daemon. Output will be redirected to log file.")
	rootCmd.PersistentFlags().StringVarP(&pidFile, "pid-file", "p", daemon.DefaultPIDFile,
		"Path to the PID file. Supported in both foreground and daemon mode; parent directory is created if missing (e.g. -p /var/run/k8s-healer.pid).")
	rootCmd.PersistentFlags().StringVar(&logFile, "log-file", daemon.DefaultLogFile,
		"Path to the log file when running as daemon.")
	rootCmd.PersistentFlags().BoolVar(&discoverEndpoints, "discover", false,
		"Discover and display all available API endpoints and resources in the cluster, then exit.")
	rootCmd.PersistentFlags().StringVarP(&excludeNamespaces, "exclude-namespaces", "e", "",
		"Comma-separated list of namespaces to exclude from prefix-based discovery (e.g., 'test-keep,test-important').")
	rootCmd.PersistentFlags().DurationVarP(&warmupTimeout, "warmup-timeout", "w", 30*time.Second,
		"Timeout for cluster warm-up phase - validates CRD availability and API connectivity before starting heavy operations (e.g., 30s, 1m). Set to 0 to disable warm-up. Default: 30s.")
	rootCmd.PersistentFlags().UintVar(&memoryLimitMB, "memory-limit-mb", 40,
		"Maximum heap size in MB before sanitizing references and optionally restarting (e.g. 50). Default: 40. Set to 0 to disable. When exceeded, tracking maps are cleared and GC run; if still over limit and --restart-on-memory-limit is set, process exits for restart.")
	rootCmd.PersistentFlags().DurationVar(&memoryCheckInterval, "memory-check-interval", 1*time.Minute,
		"How often to check memory when --memory-limit-mb is set (e.g. 1m, 30s). Default: 1m.")
	rootCmd.PersistentFlags().BoolVar(&restartOnMemoryLimit, "restart-on-memory-limit", true,
		"When memory limit is exceeded and sanitization does not bring usage below limit, exit process with code 0 so a process manager can restart. Default: true when using --memory-limit-mb.")

	// Daemon management subcommands
	rootCmd.AddCommand(daemonStartCmd)
	rootCmd.AddCommand(daemonStopCmd)
	rootCmd.AddCommand(daemonStatusCmd)
	rootCmd.AddCommand(cleanupCmd)
	rootCmd.AddCommand(summaryCmd)
}

// extractPrefixesFromNamespaces extracts prefixes from namespace names for prefix-based discovery.
// For example, "test-123" -> "test-", "e2e-456" -> "e2e-".
func extractPrefixesFromNamespaces(namespaces []string) map[string]bool {
	prefixes := make(map[string]bool)

	for _, ns := range namespaces {
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

// resolveWildcardNamespaces connects to the cluster, lists all namespaces, and returns a concrete list
// based on the input patterns, handling wildcards using filepath.Match and prefix-based discovery.
func resolveWildcardNamespaces(kubeconfigPath, namespacesInput string, excludedNamespaces []string) ([]string, error) {
	if namespacesInput == "" {
		return []string{}, nil // Return empty list, signaling the healer to watch all.
	}

	patterns := strings.Split(namespacesInput, ",")
	for i, p := range patterns {
		patterns[i] = strings.TrimSpace(p)
	}

	// Create a map of excluded namespaces for quick lookup
	excludedMap := make(map[string]bool)
	for _, excluded := range excludedNamespaces {
		excludedMap[excluded] = true
	}

	// Check if any pattern contains a wildcard or if we need prefix-based discovery
	needsResolution := false
	hasWildcards := false
	for _, p := range patterns {
		if strings.Contains(p, "*") {
			needsResolution = true
			hasWildcards = true
			break
		}
	}

	// Also check if we have non-wildcard namespaces that need prefix-based discovery
	prefixes := extractPrefixesFromNamespaces(patterns)
	if len(prefixes) > 0 {
		needsResolution = true
	}

	// If no resolution needed, just return the list
	// Note: We still include excluded namespaces (they will be monitored but not deleted)
	if !needsResolution {
		return patterns, nil
	}

	// --- Connect to Kubernetes to list existing namespaces ---

	var config *rest.Config
	var err error

	if kubeconfigPath != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	} else {
		config, err = clientcmd.BuildConfigFromFlags("", "")
	}

	if err != nil {
		return nil, fmt.Errorf("failed to build Kubernetes config for resolution: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes clientset for resolution: %w", err)
	}

	// List all namespaces in the cluster
	nsList, err := clientset.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list namespaces for wildcard resolution: %w", err)
	}

	resolvedNamespaces := make(map[string]bool)

	// First, add explicitly specified namespaces (non-wildcard)
	// Note: We still include excluded namespaces (they will be monitored but not deleted)
	for _, ns := range patterns {
		if !strings.Contains(ns, "*") {
			resolvedNamespaces[ns] = true
		}
	}

	// Match existing namespaces against all patterns and prefixes
	// Note: We still include excluded namespaces in the watched list (they will be monitored but not deleted)
	for _, ns := range nsList.Items {
		// Match against wildcard patterns
		if hasWildcards {
			for _, pattern := range patterns {
				if strings.Contains(pattern, "*") {
					match, err := filepath.Match(pattern, ns.Name)
					if err != nil {
						// This shouldn't happen with simple glob patterns
						fmt.Printf("Warning: Invalid wildcard pattern '%s': %v\n", pattern, err)
						continue
					}
					if match {
						resolvedNamespaces[ns.Name] = true
						break // Found a match, no need to check other patterns
					}
				}
			}
		}

		// Match against prefixes (if not already matched)
		if !resolvedNamespaces[ns.Name] {
			for prefix := range prefixes {
				if strings.HasPrefix(ns.Name, prefix) {
					resolvedNamespaces[ns.Name] = true
					break // Found a match, no need to check other prefixes
				}
			}
		}
	}

	// Convert map keys to slice
	var finalNsList []string
	for ns := range resolvedNamespaces {
		finalNsList = append(finalNsList, ns)
	}

	if len(finalNsList) > 0 {
		fmt.Printf("Wildcards and prefixes resolved. Watching %d namespaces: [%s]\n", len(finalNsList), strings.Join(finalNsList, ", "))
	} else {
		fmt.Println("Warning: Wildcard patterns and prefixes did not match any existing namespaces.")
	}

	return finalNsList, nil
}

// daemonStartCmd starts the healer as a daemon
var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start k8s-healer as a background daemon",
	Long:  "Start k8s-healer as a background daemon. The process will run in the background and output will be redirected to a log file.",
	Run: func(cmd *cobra.Command, args []string) {
		// Get all non-daemon flags to pass to the daemon process
		daemonArgs := getDaemonArgs()

		pf, lf := pidFile, logFile
		if pf == "" {
			pf = daemon.DefaultPIDFile
		}
		if lf == "" {
			lf = daemon.DefaultLogFile
		}
		config := daemon.DaemonConfig{
			PIDFile: pf,
			LogFile: lf,
			Args:    daemonArgs,
		}

		if err := daemon.StartDaemon(config); err != nil {
			fmt.Printf("Error starting daemon: %v\n", err)
			os.Exit(1)
		}
	},
}

// daemonStopCmd stops the running daemon
var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running k8s-healer daemon",
	Long:  "Stop the running k8s-healer daemon by sending a termination signal.",
	Run: func(cmd *cobra.Command, args []string) {
		if err := daemon.StopDaemon(pidFile); err != nil {
			fmt.Printf("Error stopping daemon: %v\n", err)
			os.Exit(1)
		}
	},
}

// daemonStatusCmd checks the status of the daemon
var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check the status of the k8s-healer daemon",
	Long:  "Check if the k8s-healer daemon is currently running.",
	Run: func(cmd *cobra.Command, args []string) {
		if err := daemon.StatusDaemon(pidFile); err != nil {
			os.Exit(1)
		}
	},
}

// cleanupCmd signals the running daemon to run a one-off full CRD cleanup (finish cleaning all pending resources).
func cleanupCmdRun(cmd *cobra.Command, args []string) {
	if err := daemon.SendSignal(pidFile, syscall.SIGUSR1); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Sent cleanup signal to daemon. Full CRD cleanup is running.")
}

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Signal the daemon to run a one-off full CRD cleanup",
	Long:  "Sends SIGUSR1 to the running k8s-healer daemon to trigger a one-off full CRD cleanup for all registered resource types. Use --pid-file if the daemon uses a custom PID file.",
	Run:   cleanupCmdRun,
}

// summaryPathFromPIDFile returns the path where the daemon writes the summary when it receives SIGUSR2 (same directory as PID file).
func summaryPathFromPIDFile(pidFile string) string {
	if pidFile == "" {
		pidFile = daemon.DefaultPIDFile
	}
	abs, _ := filepath.Abs(pidFile)
	return filepath.Join(filepath.Dir(abs), "k8s-healer-summary.txt")
}

// summaryCmd signals the daemon to write the CRD cleanup summary to a file, then reads that file and prints it to stdout (no log file needed).
func summaryCmdRun(cmd *cobra.Command, args []string) {
	if err := daemon.SendSignal(pidFile, syscall.SIGUSR2); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	pf := pidFile
	if pf == "" {
		pf = daemon.DefaultPIDFile
	}
	summaryPath := summaryPathFromPIDFile(pf)
	time.Sleep(400 * time.Millisecond) // give daemon time to write the summary file
	data, err := os.ReadFile(summaryPath)
	if err != nil {
		fmt.Printf("Summary not ready or daemon not responding (check PID file path): %v\n", err)
		os.Exit(1)
	}
	os.Remove(summaryPath) // clean up
	fmt.Print(string(data))
}

var summaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Signal the daemon to write CRD cleanup summary and print it to stdout",
	Long:  "Sends SIGUSR2 to the running k8s-healer daemon, which writes the CRD cleanup summary (counts per type and total) to a temp file. This command then reads that file and prints it directly to stdout. Use -p/--pid-file if the daemon uses a custom PID file path.",
	Run:   summaryCmdRun,
}

// getDaemonArgs extracts all non-daemon flags to pass to the daemon process
func getDaemonArgs() []string {
	var args []string

	// Reconstruct all the flags that were passed
	if kubeconfigPath != "" {
		args = append(args, "--kubeconfig", kubeconfigPath)
	}
	if namespaces != "" {
		args = append(args, "--namespaces", namespaces)
	}
	if healCooldown != 10*time.Minute {
		args = append(args, "--heal-cooldown", healCooldown.String())
	}
	if !enableVMHealing {
		args = append(args, "--enable-vm-healing=false")
	}
	if !enableCRDCleanup {
		args = append(args, "--enable-crd-cleanup=false")
	}
	if crdResources != "" {
		args = append(args, "--crd-resources", crdResources)
	}
	if staleAge != 6*time.Minute {
		args = append(args, "--stale-age", staleAge.String())
	}
	if !cleanupFinalizers {
		args = append(args, "--cleanup-finalizers=false")
	}
	if !enableResourceOptimization {
		args = append(args, "--enable-resource-optimization=false")
	}
	if strainThreshold != 30.0 {
		args = append(args, "--strain-threshold", fmt.Sprintf("%.1f", strainThreshold))
	}
	if excludeNamespaces != "" {
		args = append(args, "--exclude-namespaces", excludeNamespaces)
	}
	if memoryLimitMB > 0 {
		args = append(args, "--memory-limit-mb", fmt.Sprintf("%d", memoryLimitMB))
		args = append(args, "--memory-check-interval", memoryCheckInterval.String())
		if !restartOnMemoryLimit {
			args = append(args, "--restart-on-memory-limit=false")
		}
	}
	// Add daemon flag so the child process knows it's running as daemon
	args = append(args, "--daemon")
	pf, lf := pidFile, logFile
	if pf == "" {
		pf = daemon.DefaultPIDFile
	}
	if lf == "" {
		lf = daemon.DefaultLogFile
	}
	args = append(args, "--pid-file", pf, "--log-file", lf)

	return args
}

// startHealer parses the flags, initializes the healer, and manages the shutdown signals.
func startHealer() {
	// If discover flag is set, run endpoint discovery and exit
	if discoverEndpoints {
		if err := discovery.DiscoverClusterEndpoints(kubeconfigPath); err != nil {
			fmt.Printf("Error discovering cluster endpoints: %v\n", err)
			os.Exit(1)
		}
		return // Exit after discovery
	}

	// If running as daemon, redirect output to log file
	if daemonMode {
		if err := daemon.RedirectOutput(logFile); err != nil {
			fmt.Printf("Warning: Failed to redirect output to log file: %v\n", err)
		}
	}

	// Parse excluded namespaces
	var excludedNsList []string
	if excludeNamespaces != "" {
		excludedParts := strings.Split(excludeNamespaces, ",")
		for _, part := range excludedParts {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				excludedNsList = append(excludedNsList, trimmed)
			}
		}
	}

	// Resolve the raw namespace input (including wildcards and prefixes) into a concrete list of existing namespaces
	nsList, err := resolveWildcardNamespaces(kubeconfigPath, namespaces, excludedNsList)
	if err != nil {
		fmt.Printf("Error resolving namespaces: %v\n", err)
		os.Exit(1)
	}

	// Extract patterns from namespaces flag for namespace polling
	// If namespace polling is enabled and no explicit pattern is set, derive it from --namespaces
	if enableNamespacePolling && namespacePattern == "" && namespaces != "" {
		// Extract wildcard patterns from the namespaces flag
		patterns := strings.Split(namespaces, ",")
		wildcardPatterns := []string{}
		for _, p := range patterns {
			p = strings.TrimSpace(p)
			if strings.Contains(p, "*") {
				wildcardPatterns = append(wildcardPatterns, p)
			}
		}
		// If we found wildcard patterns, use the first one (or combine them)
		if len(wildcardPatterns) > 0 {
			namespacePattern = wildcardPatterns[0] // Use first wildcard pattern for polling
			if len(wildcardPatterns) > 1 {
				fmt.Printf("Note: Using first wildcard pattern '%s' for namespace polling (from --namespaces flag)\n", namespacePattern)
			}
		}
	}

	// Validate CRD cleanup flags
	if enableCRDCleanup && crdResources == "" {
		// Set default CRD resources if cleanup is enabled but none specified
		// Comprehensive list based on KubeVirt/OpenShift test environments
		// Format: plural.group/version (e.g., "virtualmachines.kubevirt.io/v1")
		crdResources = "virtualmachines.kubevirt.io/v1," +
			"virtualmachineinstances.kubevirt.io/v1," +
			"virtualmachineinstancemigrations.kubevirt.io/v1," +
			"virtualmachineinstancereplicasets.kubevirt.io/v1," +
			// CDI resources (VM disks and data sources)
			"datavolumes.cdi.kubevirt.io/v1beta1," +
			"datasources.cdi.kubevirt.io/v1beta1," +
			// Snapshot and clone resources
			"virtualmachinesnapshots.snapshot.kubevirt.io/v1beta1," +
			"virtualmachineclones.clone.kubevirt.io/v1alpha1," +
			// Instance types and preferences
			"virtualmachineinstancetypes.instancetype.kubevirt.io/v1beta1," +
			"virtualmachineclusterinstancetypes.instancetype.kubevirt.io/v1beta1," +
			"virtualmachinepreferences.instancetype.kubevirt.io/v1beta1," +
			"virtualmachineclusterpreferences.instancetype.kubevirt.io/v1beta1," +
			// Migration policies and plans
			"migrationpolicies.migrations.kubevirt.io/v1alpha1," +
			"virtualmachinemigrationplans.kubevirt.io/v1alpha1," +
			// Network resources
			"network-attachment-definitions.k8s.cni.cncf.io/v1," +
			"userdefinednetworks.k8s.ovn.org/v1," +
			"clusteruserdefinednetworks.k8s.ovn.org/v1," +
			// OpenShift templates
			"templates.template.openshift.io/v1"
		fmt.Printf("No CRD resources specified, using comprehensive defaults (covering all Playwright test resources)\n")
	}

	// Parse CRD resources if enabled
	var crdResourceList []string
	if enableCRDCleanup {
		crdResourceList = strings.Split(crdResources, ",")
		for i, r := range crdResourceList {
			crdResourceList[i] = strings.TrimSpace(r)
		}
	}

	// Initialize the Healer module. This connects to Kubernetes.
	healer, err := healer.NewHealer(kubeconfigPath, nsList, enableVMHealing, enableCRDCleanup, crdResourceList, enableNamespacePolling, namespacePattern, namespacePollInterval, excludedNsList)
	if err != nil {
		fmt.Printf("Error setting up Kubernetes client: %v\n", err)
		os.Exit(1)
	}

	healer.HealCooldown = healCooldown
	healer.StaleAge = staleAge
	healer.CleanupFinalizers = cleanupFinalizers
	healer.EnableResourceOptimization = enableResourceOptimization
	healer.EnableResourceCreationThrottling = enableResourceCreationThrottling
	healer.StrainThreshold = strainThreshold
	if memoryLimitMB > 0 {
		healer.MemoryLimitMB = uint64(memoryLimitMB)
		healer.MemoryCheckInterval = memoryCheckInterval
		healer.RestartOnMemoryLimit = restartOnMemoryLimit
		fmt.Printf("Memory limit: %d MB (check every %v, restart on limit: %v)\n", memoryLimitMB, memoryCheckInterval, restartOnMemoryLimit)
	}
	// Warm up the cluster if enabled (validates CRD availability before heavy operations)
	if warmupTimeout > 0 {
		fmt.Printf("\n🔥 Warming up cluster (timeout: %v)...\n", warmupTimeout)
		if err := healer.Warmup(warmupTimeout); err != nil {
			fmt.Printf("⚠️  Warning: Cluster warm-up completed with issues: %v\n", err)
			fmt.Printf("   Continuing anyway - some CRD resources may not be available yet.\n")
		} else {
			fmt.Printf("✅ Cluster warm-up completed successfully - ready for heavy operations!\n\n")
		}
	}

	// Write PID file (foreground and --daemon). Creates directory if missing. When using "start" subcommand the parent also writes it in StartDaemon; here we ensure it exists for both direct run and --daemon. Remove on exit.
	pf := pidFile
	if pf == "" {
		pf = daemon.DefaultPIDFile
	}
	if err := daemon.WritePIDFile(pf, os.Getpid()); err != nil {
		fmt.Printf("Warning: Failed to write PID file at %s: %v\n", pf, err)
	} else {
		defer os.Remove(pf)
	}

	// Setup signal handling: SIGINT/SIGTERM for shutdown; SIGUSR1 trigger full CRD cleanup; SIGUSR2 print CRD cleanup summary.
	termCh := make(chan os.Signal, 1)
	signal.Notify(termCh, syscall.SIGINT, syscall.SIGTERM)
	cleanupCh := make(chan os.Signal, 1)
	signal.Notify(cleanupCh, syscall.SIGUSR1)
	summaryCh := make(chan os.Signal, 1)
	signal.Notify(summaryCh, syscall.SIGUSR2)

	// Start the main watch loop in a goroutine. This will start the informers.
	watchDone := make(chan struct{})
	go func() {
		healer.Watch()
		close(watchDone)
	}()

	// Wait for termination, restart, or user signals (cleanup/summary)
	for {
		select {
		case <-termCh:
			fmt.Println("\nTermination signal received. Shutting down healer...")
			goto shutdown
		case <-healer.RestartRequested:
			fmt.Println("\nMemory limit exceeded after sanitization — exiting for restart...")
			goto shutdown
		case <-cleanupCh:
			healer.RunFullCRDCleanup()
		case <-summaryCh:
			summaryPath := summaryPathFromPIDFile(pidFile)
			if f, err := os.Create(summaryPath); err == nil {
				healer.PrintCRDCleanupSummaryTo(f)
				f.Close()
			} else {
				healer.PrintCRDCleanupSummary() // fallback to stdout/log
			}
		}
	}
shutdown:

	// Close the StopCh channel to signal all concurrent informers to stop gracefully.
	close(healer.StopCh)

	// Give informers a moment to stop before exiting the process.
	<-watchDone
	time.Sleep(1 * time.Second)
	fmt.Println("Healer stopped.")

	if healer.IsRestartRequested() {
		os.Exit(0) // Exit 0 so process manager (e.g. systemd, k8s) restarts the process
	}
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
