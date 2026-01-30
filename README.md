# 🩺 k8s-healer

A powerful and reliable **command-line interface (CLI)** tool written in
Go that watches specified Kubernetes namespaces for unhealthy Pods and
automatically performs a healing action --- safely and intelligently.

For **OpenShift clusters**, it can also monitor and heal failing VMs by detecting
unhealthy nodes and performing automated remediation.

For **test environments**, it can act as a janitor by cleaning up stale CRDs
(Custom Resource Definitions) that are stuck with finalizers, in error states,
or older than a specified age threshold, ensuring tests run smoothly.

------------------------------------------------------------------------

## 🌟 Overview

**k8s-healer** continuously monitors Kubernetes Pods and automatically
heals those stuck in persistent failure states (like
`CrashLoopBackOff`).\
It does this by deleting the faulty Pod, allowing the managing
controller (e.g., Deployment, StatefulSet) to recreate a fresh, healthy
instance.

For OpenShift clusters with VM healing enabled, it also monitors node health
and can automatically drain and delete failing nodes, allowing the machine
controller to recreate them.

The tool monitors VirtualMachine resources directly. VMs are only deleted
if they exist for more than 6 minutes (allowing tests to self-cleanup). VMs in
ErrorUnschedulable state are cleaned up after 3 minutes to handle scheduling failures
faster while still allowing time for recovery.

CRD cleanup mode allows the tool to act as a janitor for test environments,
automatically cleaning up stale CRD resources that are stuck, in error states, or
older than a configurable threshold. This is particularly useful when running tests
in parallel, as it ensures the cluster stays clean and tests can run smoothly.

Resource creation throttling warns when resources are created during cluster
strain, helping prevent further resource pressure. Test namespace resources are allowed
but logged with guidance, while non-test resources receive stronger warnings.

Cluster information summary is displayed immediately upon connection, showing
Kubernetes version, cluster host, node count, watched namespaces, enabled features, and
configuration. This information is logged even when running as a daemon.

Namespace management includes automatic discovery of namespaces matching patterns,
monitoring of discovered namespaces, and automatic cleanup of stale namespaces that
exceed the age threshold, while respecting exclusion lists.

The tool is designed to be lightweight, safe, and extensible ---
suitable for both local development, test environments, and production environments.

------------------------------------------------------------------------

## ⚙️ Key Features

-   **Real-time Monitoring:** Efficiently watches Pod status updates
    using Kubernetes Informers (no polling).\
-   **Targeted Healing:** Automatically deletes Pods that exceed a
    restart threshold and are stuck in states like `CrashLoopBackOff`.\
-   **VM Healing (OpenShift):** Monitors node health and VirtualMachine resources,
    automatically draining/deleting failing nodes and VMs for remediation.\
-   **CRD Cleanup:** Acts as a janitor for test environments by cleaning up
    stale CRD resources that are stuck with finalizers, in error states, or older
    than a configurable threshold.\
-   **Resource Optimization:** Proactively optimizes cluster resources during
    strain by detecting cluster-wide resource pressure and evicting resource-constrained
    pods (OOMKilled, high restart count, stuck in Pending). Helps keep clusters healthy
    during heavy test workloads.\
-   **Resource Creation Throttling:** Monitors and warns when resources are created
    during cluster strain, helping prevent further resource pressure. Provides actionable
    guidance to operators about deferring non-critical resource creation.\
-   **Healing Cooldown:** Prevents repeated healing loops by
    skipping Pods recently healed within a configurable cooldown period
    (default: **10 minutes**).\
-   **Flexible Namespace Selection:** Supports multiple namespaces and
    wildcard patterns (e.g. `app-*`, `prod-*`).\
-   **Namespace Management:** Automatically discovers namespaces matching patterns,
    monitors them for resource cleanup, and deletes stale namespaces that exceed
    the age threshold. Supports namespace exclusion to protect specific namespaces
    from deletion while still monitoring them.\
-   **Graceful Shutdown:** Handles OS signals cleanly (`Ctrl+C`,
    `SIGTERM`) for safe exits.\
-   **Daemon Mode:** Run as a background daemon with automatic
    log file redirection and PID file management.\
-   **Cluster Information Display:** Automatically displays cluster summary
    upon connection, including Kubernetes version, cluster host, node count,
    watched namespaces, enabled features, and configuration. Works in both
    foreground and daemon modes.\
-   **Endpoint Discovery:** Discover and display all available API endpoints
    and resources in the cluster using the `--discover` flag. Useful for exploring
    cluster capabilities and verifying API group availability.\
-   **Pluggable Architecture:** Easy to extend with new failure
    detection logic or healing strategies.

------------------------------------------------------------------------

## 🩹 Healing Strategy

### Default Workflow (Pods)

1.  **Detection:**\
    The tool checks if a Pod has containers in `CrashLoopBackOff` or
    other failure states (e.g., `ImagePullBackOff`).\
    If a container's `RestartCount` exceeds a configurable threshold
    (default: `3`), the Pod is marked unhealthy.

2.  **Cooldown Check:**\
    Before deleting, k8s-healer verifies whether the Pod was healed
    recently.\
    If the same Pod was healed within the **cooldown window** (default:
    `10m`), it is skipped to avoid endless delete/recreate loops.

3.  **Deletion:**\
    If eligible, the Pod is deleted via the Kubernetes API.\
    Its managing controller detects the deletion and immediately spins
    up a replacement Pod.

4.  **Reconciliation:**\
    The new Pod is scheduled and started fresh --- effectively
    self-healing the workload.

### VM Healing Workflow (OpenShift)

1.  **Node Detection:**\
    Monitors node conditions for `NotReady`, `Unknown`, or resource pressure
    states that persist beyond configurable thresholds.

