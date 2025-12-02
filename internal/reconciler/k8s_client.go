// Package reconciler handles infrastructure reconciliation with Kubernetes
package reconciler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// K8sClient wraps the Kubernetes client with helper methods
// for managing Tenatrix-controlled resources
type K8sClient struct {
	clientset    *kubernetes.Clientset
	namespace    string
	managedLabel string // Label to identify Tenatrix-managed resources
}

// K8sClientConfig holds configuration for K8sClient
type K8sClientConfig struct {
	// Namespace is the default namespace for operations
	Namespace string
	// InCluster indicates whether to use in-cluster config
	InCluster bool
	// KubeconfigPath is the path to kubeconfig file (optional, used when not in-cluster)
	KubeconfigPath string
}

// NewK8sClient creates a new Kubernetes client wrapper
// It automatically detects whether running inside a cluster or locally
func NewK8sClient(cfg K8sClientConfig) (*K8sClient, error) {
	var config *rest.Config
	var err error

	if cfg.InCluster {
		// In-cluster configuration (when running inside Kubernetes)
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
		}
	} else {
		// Out-of-cluster configuration (for local development)
		kubeconfigPath := cfg.KubeconfigPath
		if kubeconfigPath == "" {
			// Default to ~/.kube/config
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("failed to get home directory: %w", err)
			}
			kubeconfigPath = filepath.Join(homeDir, ".kube", "config")
		}

		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to build config from kubeconfig: %w", err)
		}
	}

	// Set reasonable timeout defaults
	config.Timeout = 30 * time.Second

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	namespace := cfg.Namespace
	if namespace == "" {
		namespace = "default"
	}

	return &K8sClient{
		clientset:    clientset,
		namespace:    namespace,
		managedLabel: "tenatrix.io/managed",
	}, nil
}

// ==================== Deployment Operations ====================

// CreateDeployment creates a new Deployment in the cluster
func (c *K8sClient) CreateDeployment(ctx context.Context, deployment *appsv1.Deployment) (*appsv1.Deployment, error) {
	ns := c.getNamespace(deployment.Namespace)

	result, err := c.clientset.AppsV1().Deployments(ns).Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create deployment %s: %w", deployment.Name, err)
	}

	return result, nil
}

// UpdateDeployment updates an existing Deployment
func (c *K8sClient) UpdateDeployment(ctx context.Context, deployment *appsv1.Deployment) (*appsv1.Deployment, error) {
	ns := c.getNamespace(deployment.Namespace)

	result, err := c.clientset.AppsV1().Deployments(ns).Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update deployment %s: %w", deployment.Name, err)
	}

	return result, nil
}

// DeleteDeployment deletes a Deployment from the cluster
func (c *K8sClient) DeleteDeployment(ctx context.Context, name, namespace string) error {
	ns := c.getNamespace(namespace)

	err := c.clientset.AppsV1().Deployments(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete deployment %s: %w", name, err)
	}

	return nil
}

// GetDeployment retrieves a Deployment by name
func (c *K8sClient) GetDeployment(ctx context.Context, name, namespace string) (*appsv1.Deployment, error) {
	ns := c.getNamespace(namespace)

	deployment, err := c.clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, nil // Return nil instead of error for not found
		}
		return nil, fmt.Errorf("failed to get deployment %s: %w", name, err)
	}

	return deployment, nil
}

// ListManagedDeployments lists all Tenatrix-managed Deployments
func (c *K8sClient) ListManagedDeployments(ctx context.Context, namespace string) ([]appsv1.Deployment, error) {
	ns := c.getNamespace(namespace)

	// Filter by Tenatrix managed label
	labelSelector := fmt.Sprintf("%s=true", c.managedLabel)

	list, err := c.clientset.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list managed deployments: %w", err)
	}

	return list.Items, nil
}

// ListManagedDeploymentsForApp lists Deployments for a specific application
func (c *K8sClient) ListManagedDeploymentsForApp(ctx context.Context, namespace, appID string) ([]appsv1.Deployment, error) {
	ns := c.getNamespace(namespace)

	// Filter by managed label AND app label
	labelSelector := fmt.Sprintf("%s=true,tenatrix.io/app=%s", c.managedLabel, appID)

	list, err := c.clientset.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list deployments for app %s: %w", appID, err)
	}

	return list.Items, nil
}

// ==================== Service Operations ====================

// CreateService creates a new Service in the cluster
func (c *K8sClient) CreateService(ctx context.Context, service *corev1.Service) (*corev1.Service, error) {
	ns := c.getNamespace(service.Namespace)

	result, err := c.clientset.CoreV1().Services(ns).Create(ctx, service, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create service %s: %w", service.Name, err)
	}

	return result, nil
}

