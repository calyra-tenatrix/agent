// Package digitalocean provides DigitalOcean cloud provider implementation
package digitalocean

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"time"

	"github.com/digitalocean/godo"
)

// ClusterState represents the state of a Kubernetes cluster
type ClusterState string

const (
	ClusterStateProvisioning ClusterState = "provisioning"
	ClusterStateRunning      ClusterState = "running"
	ClusterStateDegraded     ClusterState = "degraded"
	ClusterStateError        ClusterState = "error"
	ClusterStateDeleting     ClusterState = "deleting"
	ClusterStateNotFound     ClusterState = "not_found"
)

// ClusterConfig holds configuration for creating a DOKS cluster
type ClusterConfig struct {
	Name       string // Cluster name
	TargetID   string // Tenatrix target ID (for tagging)
	VPCUUID    string // VPC to place the cluster in
	Region     string // Region for the cluster
	K8sVersion string // Kubernetes version (e.g., "1.29")
	NodeSize   string // Node size (e.g., "s-2vcpu-4gb")
	NodeCount  int    // Number of nodes in default pool
	AutoScale  bool   // Enable autoscaling
	MinNodes   int    // Min nodes for autoscaling
	MaxNodes   int    // Max nodes for autoscaling

	// Control Plane Firewall
	EnableFirewall bool     // Enable control plane firewall
	AllowedIPs     []string // IPs/CIDRs allowed to access K8s API
}

// ClusterInfo holds information about a running cluster
type ClusterInfo struct {
	ID         string
	Name       string
	State      ClusterState
	Region     string
	K8sVersion string
	Endpoint   string // API server endpoint
	VPCUUID    string
	CreatedAt  time.Time
	NodePools  []NodePoolInfo
}

// NodePoolInfo holds information about a node pool
type NodePoolInfo struct {
	ID        string
	Name      string
	Size      string
	NodeCount int
	AutoScale bool
	MinNodes  int
	MaxNodes  int
}

// DefaultClusterConfig returns sensible defaults for a Tenatrix cluster
func DefaultClusterConfig(targetID, vpcUUID, region string, agentIP string) ClusterConfig {
	// Use first 8 chars of target ID for readable name
	shortID := targetID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	// Default allowed IPs - agent's IP with /32 for single host
	allowedIPs := []string{}
	if agentIP != "" {
		allowedIPs = append(allowedIPs, fmt.Sprintf("%s/32", agentIP))
	}

	return ClusterConfig{
		Name:           fmt.Sprintf("tenatrix-%s", shortID),
		TargetID:       targetID,
		VPCUUID:        vpcUUID,
		Region:         region,
		K8sVersion:     "1.29", // Latest stable
		NodeSize:       "s-2vcpu-4gb",
		NodeCount:      2,
		AutoScale:      true,
		MinNodes:       1,
		MaxNodes:       5,
		EnableFirewall: true, // Secure by default
		AllowedIPs:     allowedIPs,
	}
}

