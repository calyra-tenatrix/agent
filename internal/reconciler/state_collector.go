// Package reconciler handles infrastructure reconciliation with Kubernetes
package reconciler

import (
	"context"
	"fmt"
	"time"

	agentv1 "github.com/calyra-tenatrix/agent/internal/proto/agent/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// StateCollector collects the actual state of resources from Kubernetes
// and converts them to proto format for reporting to the backend
type StateCollector struct {
	k8sClient *K8sClient
	targetID  string // Agent's target ID (cluster identifier)
}

// NewStateCollector creates a new StateCollector
func NewStateCollector(k8sClient *K8sClient, targetID string) *StateCollector {
	return &StateCollector{
		k8sClient: k8sClient,
		targetID:  targetID,
	}
}

// CollectActualState collects the current state of all Tenatrix-managed resources
// and returns an ActualInfrastructure proto message
func (c *StateCollector) CollectActualState(ctx context.Context) (*agentv1.ActualInfrastructure, error) {
	now := time.Now()

	// Collect services (deployments)
	services, err := c.collectServices(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to collect services: %w", err)
	}

	return &agentv1.ActualInfrastructure{
		TargetId:    c.targetID,
		CollectedAt: timestamppb.New(now),
		Services:    services,
		// Databases, Queues, Storages are not used (external services)
	}, nil
}

// CollectActualStateForApp collects state for a specific application only
func (c *StateCollector) CollectActualStateForApp(ctx context.Context, appID string) (*agentv1.ActualInfrastructure, error) {
	now := time.Now()

	services, err := c.collectServicesForApp(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("failed to collect services for app %s: %w", appID, err)
	}

	return &agentv1.ActualInfrastructure{
		TargetId:    c.targetID,
		CollectedAt: timestamppb.New(now),
		Services:    services,
	}, nil
}

// collectServices collects all Tenatrix-managed deployments and converts to ServiceStatus
func (c *StateCollector) collectServices(ctx context.Context) ([]*agentv1.ServiceStatus, error) {
	namespace := c.k8sClient.GetNamespace()

	// Get all managed deployments
	deployments, err := c.k8sClient.ListManagedDeployments(ctx, namespace)
	if err != nil {
		return nil, err
	}

	services := make([]*agentv1.ServiceStatus, 0, len(deployments))

	for _, deployment := range deployments {
		status, err := c.deploymentToServiceStatus(ctx, &deployment)
		if err != nil {
			// Log error but continue with other deployments
			continue
		}
		services = append(services, status)
	}

	return services, nil
}

// collectServicesForApp collects deployments for a specific application
func (c *StateCollector) collectServicesForApp(ctx context.Context, appID string) ([]*agentv1.ServiceStatus, error) {
	namespace := c.k8sClient.GetNamespace()

	deployments, err := c.k8sClient.ListManagedDeploymentsForApp(ctx, namespace, appID)
	if err != nil {
		return nil, err
	}

	services := make([]*agentv1.ServiceStatus, 0, len(deployments))

	for _, deployment := range deployments {
		status, err := c.deploymentToServiceStatus(ctx, &deployment)
		if err != nil {
			continue
		}
		services = append(services, status)
	}

	return services, nil
}

// deploymentToServiceStatus converts a Kubernetes Deployment to ServiceStatus proto
func (c *StateCollector) deploymentToServiceStatus(ctx context.Context, deployment *appsv1.Deployment) (*agentv1.ServiceStatus, error) {
	now := time.Now()

	// Get Tenatrix ID from annotation
	id := deployment.Annotations["tenatrix.io/service-id"]
	if id == "" {
		id = deployment.Labels["tenatrix.io/id"]
	}

	// Get container info (first container)
	var image string
	var ports []*agentv1.PortMapping

	if len(deployment.Spec.Template.Spec.Containers) > 0 {
		container := deployment.Spec.Template.Spec.Containers[0]
		image = container.Image
		ports = c.convertContainerPorts(container.Ports)
	}

	// Calculate status
	status := c.calculateDeploymentStatus(deployment)
	healthStatus := c.calculateHealthStatus(deployment)

	// Get replica counts
	var desiredReplicas int32 = 1
	if deployment.Spec.Replicas != nil {
		desiredReplicas = *deployment.Spec.Replicas
	}
	readyReplicas := deployment.Status.ReadyReplicas

	// Get started time
	var startedAt *timestamppb.Timestamp
	if deployment.Status.Conditions != nil {
		for _, cond := range deployment.Status.Conditions {
			if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
				startedAt = timestamppb.New(cond.LastTransitionTime.Time)
				break
			}
		}
	}

	// Convert labels
	labels := make(map[string]string)
	for k, v := range deployment.Labels {
		labels[k] = v
	}

	return &agentv1.ServiceStatus{
		Id:              id,
		ContainerId:     string(deployment.UID), // Use deployment UID as container ID
		Name:            deployment.Name,
		Image:           image,
		Status:          status,
		ReadyReplicas:   readyReplicas,
		DesiredReplicas: desiredReplicas,
		Ports:           ports,
		StartedAt:       startedAt,
		LastUpdated:     timestamppb.New(now),
		HealthStatus:    healthStatus,
		Labels:          labels,
	}, nil
}