2.  **VirtualMachine Detection:**\
    Monitors VirtualMachine resources for health issues. VMs are only deleted if:
    - They are older than 6 minutes (regardless of health state), allowing tests to self-cleanup
    - VMs in `ErrorUnschedulable` state are cleaned up after 3 minutes (faster cleanup for scheduling failures)
    - Health monitoring continues for logging purposes even if deletion is deferred

3.  **Pod Eviction:**\
    Before deleting a failing node, attempts to safely drain it by evicting
    all non-DaemonSet pods.

4.  **Node/VM Deletion:**\
    Deletes failing nodes and VirtualMachines, allowing controllers to recreate
    them with fresh instances.

5.  **VM Recovery:**\
    The new node/VM is provisioned and workloads are rescheduled.

### Resource Optimization Workflow (Cluster Strain)

1.  **Cluster Strain Detection:**\
    Monitors all nodes in the cluster for resource pressure conditions
    (MemoryPressure, DiskPressure, PIDPressure). Calculates the percentage
    of nodes under pressure.

2.  **Strain Threshold:**\
    When the percentage of nodes under pressure exceeds the configured
    threshold (default: 30%), the cluster is considered "under strain"
    and optimization actions are triggered.

3.  **Pod Resource Issue Detection:**\
    Identifies pods with resource-related issues:
    - Pods that were OOMKilled (Out of Memory)
    - Pods with high restart counts (≥5) indicating persistent resource issues
    - Pods stuck in Pending state for extended periods (likely resource constraints)
    - Pods that cannot be scheduled due to insufficient resources

4.  **Test Namespace Pod Protection:**\
    Test namespace pods (matching the namespace pattern) are **never evicted**
    and always receive priority for resource allocation. When cluster is under
    strain or test pods need resources, non-test pods are evicted first to free
    resources for test workloads.

5.  **Priority-Based Eviction:**\
    Among non-test pods, evicts based on priority classes, ensuring critical
    system pods are protected. Lower priority non-test pods are evicted first
    to free resources for test namespace pods.

6.  **Graceful Eviction:**\
    Uses the Kubernetes Eviction API for graceful pod termination, allowing
    controllers to recreate pods when resources become available.

7.  **Cooldown Period:**\
    Prevents repeatedly evicting the same pods by maintaining a cooldown
    period (default: 10 minutes) between optimizations.

8.  **Cross-Namespace Resource Optimization:**\
    When test pods need resources, the healer checks **all namespaces** (not
    just watched ones) to find non-test pods that can be evicted, ensuring
    maximum resource availability for test workloads.

This feature is particularly useful when running heavy test workloads (e.g., Playwright tests)
that can cause cluster resource pressure, helping to keep the cluster healthy and tests running smoothly.

**Test Namespace Pod Protection:** When namespace polling is enabled with a pattern (e.g., `test-*`),
pods in matching namespaces are **protected from eviction** and always receive priority for resource
allocation. The healer will proactively evict non-test pods (even from other namespaces) to ensure
test pods have the best possible resource allocation.

### CRD Cleanup Workflow (Test Environments)

**Default CRD Resources Monitored (Comprehensive KubeVirt/OpenShift Coverage):**
- **KubeVirt Core:** VirtualMachines, VirtualMachineInstances, VMI Migrations, VMI ReplicaSets
- **CDI Storage:** DataVolumes (VM disks), DataSources (bootable volumes)
- **Snapshots & Clones:** VirtualMachineSnapshots, VirtualMachineClones
- **Instance Types:** VirtualMachineInstanceTypes, VirtualMachineClusterInstanceTypes
- **Preferences:** VirtualMachinePreferences, VirtualMachineClusterPreferences
- **Migration:** MigrationPolicies, VirtualMachineMigrationPlans
- **Networking:** NetworkAttachmentDefinitions, UserDefinedNetworks, ClusterUserDefinedNetworks
- **OpenShift:** Templates

This comprehensive list ensures all resources created during Playwright test runs are properly cleaned up.

1.  **Resource Detection:**\
    Monitors specified CRD resource types across watched namespaces,
    checking for stale resources based on multiple criteria.

2.  **Staleness Criteria:**\
    A resource is considered stale if it meets any of these conditions:
    - Older than the configured stale age threshold (default: 6 minutes)
    - Stuck in terminating state for too long (indicates stuck finalizers)
    - Has error conditions in status that have persisted
    - In a failed/error phase (e.g., Error, Failed, Terminated)

3.  **Finalizer Cleanup (Optional):**\
    If enabled, removes finalizers from resources before deletion to
    allow cleanup of stuck resources that cannot be deleted normally.

4.  **Resource Deletion:**\
    Deletes stale resources, allowing the cluster to stay clean for
    parallel test execution.

5.  **Test Environment Stability:**\
    By continuously cleaning up stale resources, tests can run smoothly
    without interference from leftover test artifacts.

### Namespace Management Workflow (Test Environments)

**Namespace polling is enabled by default** to automatically discover, monitor, and clean up test namespaces.

1.  **Initial Namespace Resolution:**\
    Resolves wildcard patterns to concrete namespace lists at startup.
    Also extracts prefixes from provided namespaces (e.g., `test-123` → `test-*`)
    for prefix-based discovery.

2.  **Dynamic Namespace Discovery:**\
    Periodically checks the cluster for new namespaces matching the specified
    pattern (wildcard like `test-*`) or prefix (derived from provided namespaces).
    Enabled by default, but requires either `--namespace-pattern` or namespaces
    with extractable prefixes to function.

