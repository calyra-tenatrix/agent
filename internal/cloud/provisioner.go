// Package cloud provides cloud infrastructure provisioning
package cloud

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/calyra-tenatrix/agent/internal/cloud/digitalocean"
	"github.com/calyra-tenatrix/agent/internal/reconciler"
)

// ClusterProvisioner manages Kubernetes cluster lifecycle
// It handles lazy initialization - cluster is only created when first needed
type ClusterProvisioner struct {
	doClient  *digitalocean.Client
	targetID  string
	namespace string

	// Cluster state
	mu          sync.RWMutex
	clusterInfo *digitalocean.ClusterInfo
	vpcInfo     *digitalocean.VPCInfo
	k8sClient   *reconciler.K8sClient
	reconciler  *reconciler.Reconciler

	// Kubeconfig path
	kubeconfigPath string

	// Status callback for reporting to backend
	statusCallback func(status ProvisioningStatus)
}

// ProvisioningStatus represents the current provisioning state
type ProvisioningStatus struct {
	State       string    // "idle", "creating_vpc", "creating_cluster", "configuring", "ready", "error"
	Message     string    // Human readable status message
	ClusterID   string    // Cluster ID if available
	ClusterName string    // Cluster name
	Progress    int       // Progress percentage (0-100)
	Error       string    // Error message if State == "error"
	UpdatedAt   time.Time // Last update time
}

// ProvisionerConfig holds configuration for the provisioner
type ProvisionerConfig struct {
	DOToken        string // DigitalOcean API token
	TargetID       string // Tenatrix target ID
	Namespace      string // Kubernetes namespace for deployments
	KubeconfigDir  string // Directory to store kubeconfig (default: ~/.tenatrix)
	StatusCallback func(status ProvisioningStatus)
}