// UpdateService updates an existing Service
func (c *K8sClient) UpdateService(ctx context.Context, service *corev1.Service) (*corev1.Service, error) {
	ns := c.getNamespace(service.Namespace)

	// For Service updates, we need to preserve ClusterIP
	existing, err := c.clientset.CoreV1().Services(ns).Get(ctx, service.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get existing service %s: %w", service.Name, err)
	}

	// Preserve ClusterIP (Kubernetes doesn't allow changing it)
	service.Spec.ClusterIP = existing.Spec.ClusterIP
	service.ResourceVersion = existing.ResourceVersion

	result, err := c.clientset.CoreV1().Services(ns).Update(ctx, service, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update service %s: %w", service.Name, err)
	}

	return result, nil
}

// DeleteService deletes a Service from the cluster
func (c *K8sClient) DeleteService(ctx context.Context, name, namespace string) error {
	ns := c.getNamespace(namespace)

	err := c.clientset.CoreV1().Services(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete service %s: %w", name, err)
	}

	return nil
}

// GetService retrieves a Service by name
func (c *K8sClient) GetService(ctx context.Context, name, namespace string) (*corev1.Service, error) {
	ns := c.getNamespace(namespace)

	service, err := c.clientset.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, nil // Return nil instead of error for not found
		}
		return nil, fmt.Errorf("failed to get service %s: %w", name, err)
	}

	return service, nil
}

// ListManagedServices lists all Tenatrix-managed Services
func (c *K8sClient) ListManagedServices(ctx context.Context, namespace string) ([]corev1.Service, error) {
	ns := c.getNamespace(namespace)

	labelSelector := fmt.Sprintf("%s=true", c.managedLabel)

	list, err := c.clientset.CoreV1().Services(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list managed services: %w", err)
	}

	return list.Items, nil
}

// ListManagedServicesForApp lists Services for a specific application
func (c *K8sClient) ListManagedServicesForApp(ctx context.Context, namespace, appID string) ([]corev1.Service, error) {
	ns := c.getNamespace(namespace)

	labelSelector := fmt.Sprintf("%s=true,tenatrix.io/app=%s", c.managedLabel, appID)

	list, err := c.clientset.CoreV1().Services(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list services for app %s: %w", appID, err)
	}

	return list.Items, nil
}

// ==================== Pod Operations (for status) ====================

// ListPodsForDeployment lists all Pods belonging to a Deployment
func (c *K8sClient) ListPodsForDeployment(ctx context.Context, deployment *appsv1.Deployment) ([]corev1.Pod, error) {
	ns := c.getNamespace(deployment.Namespace)

	// Use the deployment's selector to find pods
	selector := deployment.Spec.Selector
	if selector == nil {
		return nil, fmt.Errorf("deployment %s has no selector", deployment.Name)
	}

	// Build label selector string
	var labelParts []string
	for k, v := range selector.MatchLabels {
		labelParts = append(labelParts, fmt.Sprintf("%s=%s", k, v))
	}
	labelSelector := ""
	if len(labelParts) > 0 {
		labelSelector = labelParts[0]
		for i := 1; i < len(labelParts); i++ {
			labelSelector += "," + labelParts[i]
		}
	}

	list, err := c.clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods for deployment %s: %w", deployment.Name, err)
	}

	return list.Items, nil
}

// ==================== Utility Methods ====================

// ApplyDeployment creates or updates a Deployment (upsert)
func (c *K8sClient) ApplyDeployment(ctx context.Context, deployment *appsv1.Deployment) (*appsv1.Deployment, error) {
	existing, err := c.GetDeployment(ctx, deployment.Name, deployment.Namespace)
	if err != nil {
		return nil, err
	}

	if existing == nil {
		return c.CreateDeployment(ctx, deployment)
	}

	// Preserve resource version for update
	deployment.ResourceVersion = existing.ResourceVersion
	return c.UpdateDeployment(ctx, deployment)
}

// ApplyService creates or updates a Service (upsert)
func (c *K8sClient) ApplyService(ctx context.Context, service *corev1.Service) (*corev1.Service, error) {
	existing, err := c.GetService(ctx, service.Name, service.Namespace)
	if err != nil {
		return nil, err
	}

	if existing == nil {
		return c.CreateService(ctx, service)
	}

	return c.UpdateService(ctx, service)
}

// CheckHealth performs a basic health check against the Kubernetes API
func (c *K8sClient) CheckHealth(ctx context.Context) error {
	_, err := c.clientset.Discovery().ServerVersion()
	if err != nil {
		return fmt.Errorf("kubernetes API health check failed: %w", err)
	}
	return nil
}

// GetNamespace returns the configured namespace
func (c *K8sClient) GetNamespace() string {
	return c.namespace
}

// getNamespace returns the provided namespace or falls back to default
func (c *K8sClient) getNamespace(ns string) string {
	if ns != "" {
		return ns
	}
	return c.namespace
}

// IsNotFound checks if an error is a Kubernetes NotFound error
func IsNotFound(err error) bool {
	return k8serrors.IsNotFound(err)
}

// IsAlreadyExists checks if an error is a Kubernetes AlreadyExists error
func IsAlreadyExists(err error) bool {
	return k8serrors.IsAlreadyExists(err)
}
