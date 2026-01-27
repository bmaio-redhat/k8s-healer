# Contributing to k8s-healer

This document provides a summary of the development prompts and evolution of the k8s-healer project to help contributors understand the project's design philosophy and implementation approach.

## Project Evolution Summary

### Initial Core Functionality

The project started as a Kubernetes pod healing tool with the following core features:

1. **Pod Health Monitoring**: Watch Kubernetes namespaces for unhealthy pods using Kubernetes Informers
2. **Automatic Healing**: Delete pods stuck in failure states (CrashLoopBackOff, ImagePullBackOff) to allow controllers to recreate them
3. **Cooldown Mechanism**: Prevent healing loops by implementing a cooldown period between healing attempts
4. **Multi-Namespace Support**: Support for watching multiple namespaces with wildcard pattern matching

### Expansion to General-Purpose Test Environment Janitor

The project evolved to become a general-purpose janitor for test environments, specifically designed to support parallel test runs for projects like kubevirt-ui:

**Key Prompts That Shaped This Evolution:**

1. **"can we make this a bit more general purpose? the idea would for the project at /home/bmaio/Developer/Projects/kubevirt-ui/ run tests in parallel while this tool monitors the targeted cluster and acts as a janitor for every stale CRD in order for the tests run as smoothly as possible"**
   - Result: Added comprehensive CRD cleanup functionality
   - Monitors and cleans up stale Custom Resource Definitions (CRDs)
   - Supports configurable resource types and stale age thresholds
   - Includes finalizer cleanup for stuck resources

2. **"any chance we can run this in the background as a daemon, maybe provide a cli flag to enable that option"**
   - Result: Implemented daemon mode with PID file management and log redirection
   - Added daemon subcommands: `start`, `stop`, `status`
   - Created `pkg/daemon/daemon.go` package for daemonization logic

3. **"lets consolidate this into a shell script"**
   - Result: Created `run-healer.sh` convenience script
   - Handles kubeconfig management, building, and daemon lifecycle
   - Supports `--source-kubeconfig` flag for flexible kubeconfig sourcing
   - Provides `--foreground` flag for non-daemon execution

4. **"make all cleanups enabled by default"**
   - Result: Changed defaults so `--enable-vm-healing` and `--enable-crd-cleanup` default to `true`
   - Makes the tool more useful out-of-the-box for test environments

5. **"lets copy the referenced kubeconfig file into a .kubeconfig folder on the root and target that copied file for auth"**
   - Result: Implemented kubeconfig copying mechanism in `run-healer.sh`
   - Copies source kubeconfig to local `.kubeconfigs/` directory (git-ignored)
   - Ensures portable and secure kubeconfig management

6. **"also, reduce staleness value to 5 mins"** → later changed to **"lets increase the stale timeout to 6 mins"**
   - Result: Default stale age set to 6 minutes
   - Balances between allowing tests to complete and cleaning up truly stale resources

### Resource Optimization and Test Pod Protection

7. **"can we leverage the pods on which the test namespaces are acting un so they always have the best possible resource allocation? even if we move resources from other pods not used by tests"**
   - Result: Implemented test namespace pod protection
   - Test namespace pods are never evicted
   - Non-test pods are evicted to free resources for test pods
   - Cross-namespace resource optimization
   - Priority-based eviction (system-critical pods protected)

8. **"can we throttle resource creation for a bit if the cluster is under heavy load?"**
   - Result: Added resource creation throttling feature
   - Monitors new resource creation (pods, CRDs)
   - Warns when resources are created during cluster strain
   - Differentiates between test and non-test namespace resources
   - Provides actionable guidance to operators

### VM-Specific Optimizations

9. **"lets skip the deletion of vms since most tests will do self cleanup, lets keep the monitoring on other resources, for vms only apply a deletion if a vm exists for more than 6 mins"**
   - Result: Changed VM deletion strategy
   - VMs are monitored but only deleted if older than 6 minutes
   - Allows tests to perform self-cleanup
   - Still cleans up truly stale VMs