3.  **Automatic Watch Addition:**\
    When new matching namespaces are discovered, automatically starts
    watching them for pod healing and CRD cleanup.

4.  **Real-time Monitoring:**\
    Newly discovered namespaces are immediately monitored, ensuring
    test namespaces created during parallel test execution are covered.

5.  **Deleted Namespace Detection:**\
    Automatically detects when namespaces matching the pattern are deleted
    externally and stops watching them, cleaning up tracking information.

6.  **Stale Namespace Cleanup:**\
    Automatically deletes namespaces that match the pattern and are older
    than the stale age threshold (default: 6 minutes). Excluded namespaces
    are never deleted, even if they match the pattern and exceed the threshold.

7.  **Namespace Exclusion:**\
    Namespaces specified in `--exclude-namespaces` are still discovered and
    monitored, but resources within them are never deleted. This allows
    protecting important test namespaces while still monitoring their health.

8.  **Continuous Operation:**\
    Polling continues throughout the healer's runtime, adapting to
    dynamically created and deleted test namespaces.

**Note:** To disable namespace polling, use `--enable-namespace-polling=false`.

------------------------------------------------------------------------

## 🚀 Usage

### 🏗️ Build

``` bash
# 1. Initialize dependencies
go mod tidy

# 2. Build the executable
go build -o k8s-healer ./cmd/main.go
```

### 🐳 Docker / Podman

k8s-healer can be run as a container using Docker or Podman, providing a lightweight, isolated environment. The container image uses a minimal `scratch` base image for the smallest possible footprint.

**Note:** All examples use `docker` commands, but they work identically with `podman` - simply replace `docker` with `podman` in any command.

#### Building the Container Image

```bash
# Build the container image with Docker
docker build -t k8s-healer .

# Or build with Podman (identical command)
podman build -t k8s-healer .
```

#### Running the Container

**Easy Way (Recommended):** Use the `docker-k8s-healer.sh` wrapper script to automatically handle volume mounting. This allows you to use the same flags as the native binary without manually specifying the `-v` flag. The script automatically prefers Podman and falls back to Docker if Podman is not available:

```bash
# Use the wrapper script - automatically handles volume mounting and container runtime detection
./docker-k8s-healer.sh -k /path/to/kubeconfig -n "pw-*" -e "pw-test-ns"

# Explicitly use Podman (overrides auto-detection)
./docker-k8s-healer.sh podman -k /path/to/kubeconfig -n "pw-*" -e "pw-test-ns"

# Explicitly use Docker (overrides auto-detection)
./docker-k8s-healer.sh docker -k /path/to/kubeconfig -n "pw-*" -e "pw-test-ns"

# Run as daemon
./docker-k8s-healer.sh -k /path/to/kubeconfig --daemon -n "pw-*"
```

**Manual Way:** Mount your kubeconfig file manually using the `-v` flag. Mount to `/root/.kube/config` (the default location) to avoid needing the `-k` flag.

**Important:** All examples use the `--rm` flag to automatically remove the container when it exits or is stopped (e.g., with `Ctrl+C`). All `docker` commands can be replaced with `podman` for identical functionality.

**Manual Docker/Podman Examples (if not using wrapper script):**

```bash
# Basic usage - mount to default location, no -k flag needed
docker run --rm -v ~/.kube/config:/root/.kube/config:ro k8s-healer

# Watch namespaces - mount custom kubeconfig to default location
docker run --rm -v /path/to/kubeconfig:/root/.kube/config:ro k8s-healer -n "pw-*"

# Watch namespaces and exclude specific ones
docker run --rm -v ~/.kube/config:/root/.kube/config:ro k8s-healer -n "pw-*" -e "pw-test-ns"

# Full example with custom kubeconfig path (mounted to default location)
docker run --rm -v /home/user/.kubeconfigs/test-config:/root/.kube/config:ro k8s-healer -n "pw-*" -e "pw-test-ns"

# Run as daemon (background mode)
docker run -d --rm --name k8s-healer -v ~/.kube/config:/root/.kube/config:ro k8s-healer --daemon -n "pw-*"

# Discover cluster endpoints
docker run --rm -v ~/.kube/config:/root/.kube/config:ro k8s-healer --discover
```

**Note:** Replace `docker` with `podman` in any command above for Podman usage.

#### Container Management

```bash
# Stop a running container (if running in daemon mode)
docker stop k8s-healer
# Or with Podman:
podman stop k8s-healer

# View container logs (if running in daemon mode)
docker logs k8s-healer
# Or with Podman:
podman logs k8s-healer

# Follow container logs in real-time
docker logs -f k8s-healer
# Or with Podman:
podman logs -f k8s-healer
```

**Note:** The `--rm` flag ensures containers are automatically cleaned up when they exit, preventing accumulation of stopped containers. This is especially useful when running the tool interactively or in CI/CD pipelines.

**Podman Compatibility:** Podman is fully compatible with all Docker commands shown above. Simply replace `docker` with `podman` in any command. Podman is daemonless and rootless by default, making it ideal for environments where Docker daemon access is restricted.

### ▶️ Run