// NewClusterProvisioner creates a new cluster provisioner
func NewClusterProvisioner(ctx context.Context, cfg ProvisionerConfig) (*ClusterProvisioner, error) {
	// Create DO client
	doClient, err := digitalocean.NewClient(digitalocean.ClientConfig{
		Token: cfg.DOToken,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create DO client: %w", err)
	}

	// Detect environment (region, droplet ID)
	if err := doClient.DetectEnvironment(ctx); err != nil {
		log.Printf("[Provisioner] Warning: could not detect environment: %v", err)
	}

	// Set up kubeconfig directory
	kubeconfigDir := cfg.KubeconfigDir
	if kubeconfigDir == "" {
		homeDir, _ := os.UserHomeDir()
		kubeconfigDir = filepath.Join(homeDir, ".tenatrix")
	}

	if err := os.MkdirAll(kubeconfigDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create kubeconfig directory: %w", err)
	}

	namespace := cfg.Namespace
	if namespace == "" {
		namespace = "default"
	}

	p := &ClusterProvisioner{
		doClient:       doClient,
		targetID:       cfg.TargetID,
		namespace:      namespace,
		kubeconfigPath: filepath.Join(kubeconfigDir, "kubeconfig"),
		statusCallback: cfg.StatusCallback,
	}

	// Try to load existing cluster
	if err := p.loadExistingCluster(ctx); err != nil {
		log.Printf("[Provisioner] No existing cluster found: %v", err)
	}

	return p, nil
}

// EnsureCluster ensures a Kubernetes cluster exists and is ready
// This is the main entry point - call this before any reconciliation
func (p *ClusterProvisioner) EnsureCluster(ctx context.Context) (*reconciler.Reconciler, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// If we already have a working reconciler, return it
	if p.reconciler != nil && p.k8sClient != nil {
		// Verify connection is still healthy
		if err := p.k8sClient.CheckHealth(ctx); err == nil {
			return p.reconciler, nil
		}
		log.Printf("[Provisioner] K8s client unhealthy, will reconnect")
		p.reconciler = nil
		p.k8sClient = nil
	}

	// Try to find existing cluster
	if p.clusterInfo == nil {
		cluster, err := p.doClient.FindClusterByTargetID(ctx, p.targetID)
		if err != nil {
			return nil, fmt.Errorf("failed to search for existing cluster: %w", err)
		}

		if cluster != nil {
			log.Printf("[Provisioner] Found existing cluster: %s (state: %s)", cluster.Name, cluster.State)
			p.clusterInfo = cluster
		}
	}

	// If no cluster exists, create one
	if p.clusterInfo == nil {
		if err := p.provisionCluster(ctx); err != nil {
			return nil, fmt.Errorf("failed to provision cluster: %w", err)
		}
	}

	// Wait for cluster to be ready if it's still provisioning
	if p.clusterInfo.State != digitalocean.ClusterStateRunning {
		p.updateStatus(ProvisioningStatus{
			State:       "waiting",
			Message:     "Waiting for cluster to be ready...",
			ClusterID:   p.clusterInfo.ID,
			ClusterName: p.clusterInfo.Name,
			Progress:    60,
		})

		cluster, err := p.doClient.WaitForClusterReady(ctx, p.clusterInfo.ID, 15*time.Minute)
		if err != nil {
			p.updateStatus(ProvisioningStatus{
				State: "error",
				Error: fmt.Sprintf("Cluster failed to become ready: %v", err),
			})
			return nil, fmt.Errorf("cluster failed to become ready: %w", err)
		}
		p.clusterInfo = cluster
	}

	// Get kubeconfig and connect
	if err := p.connectToCluster(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to cluster: %w", err)
	}

	p.updateStatus(ProvisioningStatus{
		State:       "ready",
		Message:     "Cluster is ready for deployments",
		ClusterID:   p.clusterInfo.ID,
		ClusterName: p.clusterInfo.Name,
		Progress:    100,
	})

	return p.reconciler, nil
}

// provisionCluster creates a new cluster
func (p *ClusterProvisioner) provisionCluster(ctx context.Context) error {
	log.Printf("[Provisioner] No existing cluster found, creating new one...")

	// Step 1: Ensure VPC exists
	p.updateStatus(ProvisioningStatus{
		State:    "creating_vpc",
		Message:  "Setting up network (VPC)...",
		Progress: 10,
	})

	vpc, err := p.doClient.GetOrCreateVPC(ctx, p.targetID)
	if err != nil {
		p.updateStatus(ProvisioningStatus{
			State: "error",
			Error: fmt.Sprintf("Failed to setup VPC: %v", err),
		})
		return fmt.Errorf("failed to setup VPC: %w", err)
	}
	p.vpcInfo = vpc
	log.Printf("[Provisioner] Using VPC: %s (%s)", vpc.Name, vpc.UUID)

	// Step 2: Get agent's IP for firewall
	agentIP := ""
	if privateIP, err := p.doClient.GetDropletPrivateIP(ctx); err == nil {
		agentIP = privateIP
		log.Printf("[Provisioner] Agent private IP: %s (will be allowed in firewall)", agentIP)
	} else {
		log.Printf("[Provisioner] Warning: could not get agent IP for firewall: %v", err)
	}

	// Step 3: Create cluster
	p.updateStatus(ProvisioningStatus{
		State:    "creating_cluster",
		Message:  "Creating Kubernetes cluster (this takes 5-10 minutes)...",
		Progress: 20,
	})

	clusterCfg := digitalocean.DefaultClusterConfig(p.targetID, vpc.UUID, p.doClient.GetRegion(), agentIP)

	cluster, err := p.doClient.CreateCluster(ctx, clusterCfg)
	if err != nil {
		p.updateStatus(ProvisioningStatus{
			State: "error",
			Error: fmt.Sprintf("Failed to create cluster: %v", err),
		})
		return fmt.Errorf("failed to create cluster: %w", err)
	}

	p.clusterInfo = cluster
	log.Printf("[Provisioner] Cluster creation started: %s (ID: %s)", cluster.Name, cluster.ID)

	return nil
}

// connectToCluster retrieves kubeconfig and establishes connection
func (p *ClusterProvisioner) connectToCluster(ctx context.Context) error {
	p.updateStatus(ProvisioningStatus{
		State:       "configuring",
		Message:     "Connecting to cluster...",
		ClusterID:   p.clusterInfo.ID,
		ClusterName: p.clusterInfo.Name,
		Progress:    80,
	})

	// Get kubeconfig
	kubeconfig, err := p.doClient.GetKubeconfig(ctx, p.clusterInfo.ID)
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	// Save kubeconfig to file
	if err := os.WriteFile(p.kubeconfigPath, kubeconfig, 0600); err != nil {
		return fmt.Errorf("failed to save kubeconfig: %w", err)
	}
	log.Printf("[Provisioner] Kubeconfig saved to: %s", p.kubeconfigPath)

	// Create K8s client
	k8sClient, err := reconciler.NewK8sClient(reconciler.K8sClientConfig{
		Namespace:      p.namespace,
		InCluster:      false,
		KubeconfigPath: p.kubeconfigPath,
	})
	if err != nil {
		return fmt.Errorf("failed to create K8s client: %w", err)
	}

	// Verify connection
	if err := k8sClient.CheckHealth(ctx); err != nil {
		return fmt.Errorf("failed to connect to K8s API: %w", err)
	}

	p.k8sClient = k8sClient
	p.reconciler = reconciler.NewReconciler(k8sClient, p.targetID)

	log.Printf("[Provisioner] Successfully connected to Kubernetes cluster")
	return nil
}

// loadExistingCluster tries to load an existing cluster from saved state
func (p *ClusterProvisioner) loadExistingCluster(ctx context.Context) error {
	// Check if kubeconfig exists
	if _, err := os.Stat(p.kubeconfigPath); os.IsNotExist(err) {
		return fmt.Errorf("no saved kubeconfig found")
	}

	// Try to connect with existing kubeconfig
	k8sClient, err := reconciler.NewK8sClient(reconciler.K8sClientConfig{
		Namespace:      p.namespace,
		InCluster:      false,
		KubeconfigPath: p.kubeconfigPath,
	})
	if err != nil {
		return fmt.Errorf("failed to create K8s client from saved config: %w", err)
	}

	// Verify connection
	if err := k8sClient.CheckHealth(ctx); err != nil {
		return fmt.Errorf("saved kubeconfig is not valid: %w", err)
	}

	p.k8sClient = k8sClient
	p.reconciler = reconciler.NewReconciler(k8sClient, p.targetID)

	log.Printf("[Provisioner] Loaded existing cluster connection from saved kubeconfig")
	return nil
}

// updateStatus sends status update via callback
func (p *ClusterProvisioner) updateStatus(status ProvisioningStatus) {
	status.UpdatedAt = time.Now()
	if p.statusCallback != nil {
		p.statusCallback(status)
	}
}

// GetClusterInfo returns the current cluster information
func (p *ClusterProvisioner) GetClusterInfo() *digitalocean.ClusterInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.clusterInfo
}

// GetReconciler returns the reconciler if available
func (p *ClusterProvisioner) GetReconciler() *reconciler.Reconciler {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.reconciler
}

// IsClusterReady returns true if cluster is provisioned and connected
func (p *ClusterProvisioner) IsClusterReady() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.reconciler != nil && p.k8sClient != nil
}

// DeleteCluster deletes the cluster (use with caution!)
func (p *ClusterProvisioner) DeleteCluster(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.clusterInfo == nil {
		return fmt.Errorf("no cluster to delete")
	}

	if err := p.doClient.DeleteCluster(ctx, p.clusterInfo.ID); err != nil {
		return err
	}

	// Clean up local state
	p.clusterInfo = nil
	p.k8sClient = nil
	p.reconciler = nil
	os.Remove(p.kubeconfigPath)

	return nil
}

// GetDOClient returns the DO client for advanced operations
func (p *ClusterProvisioner) GetDOClient() *digitalocean.Client {
	return p.doClient
}