// CreateCluster creates a new DOKS cluster
func (c *Client) CreateCluster(ctx context.Context, cfg ClusterConfig) (*ClusterInfo, error) {
	log.Printf("[DO] Creating Kubernetes cluster: %s in region %s", cfg.Name, cfg.Region)

	// Node pool configuration
	nodePool := &godo.KubernetesNodePoolCreateRequest{
		Name:      "default-pool",
		Size:      cfg.NodeSize,
		Count:     cfg.NodeCount,
		AutoScale: cfg.AutoScale,
		MinNodes:  cfg.MinNodes,
		MaxNodes:  cfg.MaxNodes,
		Tags:      []string{"tenatrix", fmt.Sprintf("target:%s", cfg.TargetID)},
	}

	// Control plane firewall configuration
	var controlPlaneFirewall *godo.KubernetesControlPlaneFirewall
	if cfg.EnableFirewall {
		enabled := true
		controlPlaneFirewall = &godo.KubernetesControlPlaneFirewall{
			Enabled:          &enabled,
			AllowedAddresses: cfg.AllowedIPs,
		}
		log.Printf("[DO] Control plane firewall enabled, allowed IPs: %v", cfg.AllowedIPs)
	}

	// Cluster creation request
	createReq := &godo.KubernetesClusterCreateRequest{
		Name:        cfg.Name,
		RegionSlug:  cfg.Region,
		VersionSlug: cfg.K8sVersion,
		VPCUUID:     cfg.VPCUUID,
		Tags:        []string{"tenatrix", fmt.Sprintf("target:%s", cfg.TargetID)},
		NodePools:   []*godo.KubernetesNodePoolCreateRequest{nodePool},
		// Private networking - cluster only accessible within VPC
		MaintenancePolicy: &godo.KubernetesMaintenancePolicy{
			StartTime: "04:00",
			Day:       godo.KubernetesMaintenanceDayAny,
		},
		// Enable HA for production workloads
		HA: false, // Start with non-HA for cost, can upgrade later
		// Control plane firewall - restrict API access
		ControlPlaneFirewall: controlPlaneFirewall,
	}

	cluster, _, err := c.client.Kubernetes.Create(ctx, createReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create cluster: %w", err)
	}

	log.Printf("[DO] Cluster creation initiated: %s (ID: %s)", cluster.Name, cluster.ID)

	return c.clusterToInfo(cluster), nil
}

// GetCluster retrieves information about a cluster by ID
func (c *Client) GetCluster(ctx context.Context, clusterID string) (*ClusterInfo, error) {
	cluster, _, err := c.client.Kubernetes.Get(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster: %w", err)
	}

	return c.clusterToInfo(cluster), nil
}

// FindClusterByName finds a cluster by name in the current region
func (c *Client) FindClusterByName(ctx context.Context, name string) (*ClusterInfo, error) {
	opt := &godo.ListOptions{PerPage: 100}

	clusters, _, err := c.client.Kubernetes.List(ctx, opt)
	if err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}

	for _, cluster := range clusters {
		if cluster.Name == name && cluster.RegionSlug == c.region {
			return c.clusterToInfo(cluster), nil
		}
	}

	return nil, nil // Not found (not an error)
}

// FindClusterByTargetID finds a cluster by Tenatrix target ID tag
func (c *Client) FindClusterByTargetID(ctx context.Context, targetID string) (*ClusterInfo, error) {
	opt := &godo.ListOptions{PerPage: 100}
	targetTag := fmt.Sprintf("target:%s", targetID)

	clusters, _, err := c.client.Kubernetes.List(ctx, opt)
	if err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}

	for _, cluster := range clusters {
		for _, tag := range cluster.Tags {
			if tag == targetTag {
				return c.clusterToInfo(cluster), nil
			}
		}
	}

	return nil, nil // Not found (not an error)
}

// WaitForClusterReady waits for the cluster to be in running state
func (c *Client) WaitForClusterReady(ctx context.Context, clusterID string, timeout time.Duration) (*ClusterInfo, error) {
	deadline := time.Now().Add(timeout)
	checkInterval := 30 * time.Second

	log.Printf("[DO] Waiting for cluster %s to be ready (timeout: %s)...", clusterID, timeout)

	for time.Now().Before(deadline) {
		cluster, err := c.GetCluster(ctx, clusterID)
		if err != nil {
			return nil, err
		}

		log.Printf("[DO] Cluster %s state: %s", clusterID, cluster.State)

		switch cluster.State {
		case ClusterStateRunning:
			log.Printf("[DO] Cluster %s is ready!", clusterID)
			return cluster, nil
		case ClusterStateError:
			return nil, fmt.Errorf("cluster is in error state")
		case ClusterStateProvisioning:
			// Continue waiting
		default:
			log.Printf("[DO] Unexpected cluster state: %s", cluster.State)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(checkInterval):
			// Continue polling
		}
	}

	return nil, fmt.Errorf("timeout waiting for cluster to be ready")
}