By default, the tool authenticates using your local **Kubeconfig** file
(e.g. `~/.kube/config`).

  ------------------------------------------------------------------------------
  Flag                     Description                       Example
  ------------------------ --------------------------------- -----------------------
  `-n, --namespaces`       Comma-separated list of           `-n "prod-*,staging"`
                           namespaces to watch. Supports     
                           wildcards (`*`). Works for both
                           pod monitoring and CRD cleanup.                  

  `-k, --kubeconfig`       Path to a specific kubeconfig     `-k ~/.kube/config`
                           file.                             

  `--heal-cooldown`        Minimum duration between healing  `--heal-cooldown 5m`
                           the same Pod/Node. Default: `10m`. 

  `--enable-vm-healing`    Enable VM healing for OpenShift  `--enable-vm-healing=false`
                           clusters (monitors nodes).        
                           Default: enabled.

  `--enable-crd-cleanup`   Enable CRD cleanup mode for       `--enable-crd-cleanup=false`
                           test environments (janitor mode).
                           Default: enabled. If no CRD
                           resources specified, uses KubeVirt
                           defaults. 

  `--crd-resources`        Comma-separated list of CRD       `--crd-resources "virtualmachines.kubevirt.io/v1,datavolumes.cdi.kubevirt.io"`
                           resources to monitor. If not       
                           specified and CRD cleanup is      
                           enabled, defaults to KubeVirt    
                           resources. Format:                
                           `resource.group/version` or       
                           `resource.group` (defaults to v1).                              

  `--stale-age`            Age threshold for considering     `--stale-age 30m`
                           a resource stale. Default: `6m`.   

  `--cleanup-finalizers`   Remove finalizers before          `--cleanup-finalizers`
                           deletion (default: true).         

  `--enable-resource-      Enable resource optimization       `--enable-resource-optimization=false`
   optimization`           during cluster strain (default:    
                           enabled).                         

  `--strain-threshold`     Percentage of nodes under          `--strain-threshold 25.0`
                           pressure to trigger optimization  
                           (default: 30.0%%).                

  `--daemon`               Run as a background daemon.       `--daemon`
                           Output will be redirected to      
                           log file.                         

  `--pid-file`             Path to the PID file when         `--pid-file /var/run/k8s-healer.pid`
                           running as daemon (default:       
                           `/tmp/k8s-healer.pid`).           

  `--log-file`             Path to the log file when         `--log-file /var/log/k8s-healer.log`
                           running as daemon (default:        
                           `/tmp/k8s-healer.log`).           

  `--enable-namespace-polling` Enable polling for new        `--enable-namespace-polling=false`
                               namespaces matching the        
                               pattern during test runs.       
                               Default: enabled.

  `--namespace-pattern`    Pattern to match namespaces for    `--namespace-pattern "test-*"`
                           polling (e.g., 'test-*'). Required 
                           if `--enable-namespace-polling`    
                           is set.                            

  `--namespace-poll-interval` How often to poll for new       `--namespace-poll-interval 5s`
                             namespaces (e.g., 5s, 1m).      
                             Default: `5s`.                  

  `--exclude-namespaces`, `-e` Comma-separated list of       `--exclude-namespaces test-keep,test-important`
                             namespaces to exclude from       `-e test-keep`
                             deletion. These namespaces       
                             are still discovered and         
                             monitored, but resources         
                             within them are never deleted.   

  `--enable-resource-creation-throttling` Enable resource     `--enable-resource-creation-throttling=false`
                                          creation throttling  
                                          warnings during      
                                          cluster strain      
                                          (default: enabled). 

  `--discover`                            Discover and display `--discover`
                                          all available API    
                                          endpoints and        
                                          resources in the     
                                          cluster, then exit.  
                                          Useful for exploring 
                                          cluster capabilities.
  
------------------------------------------------------------------------

### 💡 Examples

``` bash
# Watch all namespaces (default) - FOREGROUND MODE
./k8s-healer

# Watch specific namespaces - FOREGROUND MODE
./k8s-healer -n frontend,backend

# Watch namespaces matching wildcard patterns - FOREGROUND MODE
./k8s-healer -n 'app-*-dev,tools-*'

# Use an alternate kubeconfig - FOREGROUND MODE
./k8s-healer -k /etc/k8s/admin.conf -n default

# Run in FOREGROUND mode (non-daemon) - output to terminal
./k8s-healer -k ~/.kube/config -n default
# Press Ctrl+C to stop

# Run in DAEMON mode (background) - output to log file
./k8s-healer start -k ~/.kube/config -n default
# Use './k8s-healer stop' to stop

# Apply a custom healing cooldown period (5 minutes)
./k8s-healer --heal-cooldown 5m -n production

# Enable VM healing for OpenShift clusters
./k8s-healer --enable-vm-healing -n production

# Combine VM healing with custom cooldown
./k8s-healer --enable-vm-healing --heal-cooldown 15m

# CRD cleanup is enabled by default (includes VMs, VMIs, migrations, replicasets, and data volumes/VM disks)
./k8s-healer -n default

# CRD cleanup with custom resources (overrides defaults)
./k8s-healer --crd-resources "virtualmachines.kubevirt.io/v1,datavolumes.cdi.kubevirt.io" -n default

# CRD cleanup with custom stale age threshold and wildcard namespaces
./k8s-healer --crd-resources "virtualmachines.kubevirt.io" --stale-age 30m -n "test-*"

# CRD cleanup with multiple namespace patterns
./k8s-healer --crd-resources "virtualmachines.kubevirt.io/v1" -n "test-*,dev-*,default"

# Namespace polling is enabled by default - just provide the pattern
./k8s-healer --namespace-pattern "test-*" -n default

# Namespace polling with custom interval (enabled by default)
./k8s-healer --namespace-pattern "e2e-*" --namespace-poll-interval 15s -n default

# Disable namespace polling if not needed
./k8s-healer --enable-namespace-polling=false -n default

# Namespace polling to protect test pods and optimize resources for them (enabled by default)
./k8s-healer \
  --namespace-pattern "test-*" \
  --enable-resource-optimization \
  -k ~/.kube/config \
  -n default

# Disable CRD cleanup if needed
./k8s-healer --enable-crd-cleanup=false -n default

# Full test environment setup: pod healing + CRD cleanup + resource optimization (all enabled by default)
./k8s-healer --stale-age 6m -n default

# Enable resource optimization with custom strain threshold (25% of nodes)
./k8s-healer --enable-resource-optimization --strain-threshold 25.0 -n default

# Disable resource optimization if not needed
./k8s-healer --enable-resource-optimization=false -n default

# Resource creation throttling is enabled by default - warns when resources are created during cluster strain
./k8s-healer --enable-resource-creation-throttling -n default

# Disable resource creation throttling if not needed
./k8s-healer --enable-resource-creation-throttling=false -n default

# Run in foreground mode (non-daemon) - output goes to terminal
./k8s-healer -k ~/.kube/config -n default

# Run as background daemon (CRD cleanup enabled by default)
./k8s-healer start --crd-resources "virtualmachines.kubevirt.io/v1" -n default

# Check daemon status
./k8s-healer status

# Stop daemon
./k8s-healer stop

# Run with custom PID and log files
./k8s-healer start --pid-file /var/run/k8s-healer.pid --log-file /var/log/k8s-healer.log -n default

# Or use the convenience script (recommended)
./run-healer.sh start
./run-healer.sh status
./run-healer.sh logs -f
./run-healer.sh stop

# Discover all available API endpoints and resources in the cluster
./k8s-healer --discover
./k8s-healer --discover -k ~/.kube/config

# Discover endpoints and filter for KubeVirt-specific resources
./k8s-healer --discover | grep -A 20 "KubeVirt"

# Watch namespaces with prefix-based discovery and exclude specific namespaces from deletion
./k8s-healer -n test-123 -e test-keep,test-important
# This will discover and watch: test-123, test-456, test-789, test-keep, test-important
# But will only delete resources from: test-123, test-456, test-789
# test-keep and test-important are monitored but protected from deletion

# Watch namespaces with wildcard pattern and exclude namespaces
./k8s-healer -n "test-*" -e test-keep
# All test-* namespaces are discovered and watched
# test-keep is excluded from resource deletion
# Stale namespaces matching test-* (older than 6 minutes) are automatically deleted
# test-keep is never deleted, even if it's old
```