// calculateDeploymentStatus determines the deployment status string
func (c *StateCollector) calculateDeploymentStatus(deployment *appsv1.Deployment) string {
	// Check conditions
	for _, cond := range deployment.Status.Conditions {
		if cond.Type == appsv1.DeploymentProgressing {
			if cond.Status == corev1.ConditionFalse && cond.Reason == "ProgressDeadlineExceeded" {
				return "error"
			}
		}
		if cond.Type == appsv1.DeploymentReplicaFailure && cond.Status == corev1.ConditionTrue {
			return "error"
		}
	}

	// Check replica counts
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}

	if desired == 0 {
		return "stopped"
	}

	if deployment.Status.ReadyReplicas == 0 {
		return "starting"
	}

	if deployment.Status.ReadyReplicas < desired {
		return "degraded"
	}

	return "running"
}

// calculateHealthStatus determines the health status
func (c *StateCollector) calculateHealthStatus(deployment *appsv1.Deployment) string {
	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}

	if desired == 0 {
		return "unknown"
	}

	// Check if available condition is true
	for _, cond := range deployment.Status.Conditions {
		if cond.Type == appsv1.DeploymentAvailable {
			if cond.Status == corev1.ConditionTrue {
				if deployment.Status.ReadyReplicas >= desired {
					return "healthy"
				}
				return "degraded"
			}
			return "unhealthy"
		}
	}

	return "unknown"
}

// convertContainerPorts converts Kubernetes container ports to proto PortMapping
func (c *StateCollector) convertContainerPorts(ports []corev1.ContainerPort) []*agentv1.PortMapping {
	result := make([]*agentv1.PortMapping, 0, len(ports))

	for _, p := range ports {
		protocol := "TCP"
		if p.Protocol == corev1.ProtocolUDP {
			protocol = "UDP"
		}

		result = append(result, &agentv1.PortMapping{
			ContainerPort: p.ContainerPort,
			// HostPort might be different if using NodePort service
			Protocol: protocol,
		})
	}

	return result
}

// ServiceInfo holds summary info about a deployed service
type ServiceInfo struct {
	ID              string
	Name            string
	Image           string
	Status          string
	ReadyReplicas   int32
	DesiredReplicas int32
	HealthStatus    string
}

// GetServiceInfo returns a quick summary of a specific service
func (c *StateCollector) GetServiceInfo(ctx context.Context, serviceName string) (*ServiceInfo, error) {
	deployment, err := c.k8sClient.GetDeployment(ctx, serviceName, "")
	if err != nil {
		return nil, err
	}
	if deployment == nil {
		return nil, nil
	}

	status, err := c.deploymentToServiceStatus(ctx, deployment)
	if err != nil {
		return nil, err
	}

	return &ServiceInfo{
		ID:              status.Id,
		Name:            status.Name,
		Image:           status.Image,
		Status:          status.Status,
		ReadyReplicas:   status.ReadyReplicas,
		DesiredReplicas: status.DesiredReplicas,
		HealthStatus:    status.HealthStatus,
	}, nil
}

// WaitForDeploymentReady waits until a deployment reaches the desired ready state
func (c *StateCollector) WaitForDeploymentReady(ctx context.Context, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		deployment, err := c.k8sClient.GetDeployment(ctx, name, "")
		if err != nil {
			return err
		}
		if deployment == nil {
			return fmt.Errorf("deployment %s not found", name)
		}

		desired := int32(1)
		if deployment.Spec.Replicas != nil {
			desired = *deployment.Spec.Replicas
		}

		// Check if ready
		if deployment.Status.ReadyReplicas >= desired &&
			deployment.Status.UpdatedReplicas >= desired {
			return nil
		}

		// Check for failure
		for _, cond := range deployment.Status.Conditions {
			if cond.Type == appsv1.DeploymentReplicaFailure && cond.Status == corev1.ConditionTrue {
				return fmt.Errorf("deployment %s failed: %s", name, cond.Message)
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			// Poll every 2 seconds
		}
	}

	return fmt.Errorf("timeout waiting for deployment %s to be ready", name)
}