// GetKubeconfig retrieves the kubeconfig for connecting to the cluster
func (c *Client) GetKubeconfig(ctx context.Context, clusterID string) ([]byte, error) {
	config, _, err := c.client.Kubernetes.GetKubeConfig(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	return config.KubeconfigYAML, nil
}

// GetKubeconfigBase64 retrieves the kubeconfig as base64 encoded string
func (c *Client) GetKubeconfigBase64(ctx context.Context, clusterID string) (string, error) {
	config, err := c.GetKubeconfig(ctx, clusterID)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(config), nil
}

// DeleteCluster deletes a Kubernetes cluster
func (c *Client) DeleteCluster(ctx context.Context, clusterID string) error {
	log.Printf("[DO] Deleting cluster: %s", clusterID)

	_, err := c.client.Kubernetes.Delete(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("failed to delete cluster: %w", err)
	}

	log.Printf("[DO] Cluster deletion initiated: %s", clusterID)
	return nil
}

// ScaleNodePool scales a node pool to the specified count
func (c *Client) ScaleNodePool(ctx context.Context, clusterID, poolID string, count int) error {
	log.Printf("[DO] Scaling node pool %s to %d nodes", poolID, count)

	_, _, err := c.client.Kubernetes.UpdateNodePool(ctx, clusterID, poolID, &godo.KubernetesNodePoolUpdateRequest{
		Count: &count,
	})
	if err != nil {
		return fmt.Errorf("failed to scale node pool: %w", err)
	}

	return nil
}

// clusterToInfo converts godo cluster to ClusterInfo
func (c *Client) clusterToInfo(cluster *godo.KubernetesCluster) *ClusterInfo {
	info := &ClusterInfo{
		ID:         cluster.ID,
		Name:       cluster.Name,
		State:      mapClusterState(cluster.Status.State),
		Region:     cluster.RegionSlug,
		K8sVersion: cluster.VersionSlug,
		Endpoint:   cluster.Endpoint,
		VPCUUID:    cluster.VPCUUID,
		CreatedAt:  cluster.CreatedAt,
		NodePools:  make([]NodePoolInfo, 0, len(cluster.NodePools)),
	}

	for _, pool := range cluster.NodePools {
		info.NodePools = append(info.NodePools, NodePoolInfo{
			ID:        pool.ID,
			Name:      pool.Name,
			Size:      pool.Size,
			NodeCount: pool.Count,
			AutoScale: pool.AutoScale,
			MinNodes:  pool.MinNodes,
			MaxNodes:  pool.MaxNodes,
		})
	}

	return info
}

// mapClusterState maps DO cluster state to our ClusterState
func mapClusterState(state godo.KubernetesClusterStatusState) ClusterState {
	switch state {
	case godo.KubernetesClusterStatusProvisioning:
		return ClusterStateProvisioning
	case godo.KubernetesClusterStatusRunning:
		return ClusterStateRunning
	case godo.KubernetesClusterStatusDegraded:
		return ClusterStateDegraded
	case godo.KubernetesClusterStatusError:
		return ClusterStateError
	case godo.KubernetesClusterStatusDeleted:
		return ClusterStateDeleting
	case godo.KubernetesClusterStatusUpgrading:
		return ClusterStateProvisioning // Treat upgrading as provisioning
	default:
		return ClusterStateError
	}
}

// GetAvailableK8sVersions returns available Kubernetes versions
func (c *Client) GetAvailableK8sVersions(ctx context.Context) ([]string, error) {
	options, _, err := c.client.Kubernetes.GetOptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get K8s options: %w", err)
	}

	versions := make([]string, 0, len(options.Versions))
	for _, v := range options.Versions {
		versions = append(versions, v.Slug)
	}

	return versions, nil
}

// GetAvailableNodeSizes returns available node sizes for the region
func (c *Client) GetAvailableNodeSizes(ctx context.Context) ([]string, error) {
	options, _, err := c.client.Kubernetes.GetOptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get K8s options: %w", err)
	}

	sizes := make([]string, 0, len(options.Sizes))
	for _, s := range options.Sizes {
		sizes = append(sizes, s.Slug)
	}

	return sizes, nil
}