------------------------------------------------------------------------

## 🔄 Running Modes

k8s-healer can run in two modes:

### Foreground Mode (Non-Daemon)

Run directly in the terminal - output goes to stdout/stderr. Press `Ctrl+C` to stop.

```bash
# Run in foreground mode
./k8s-healer -k ~/.kube/config -n default

# Or with the convenience script (without 'start' command)
./run-healer.sh --source-kubeconfig ~/.kube/config start --foreground
```

**Use foreground mode when:**
- You want to see real-time output in the terminal
- You're debugging or testing
- You want to manually control when it stops (Ctrl+C)

### Daemon Mode (Background)

Run as a background process - output goes to a log file. Use `stop` command to terminate.

```bash
# Start as daemon
./k8s-healer start -k ~/.kube/config -n default

# Or with the convenience script
./run-healer.sh start
```

**Use daemon mode when:**
- Long-running monitoring in test environments
- CI/CD pipelines that need continuous cluster maintenance
- Production environments where you want the healer to run unattended
- You want it to run in the background while you do other work

### Daemon Commands

-   **`start`**: Start k8s-healer as a background daemon
-   **`stop`**: Stop the running daemon gracefully (sends SIGTERM, then SIGKILL if needed)
-   **`status`**: Check if the daemon is currently running
-   **`restart`**: Stop and start the daemon
-   **`logs`**: View daemon logs

### Stopping the Daemon

There are several ways to stop the daemon:

**Using the convenience script (recommended):**
```bash
./run-healer.sh stop
```

**Using the k8s-healer binary directly:**
```bash
./k8s-healer stop
```

**Manual stop (if needed):**
```bash
# Find the PID
cat .k8s-healer.pid

# Send termination signal
kill $(cat .k8s-healer.pid)

# Or force kill if needed
kill -9 $(cat .k8s-healer.pid)
```

### Daemon Behavior

When running as a daemon:
- The process runs in the background (detached from terminal)
- All output is redirected to a log file (default: `/tmp/k8s-healer.log`)
- A PID file is created (default: `/tmp/k8s-healer.pid`) for process management
- The daemon responds to `SIGTERM` for graceful shutdown
- If graceful shutdown fails, `SIGKILL` is sent after a timeout

### Example Daemon Usage

```bash
# Start daemon (CRD cleanup enabled by default)
./k8s-healer start \
  --crd-resources "virtualmachines.kubevirt.io/v1,datavolumes.cdi.kubevirt.io" \
  --stale-age 6m \
  -n default

# Check if daemon is running
./k8s-healer status

# View daemon logs
tail -f /tmp/k8s-healer.log

# Stop the daemon gracefully
./k8s-healer stop

# Or using the convenience script
./run-healer.sh stop
```

### Direct Daemon Mode

You can also run directly in daemon mode (without using the `start` subcommand):

```bash
# Run directly as daemon (forks immediately, CRD cleanup enabled by default)
./k8s-healer --daemon --crd-resources "virtualmachines.kubevirt.io/v1" -n default
```

Note: Using the `start` subcommand is recommended as it provides better process management.

### Convenience Script

For easier management, a shell script (`run-healer.sh`) is provided that handles all the configuration:

```bash
# Start as daemon (background mode)
./run-healer.sh start

# Start in foreground mode (terminal output, Ctrl+C to stop)
./run-healer.sh start --foreground

# Check status
./run-healer.sh status

# View logs (last 50 lines)
./run-healer.sh logs

# Follow logs in real-time
./run-healer.sh logs -f

# Stop the daemon
./run-healer.sh stop

# Restart the daemon
./run-healer.sh restart
```

