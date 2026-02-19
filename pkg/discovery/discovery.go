package discovery

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// DiscoverClusterEndpoints discovers and displays all available API endpoints and resources
// in the Kubernetes cluster. It connects to the cluster using the provided kubeconfig path
// (or standard locations if empty) and outputs formatted information about all API groups,
// versions, and resources to stdout.
func DiscoverClusterEndpoints(kubeconfigPath string) error {
	var config *rest.Config
	var err error

	// Build Kubernetes config
	if kubeconfigPath != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	} else {
		config, err = clientcmd.BuildConfigFromFlags("", "")
	}

	if err != nil {
		return fmt.Errorf("failed to build Kubernetes config: %w", err)
	}

	// Create clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes clientset: %w", err)
	}

	// Create discovery client
	// kubernetes.NewForConfig returns *kubernetes.Clientset, so we can access RESTClient() directly
	discoveryClient := discovery.NewDiscoveryClient(clientset.RESTClient())
	return DiscoverClusterEndpointsWithClient(config, discoveryClient)
}

// DiscoverClusterEndpointsWithClient runs discovery logic with the given config and discovery client.
// It is used by DiscoverClusterEndpoints and by tests with a fake discovery client.
func DiscoverClusterEndpointsWithClient(config *rest.Config, discoveryClient discovery.DiscoveryInterface) error {
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("🔍 Kubernetes Cluster Endpoint Discovery")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("🌐 Cluster Host: %s\n\n", config.Host)

	// Get server version
	serverVersion, err := discoveryClient.ServerVersion()
	if err == nil {
		fmt.Printf("📦 Kubernetes Version: %s\n", serverVersion.GitVersion)
		fmt.Printf("   Platform: %s/%s\n\n", serverVersion.Platform, serverVersion.GoVersion)
	}

	// Get API groups
	fmt.Println("📚 API Groups:")
	fmt.Println(strings.Repeat("-", 80))
	apiGroups, err := discoveryClient.ServerGroups()
	if err != nil {
		return fmt.Errorf("failed to discover API groups: %w", err)
	}

	// Sort groups for consistent output
	sort.Slice(apiGroups.Groups, func(i, j int) bool {
		return apiGroups.Groups[i].Name < apiGroups.Groups[j].Name
	})

	for _, group := range apiGroups.Groups {
		fmt.Printf("  • %s\n", group.Name)
		if len(group.Versions) > 0 {
			versions := make([]string, len(group.Versions))
			for i, v := range group.Versions {
				versions[i] = v.Version
			}
			fmt.Printf("    Versions: %s\n", strings.Join(versions, ", "))
		}
		if group.PreferredVersion.Version != "" {
			fmt.Printf("    Preferred Version: %s\n", group.PreferredVersion.Version)
		}
		fmt.Println()
	}

	// Get resources for each API group
	fmt.Println("📋 Resources by API Group:")
	fmt.Println(strings.Repeat("=", 80))

	for _, group := range apiGroups.Groups {
		if len(group.Versions) == 0 {
			continue
		}

		// Use preferred version or first available version
		version := group.PreferredVersion.Version
		if version == "" {
			version = group.Versions[0].Version
		}

		groupVersion := schema.GroupVersion{
			Group:   group.Name,
			Version: version,
		}

		resources, err := discoveryClient.ServerResourcesForGroupVersion(groupVersion.String())
		if err != nil {
			fmt.Printf("  ⚠️  %s/%s: Failed to get resources: %v\n", group.Name, version, err)
			continue
		}

		if len(resources.APIResources) == 0 {
			continue
		}

		fmt.Printf("\n📦 %s/%s\n", group.Name, version)
		fmt.Println(strings.Repeat("-", 80))

		// Sort resources for consistent output
		sort.Slice(resources.APIResources, func(i, j int) bool {
			return resources.APIResources[i].Name < resources.APIResources[j].Name
		})

		for _, resource := range resources.APIResources {
			resourceType := "Namespaced"
			if !resource.Namespaced {
				resourceType = "Cluster"
			}

			verbs := strings.Join(resource.Verbs, ", ")
			fmt.Printf("  • %s (%s)\n", resource.Name, resourceType)
			fmt.Printf("    Kind: %s, Verbs: [%s]\n", resource.Kind, verbs)
		}
	}

	// Check for KubeVirt specifically
	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Println("🔍 KubeVirt-Specific Resources:")
	fmt.Println(strings.Repeat("-", 80))

	kubevirtGroups := []string{"kubevirt.io", "cdi.kubevirt.io", "snapshot.kubevirt.io", "clone.kubevirt.io", "instancetype.kubevirt.io", "migrations.kubevirt.io"}
	kubevirtFound := false

	for _, groupName := range kubevirtGroups {
		for _, group := range apiGroups.Groups {
			if group.Name == groupName {
				kubevirtFound = true
				version := group.PreferredVersion.Version
				if version == "" && len(group.Versions) > 0 {
					version = group.Versions[0].Version
				}

				groupVersion := schema.GroupVersion{
					Group:   group.Name,
					Version: version,
				}

				resources, err := discoveryClient.ServerResourcesForGroupVersion(groupVersion.String())
				if err != nil {
					fmt.Printf("  ⚠️  %s/%s: Failed to get resources: %v\n", group.Name, version, err)
					continue
				}

				fmt.Printf("\n  📦 %s/%s\n", group.Name, version)
				for _, resource := range resources.APIResources {
					resourceType := "Namespaced"
					if !resource.Namespaced {
						resourceType = "Cluster"
					}
					fmt.Printf("    • %s (%s) - Kind: %s\n", resource.Name, resourceType, resource.Kind)
				}
				break
			}
		}
	}

	if !kubevirtFound {
		fmt.Println("  ⚠️  No KubeVirt API groups found - KubeVirt may not be installed")
	}

	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("✅ Endpoint discovery complete")
	fmt.Println()

	return nil
}