10. **"in case of [Check] 🚨 VM pw-test-ns/pw-vm-customize-it-1769465109327-8aya failed check: ErrorUnschedulable. lets apply the cleanup after 30 seconds"** → later changed to **"30 seconds is too quick, lets make it 3 mins, in some cases a stale resource is still enough for the respectve test case to go through"**
    - Result: Special handling for ErrorUnschedulable VMs
    - VMs in ErrorUnschedulable state are cleaned up after 3 minutes
    - Faster than general stale age (6 minutes) but allows time for recovery

### Dynamic Namespace Discovery

11. **"can we include cli log for when crds are being created on the provided namespace? also, could we use wildcards to target multiple namespaces?"**
    - Result: Added CRD creation logging
    - Tracks and logs new CRD resource creation
    - Wildcard namespace support was already implemented, verified and documented
    - Added `TrackedCRDs` map to track seen resources

12. **Namespace Polling Feature** (implicitly requested through namespace pattern usage)
    - Result: Added dynamic namespace discovery
    - Polls for new namespaces matching patterns (e.g., `test-*`)
    - Automatically starts watching newly discovered namespaces
    - Supports configurable poll intervals

## Design Principles

Based on the evolution above, the following design principles guide the project:

1. **Test Environment First**: The tool is optimized for test environments where parallel test execution requires clean cluster state
2. **Self-Cleanup Friendly**: Prefers allowing tests to self-cleanup over aggressive deletion
3. **Resource Protection**: Test namespace resources are protected and prioritized
4. **Observability**: Comprehensive logging of all actions and resource state changes
5. **Configurability**: Most behaviors are configurable via CLI flags with sensible defaults
6. **Safety**: Cooldown periods and safeguards prevent healing storms and resource thrashing
7. **Extensibility**: Modular design allows easy addition of new resource types and healing strategies

## Key Implementation Patterns

### Informer-Based Monitoring
- Uses Kubernetes Informers for efficient, event-driven resource monitoring
- No polling overhead for pod/node state changes
- Periodic checks only for CRD resources and cluster strain

### Graceful Degradation
- Falls back to direct deletion if eviction fails
- Continues operating even if some resource types are unavailable
- Skips resources that can't be accessed rather than failing entirely

### State Tracking
- Tracks recently healed/cleaned resources to prevent loops
- Maintains cluster strain state for throttling decisions
- Tracks seen CRD resources for creation logging

### Resource Prioritization
- System-critical pods: Never evicted
- Test namespace pods: Protected, never evicted
- Non-test pods: Can be evicted based on priority and resource constraints

## Contributing Guidelines

When contributing to this project:

1. **Follow Existing Patterns**: Use the same patterns for resource monitoring, healing, and logging
2. **Test Environment Focus**: Consider how changes affect parallel test execution
3. **Safety First**: Always include cooldown mechanisms and safeguards
4. **Comprehensive Logging**: Log all significant actions with clear, actionable messages
5. **Configuration**: Make new features configurable via CLI flags with sensible defaults
6. **Documentation**: Update README.md and this CONTRIBUTING.md when adding features

## Adding New Features

When adding new features, consider:

1. **Resource Types**: Can the feature be extended to other resource types?
2. **Test Impact**: How does this affect test environments and parallel execution?
3. **Safety**: Are there safeguards to prevent resource thrashing or healing storms?
4. **Observability**: Is the feature's behavior clearly logged and observable?
5. **Configuration**: Should this be configurable or have a default behavior?

## Code Organization

- `cmd/main.go`: CLI interface, flag definitions, and command setup
- `pkg/healer/healer.go`: Core healing logic, resource monitoring, and orchestration
- `pkg/util/checks.go`: Health check utilities, resource staleness detection, cluster strain detection
- `pkg/daemon/daemon.go`: Daemonization logic, PID file management, log redirection
- `run-healer.sh`: Convenience script for easier tool management

## Testing Considerations

The tool is designed for use in test environments. When testing changes:

1. Test with parallel resource creation/deletion
2. Verify cooldown mechanisms prevent loops
3. Ensure test namespace resources are protected
4. Test daemon lifecycle (start, stop, restart)
5. Verify resource creation throttling during cluster strain
6. Test with various namespace patterns and wildcards

---

For questions or clarifications about the project's design and evolution, please refer to the git history or open an issue.