The script automatically:
- Uses the kubeconfig from `.kubeconfigs/test-config` (copies from source if missing)
- Enables CRD cleanup with KubeVirt resources
- Sets stale age to 6 minutes
- Manages PID and log files in the project directory
- Provides colored output and error handling

**Note**: The kubeconfig file is copied from a source location (specified via `--source-kubeconfig` flag) 
into `.kubeconfigs/test-config` in the k8s-healer project directory. If the file doesn't exist, 
the script will attempt to copy it automatically on first run.

You can specify a different source kubeconfig location using the `--source-kubeconfig` flag:

```bash
./run-healer.sh --source-kubeconfig ~/.kube/config start
```

------------------------------------------------------------------------

## 🔄 Example Output

### Cluster Information Display (On Connection)
```
======================================================================
🔗 Connected to Kubernetes Cluster
======================================================================
📦 Kubernetes Version: v1.28.0
   Platform: linux/amd64
🌐 Cluster Host: https://api.example.com:6443
🔑 Current Context: my-context
   Cluster: my-cluster
   Server: https://api.example.com:6443
🖥️  Nodes: 6 total (6 ready)
📁 Namespaces: 3 namespace(s) - [default, test-12345, test-67890]

⚙️  Enabled Features:
   • VM Healing: ✅ Enabled
   • CRD Cleanup: ✅ Enabled (15 resource types)
   • Resource Optimization: ✅ Enabled (threshold: 30.0%)
   • Resource Creation Throttling: ✅ Enabled
   • Namespace Polling: ✅ Enabled (pattern: test-*, interval: 5s)

🔧 Configuration:
   • Stale Age Threshold: 6m0s
   • Heal Cooldown: 10m0s
   • Cleanup Finalizers: ✅ Enabled
======================================================================
```

### Pod Healing
    [Check] 🚨 Pod prod/api-7d8f9 failed check: CrashLoopBackOff (Restarts: 4).
    !!! HEALING ACTION REQUIRED !!!
        Pod: prod/api-7d8f9
        Reason: Persistent CrashLoopBackOff (Restarts: 4)
    [SUCCESS] ✅ Deleted pod prod/api-7d8f9. Controller is expected to recreate the Pod immediately.
    !!! HEALING ACTION COMPLETE !!!

    [SKIP] ⏳ Pod prod/api-7d8f9 was healed 120 seconds ago — skipping re-heal.

### VM Healing (OpenShift)
    [Check] 🚨 VM default/pw-bulk-vm-1761119925289-1 failed check: ErrorUnschedulable.
    !!! VM HEALING ACTION REQUIRED !!!
        VM: default/pw-bulk-vm-1761119925289-1
        Reason: VM ErrorUnschedulable - cannot be scheduled to any node
    [SUCCESS] ✅ Deleted VM default/pw-bulk-vm-1761119925289-1. Controller should recreate it.
    !!! VM HEALING ACTION COMPLETE !!!

### Node Healing (OpenShift)
    [Check] 🚨 Node worker-1 failed check: NotReady for 6m30s.
    !!! NODE HEALING ACTION REQUIRED !!!
        Node: worker-1
        Reason: Node NotReady for 6m30s
    [INFO] 🔄 Attempting to drain node worker-1...
    [INFO] ✅ Evicted pod app/api-7d8f9 from node worker-1
    [SUCCESS] ✅ Deleted node worker-1. Machine controller should recreate it.
    !!! NODE HEALING ACTION COMPLETE !!!

### Resource Optimization
    [INFO] ⚠️ Cluster under strain: 35.0% of nodes (2/6) under resource pressure
    [INFO] 🔍 Nodes under pressure: worker-1, worker-3
    
    !!! RESOURCE OPTIMIZATION ACTION REQUIRED !!!
        Pod: default/test-pod-abc123
        Reason: Pod OOMKilled (cluster strain: 35.0%)
        Cluster Strain: 35.0% (2/6 nodes under pressure)
    [SUCCESS] ✅ Evicted pod default/test-pod-abc123 for resource optimization
    !!! RESOURCE OPTIMIZATION ACTION COMPLETE !!!

    !!! RESOURCE OPTIMIZATION ACTION REQUIRED !!!
        Pod: test-ns/playwright-test-pod-xyz789
        Reason: High restart count (7) indicating resource pressure (cluster strain: 35.0%)
        Cluster Strain: 35.0% (2/6 nodes under pressure)
    [SUCCESS] ✅ Evicted pod test-ns/playwright-test-pod-xyz789 for resource optimization
    !!! RESOURCE OPTIMIZATION ACTION COMPLETE !!!

### CRD Cleanup
    [INFO] ✨ New CRD resource created: virtualmachines/default/test-vm-123 (age: 2s)
    [INFO] ✨ New CRD resource created: datavolumes/test-ns/datavolume-456 (age: 1s)
    
    [Check] 🚨 CRD Resource default/test-vm-123 failed check: Older than stale age threshold (2h15m old).
    !!! CRD CLEANUP ACTION REQUIRED !!!
        Resource: virtualmachines/default/test-vm-123
        Reason: Resource older than stale age threshold (2h15m old)
    [INFO] 🔄 Removing finalizers from virtualmachines/default/test-vm-123...
    [SUCCESS] ✅ Removed finalizers from virtualmachines/default/test-vm-123
    [SUCCESS] ✅ Deleted stale resource virtualmachines/default/test-vm-123
    !!! CRD CLEANUP ACTION COMPLETE !!!

    [Check] 🚨 CRD Resource test-ns/datavolume-456 failed check: Stuck in terminating state for 1h30m (likely stuck finalizers).
    !!! CRD CLEANUP ACTION REQUIRED !!!
        Resource: datavolumes/test-ns/datavolume-456
        Reason: Stuck in terminating state for 1h30m (likely stuck finalizers: [cdi.kubevirt.io/dataVolume])
    [INFO] 🔄 Removing finalizers from datavolumes/test-ns/datavolume-456...
    [SUCCESS] ✅ Removed finalizers from datavolumes/test-ns/datavolume-456
    [SUCCESS] ✅ Deleted stale resource datavolumes/test-ns/datavolume-456
    !!! CRD CLEANUP ACTION COMPLETE !!!

    [SKIP] ⏭️ Resource virtualmachines/test-ns/test-vm-789 no longer exists, skipping deletion

