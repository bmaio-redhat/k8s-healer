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

	"github.com/daigoro86dev/k8s-healer/pkg/daemon"
	"github.com/daigoro86dev/k8s-healer/pkg/healer"
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
	daemonMode                       bool
	pidFile                          string
	logFile                          string
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
	rootCmd.PersistentFlags().BoolVar(&enableNamespacePolling, "enable-namespace-polling", false,
		"Enable polling for new namespaces matching the pattern during test runs.")
	rootCmd.PersistentFlags().StringVar(&namespacePattern, "namespace-pattern", "",
		"Pattern to match namespaces for polling (e.g., 'test-*'). Required if --enable-namespace-polling is set.")
	rootCmd.PersistentFlags().DurationVar(&namespacePollInterval, "namespace-poll-interval", 30*time.Second,
		"How often to poll for new namespaces (e.g., 30s, 1m). Default: 30s.")
	rootCmd.PersistentFlags().BoolVar(&enableResourceOptimization, "enable-resource-optimization", true,
		"Enable resource optimization during cluster strain - evicts resource-constrained pods when cluster is under pressure. Default: enabled.")
	rootCmd.PersistentFlags().Float64Var(&strainThreshold, "strain-threshold", 30.0,
		"Percentage of nodes under resource pressure to trigger optimization (default: 30.0%%).")
	rootCmd.PersistentFlags().BoolVar(&enableResourceCreationThrottling, "enable-resource-creation-throttling", true,
		"Enable resource creation throttling - warns when resources are created during cluster strain. Default: enabled.")
	rootCmd.PersistentFlags().BoolVar(&daemonMode, "daemon", false,
		"Run as a background daemon. Output will be redirected to log file.")
	rootCmd.PersistentFlags().StringVar(&pidFile, "pid-file", daemon.DefaultPIDFile,
		"Path to the PID file when running as daemon.")
	rootCmd.PersistentFlags().StringVar(&logFile, "log-file", daemon.DefaultLogFile,
		"Path to the log file when running as daemon.")

	// Daemon management subcommands
	rootCmd.AddCommand(daemonStartCmd)
	rootCmd.AddCommand(daemonStopCmd)
	rootCmd.AddCommand(daemonStatusCmd)
}

// resolveWildcardNamespaces connects to the cluster, lists all namespaces, and returns a concrete list
// based on the input patterns, handling wildcards using filepath.Match.
func resolveWildcardNamespaces(kubeconfigPath, namespacesInput string) ([]string, error) {
	if namespacesInput == "" {
		return []string{}, nil // Return empty list, signaling the healer to watch all.
	}

	patterns := strings.Split(namespacesInput, ",")
	for i, p := range patterns {
		patterns[i] = strings.TrimSpace(p)
	}

	// Check if any pattern contains a wildcard. If not, just return the list of patterns.
	needsResolution := false
	for _, p := range patterns {
		if strings.Contains(p, "*") {
			needsResolution = true
			break
		}
	}
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

	// Match existing namespaces against all patterns
	for _, ns := range nsList.Items {
		for _, pattern := range patterns {
			match, err := filepath.Match(pattern, ns.Name)
			if err != nil {
				// This shouldn't happen with simple glob patterns
				fmt.Printf("Warning: Invalid wildcard pattern '%s': %v\n", pattern, err)
				continue
			}
			if match {
				resolvedNamespaces[ns.Name] = true
			}
		}
	}

	// Convert map keys to slice
	var finalNsList []string
	for ns := range resolvedNamespaces {
		finalNsList = append(finalNsList, ns)
	}

	if len(finalNsList) > 0 {
		fmt.Printf("Wildcards resolved. Watching %d namespaces: [%s]\n", len(finalNsList), strings.Join(finalNsList, ", "))
	} else {
		fmt.Println("Warning: Wildcard patterns did not match any existing namespaces.")
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

		config := daemon.DaemonConfig{
			PIDFile: pidFile,
			LogFile: logFile,
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
	// Add daemon flag so the child process knows it's running as daemon
	args = append(args, "--daemon")
	args = append(args, "--pid-file", pidFile)
	args = append(args, "--log-file", logFile)

	return args
}

// startHealer parses the flags, initializes the healer, and manages the shutdown signals.
func startHealer() {
	// If running as daemon, redirect output to log file
	if daemonMode {
		if err := daemon.RedirectOutput(logFile); err != nil {
			fmt.Printf("Warning: Failed to redirect output to log file: %v\n", err)
		}
	}
	// Resolve the raw namespace input (including wildcards) into a concrete list of existing namespaces
	nsList, err := resolveWildcardNamespaces(kubeconfigPath, namespaces)
	if err != nil {
		fmt.Printf("Error resolving namespaces: %v\n", err)
		os.Exit(1)
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
			// Migration policies
			"migrationpolicies.migrations.kubevirt.io/v1alpha1," +
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
	healer, err := healer.NewHealer(kubeconfigPath, nsList, enableVMHealing, enableCRDCleanup, crdResourceList, enableNamespacePolling, namespacePattern, namespacePollInterval)
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
	// Setup signal handling (SIGINT/Ctrl+C and SIGTERM) for graceful shutdown.
	termCh := make(chan os.Signal, 1)
	signal.Notify(termCh, syscall.SIGINT, syscall.SIGTERM)

	// Start the main watch loop in a goroutine. This will start the informers.
	go healer.Watch()

	// Wait for termination signal
	<-termCh
	fmt.Println("\nTermination signal received. Shutting down healer...")

	// Close the StopCh channel to signal all concurrent informers to stop gracefully.
	close(healer.StopCh)

	// Give informers a moment to stop before exiting the process.
	time.Sleep(1 * time.Second)
	fmt.Println("Healer stopped.")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