### Daemon Management
    $ ./k8s-healer start --crd-resources "virtualmachines.kubevirt.io/v1" -n default
    k8s-healer started as daemon (PID: 12345)
    Log file: /tmp/k8s-healer.log
    PID file: /tmp/k8s-healer.pid

    $ ./k8s-healer status
    k8s-healer is running (PID: 12345)

    $ ./k8s-healer stop
    k8s-healer daemon stopped (PID: 12345)

### Resource Optimization with Test Pod Protection
    [INFO] ⚠️ Cluster under strain: 35.0% of nodes (2/6) under resource pressure
    [INFO] ⚠️ Test pod test-12345/playwright-test-1 needs resources (OOMKilled: true, Restarts: 3, Pending: false)
    [INFO] 🎯 Prioritizing test namespace pods - considering evicting 8 non-test pod(s) to free resources
    !!! RESOURCE OPTIMIZATION ACTION REQUIRED !!!
        Pod: default/app-pod-1 (non-test namespace)
        Reason: Pod OOMKilled (cluster strain: 35.0%)
        Cluster Strain: 35.0% (2/6 nodes under pressure)
        Action: Evicting to free resources for test namespace pods
    [SUCCESS] ✅ Evicted pod default/app-pod-1 for resource optimization
    !!! RESOURCE OPTIMIZATION ACTION COMPLETE !!!

### Namespace Management
    [INFO] 🔍 Starting namespace polling for pattern: test-* (interval: 5s)
    [INFO] ✨ Discovered 2 new namespace(s) matching pattern 'test-*': [test-456, test-789]
    [INFO] ✅ Started watching new namespace: test-456
    [INFO] ✅ Started watching new namespace: test-789
    
    [INFO] 🗑️  Detected 1 deleted namespace(s) matching pattern: [test-456]
    [INFO] ⏹️  Stopped watching deleted namespace: test-456
    
    [INFO] 🗑️  Found 1 stale namespace(s) matching pattern (older than 6m0s): [test-789]
    !!! NAMESPACE CLEANUP ACTION REQUIRED !!!
        Namespace: test-789
        Age: 10m0s (threshold: 6m0s)
    [SUCCESS] ✅ Deleted stale namespace test-789
    !!! NAMESPACE CLEANUP ACTION COMPLETE !!!
    
    [MONITOR] 🔍 Pod test-keep/unhealthy-pod is unhealthy (CrashLoopBackOff) but namespace is excluded from deletion
    [MONITOR] 🔍 VM test-important/old-vm is older than threshold (6m0s) but namespace is excluded from deletion

### Resource Creation Throttling
    [INFO] ⚠️ Cluster under strain: 35.0% of nodes (2/6) under resource pressure
    [WARN] ⚠️ New pod created in test namespace during cluster strain: test-12345/playwright-test-1 (strain: 35.0%)
    [INFO] 💡 Consider reducing parallel test execution or waiting for cluster to recover
    
    [WARN] 🚨 New virtualmachine created during cluster strain: default/app-vm-1 (strain: 35.0%)
    [INFO] 💡 Resource creation throttling active - consider deferring resource creation until cluster recovers
    [INFO] 📊 Cluster status: 2/6 nodes under pressure: [node-1, node-2]

### VM Cleanup (Age-Based)
    [MONITOR] 🔍 VM pw-test-ns/pw-vm-customize-it-1769465109327-8aya health check: VM ErrorUnschedulable for 2m30s
    !!! VM CLEANUP ACTION REQUIRED !!!
        VM: pw-test-ns/pw-vm-customize-it-1769465109327-8aya
        Age: 6m15s (threshold: 6m0s)
        Health Status: VM ErrorUnschedulable for 2m30s (cannot be scheduled to any node)
    [SUCCESS] ✅ Deleted VM pw-test-ns/pw-vm-customize-it-1769465109327-8aya
    !!! VM CLEANUP ACTION COMPLETE !!!

------------------------------------------------------------------------

## 🧠 Resource Optimization Use Cases

The resource optimization feature is particularly useful for:

-   **Heavy Test Workloads:** When running parallel Playwright tests or other
    intensive test suites, the cluster can become resource-constrained. Resource
    optimization helps keep the cluster healthy by evicting problematic pods.

-   **Development Environments:** Automatically manages resource pressure in
    development/test clusters where multiple developers are running workloads
    simultaneously.

-   **CI/CD Pipelines:** Ensures test pipelines can continue running even when
    the cluster is under strain by proactively managing resource-constrained pods.

-   **Preventing Cascading Failures:** By evicting resource-constrained pods
    early, prevents them from causing broader cluster instability.

## 🧠 Why the Cooldown Matters

Without a cooldown mechanism, a continuously crashing Pod could cause a
"healing storm" --- a rapid delete/recreate cycle that strains the
control plane and hides deeper issues (like bad configs or image pull
errors).

The **healing cooldown** ensures each Pod has a grace period to
stabilize before another healing attempt.

For VM healing, the cooldown prevents rapid node deletion cycles that
could destabilize the cluster.

For CRD cleanup, the cooldown prevents repeatedly attempting to clean
the same resource if it keeps getting recreated or if cleanup fails.

## 🧹 CRD Cleanup Use Cases

The CRD cleanup feature is particularly useful for:

-   **Parallel Test Execution:** When running tests in parallel, stale
    resources from previous test runs can interfere with new tests.
    The janitor mode ensures the cluster stays clean.

-   **CI/CD Pipelines:** Automated cleanup of test artifacts ensures
    each test run starts with a clean slate.

-   **Development Environments:** Developers can run the healer in the
    background to automatically clean up resources that get stuck
    during development and testing.

-   **Resource Leak Prevention:** Prevents accumulation of stale
    resources that consume cluster resources unnecessarily.

**Comprehensive CRD resources included by default** (based on KubeVirt/OpenShift test environments):

**KubeVirt Core Resources:**
-   `virtualmachines.kubevirt.io/v1` - VirtualMachine resources
-   `virtualmachineinstances.kubevirt.io/v1` - VirtualMachineInstance resources
-   `virtualmachineinstancemigrations.kubevirt.io/v1` - VM migration resources (can get stuck)
-   `virtualmachineinstancereplicasets.kubevirt.io/v1` - VMI replica sets

**CDI Resources (Storage/Disks):**
-   `datavolumes.cdi.kubevirt.io/v1beta1` - DataVolume resources (VM disks)
-   `datasources.cdi.kubevirt.io/v1beta1` - DataSource resources (bootable volumes)

**Snapshot & Clone Resources:**
-   `virtualmachinesnapshots.snapshot.kubevirt.io/v1beta1` - VM snapshots
-   `virtualmachineclones.clone.kubevirt.io/v1alpha1` - VM clones

**Instance Types & Preferences:**
-   `virtualmachineinstancetypes.instancetype.kubevirt.io/v1beta1` - Namespace-scoped instance types
-   `virtualmachineclusterinstancetypes.instancetype.kubevirt.io/v1beta1` - Cluster-scoped instance types
-   `virtualmachinepreferences.instancetype.kubevirt.io/v1beta1` - Namespace-scoped preferences
-   `virtualmachineclusterpreferences.instancetype.kubevirt.io/v1beta1` - Cluster-scoped preferences

**Migration Policies & Plans:**
-   `migrationpolicies.migrations.kubevirt.io/v1alpha1` - Migration policies
-   `virtualmachinemigrationplans.kubevirt.io/v1alpha1` - VM migration plans

**Network Resources:**
-   `network-attachment-definitions.k8s.cni.cncf.io/v1` - Network attachment definitions
-   `userdefinednetworks.k8s.ovn.org/v1` - User-defined networks
-   `clusteruserdefinednetworks.k8s.ovn.org/v1` - Cluster user-defined networks

**OpenShift Resources:**
-   `templates.template.openshift.io/v1` - OpenShift templates

**Note:** Core Kubernetes resources (Secrets, ConfigMaps, Services, PersistentVolumeClaims, Namespaces) are not CRDs but can be monitored separately if needed.

------------------------------------------------------------------------

## 🧰 Extensibility

You can easily extend **k8s-healer** by: 
- Adding new unhealthy conditions in `util.IsUnhealthy()`, `util.IsNodeUnhealthy()`, and `util.IsCRDResourceStale()`. 
- Adjusting thresholds or strategies in `util.DefaultRestartThreshold`, node thresholds, and stale age thresholds. 
- Integrating logging or Prometheus metrics for observability.
- Adding custom healing strategies for different failure types.
- Adding support for additional CRD resource types by extending the resource detection logic.

------------------------------------------------------------------------

## 🧪 Testing

k8s-healer includes comprehensive test coverage for core functionality:

### Running Tests

```bash
# Run all tests
go test ./pkg/...

# Run tests with verbose output
go test -v ./pkg/...

# Run tests with coverage
go test -coverprofile=coverage.out ./pkg/...
go tool cover -html=coverage.out -o coverage.html

# Or use the Makefile
make test
make test-verbose
make test-coverage
```

### Test Coverage

The test suite covers:
- **Utility Functions** (`pkg/util/checks_test.go`): Tests for pod health checks, node health checks, VM health checks, CRD staleness detection, cluster strain detection, and resource optimization logic
- **Daemon Functions** (`pkg/daemon/daemon_test.go`): Tests for PID file management, log file redirection, and daemon utilities
- **Healer Logic** (`pkg/healer/healer_test.go`): Tests for healer initialization, cluster info display, namespace pattern matching, and resource cleanup logic
- **Health Check System** (`pkg/healthcheck/healthcheck_test.go`): Tests for cluster health checks, KubeVirt CRD availability, API connectivity, and health status formatting

### Test Structure

- Unit tests use standard Go testing package
- Kubernetes clients are mocked using `fake` clients from `k8s.io/client-go`
- Tests are organized by package and cover both happy paths and edge cases

## 🤝 Contributing

We welcome contributions! Please see [CONTRIBUTING.md](./CONTRIBUTING.md) for:
- Project evolution and design principles
- Development patterns and guidelines
- How features were developed and why
- Guidelines for adding new features

The contributing guide includes a summary of the prompts and decisions that shaped the project's evolution, helping new contributors understand the design philosophy.

**Testing Requirements:**
- All new features should include appropriate test coverage
- Run `make test` before submitting PRs
- Aim for at least 80% test coverage for new code

------------------------------------------------------------------------

## 📄 License

[MIT](./LICENSE) © 2025 Bruno Maio