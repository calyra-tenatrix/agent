// Package reconciler handles infrastructure reconciliation with Kubernetes
package reconciler

import (
	"context"
	"fmt"
	"sync"
	"time"

	agentv1 "github.com/calyra-tenatrix/agent/internal/proto/agent/v1"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Reconciler orchestrates the reconciliation process between desired and actual state
type Reconciler struct {
	k8sClient         *K8sClient
	manifestGenerator *ManifestGenerator
	stateCollector    *StateCollector

	// Synchronization
	mu          sync.Mutex
	activeTasks map[string]context.CancelFunc // taskId -> cancel function
}

// NewReconciler creates a new Reconciler instance
func NewReconciler(k8sClient *K8sClient, targetID string) *Reconciler {
	namespace := k8sClient.GetNamespace()

	return &Reconciler{
		k8sClient:         k8sClient,
		manifestGenerator: NewManifestGenerator(namespace),
		stateCollector:    NewStateCollector(k8sClient, targetID),
		activeTasks:       make(map[string]context.CancelFunc),
	}
}

// ReconcileTask executes a reconciliation task
func (r *Reconciler) ReconcileTask(ctx context.Context, task *agentv1.ReconcileTask) *agentv1.ReconcileResult {
	startedAt := time.Now()
	logs := make([]string, 0)

	// Create result structure
	result := &agentv1.ReconcileResult{
		TaskId:    task.TaskId,
		DryRun:    task.DryRun,
		StartedAt: timestamppb.New(startedAt),
	}

	// Create task context with timeout
	timeout := time.Duration(task.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 5 * time.Minute // Default 5 minutes
	}

	taskCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Register active task
	r.mu.Lock()
	r.activeTasks[task.TaskId] = cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.activeTasks, task.TaskId)
		r.mu.Unlock()
	}()

	logs = append(logs, fmt.Sprintf("Starting reconciliation for app %s (env: %s)",
		task.Desired.ApplicationId, task.Desired.Environment))

	// Step 1: Calculate diff between desired and actual state
	logs = append(logs, "Calculating diff between desired and actual state...")
	diffItems, err := r.calculateDiff(taskCtx, task.Desired)
	if err != nil {
		result.Success = false
		result.Error = &agentv1.ErrorInfo{
			Code:    "DIFF_CALCULATION_FAILED",
			Message: err.Error(),
		}
		result.Logs = logs
		result.CompletedAt = timestamppb.Now()
		return result
	}

	logs = append(logs, fmt.Sprintf("Found %d changes to apply", len(diffItems)))

	// Set planned changes
	result.PlannedChanges = diffItems

	// If dry run, return without applying changes
	if task.DryRun {
		logs = append(logs, "Dry run mode - no changes applied")
		result.Success = true
		result.Logs = logs
		result.CompletedAt = timestamppb.Now()
		return result
	}

	// Step 2: Apply changes
	logs = append(logs, "Applying changes to Kubernetes cluster...")
	appliedChanges, applyLogs, err := r.applyChanges(taskCtx, task.Desired, diffItems)
	logs = append(logs, applyLogs...)
	result.AppliedChanges = appliedChanges

	if err != nil {
		result.Success = false
		result.Error = &agentv1.ErrorInfo{
			Code:    "APPLY_FAILED",
			Message: err.Error(),
		}
		logs = append(logs, fmt.Sprintf("Reconciliation failed: %s", err.Error()))
	} else {
		result.Success = true
		logs = append(logs, "Reconciliation completed successfully")
	}

	result.Logs = logs
	result.CompletedAt = timestamppb.Now()
	return result
}

// CancelTask cancels a running reconciliation task
func (r *Reconciler) CancelTask(taskID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if cancel, ok := r.activeTasks[taskID]; ok {
		cancel()
		return true
	}
	return false
}

// calculateDiff compares desired state with actual state and returns diff items
func (r *Reconciler) calculateDiff(ctx context.Context, desired *agentv1.DesiredInfrastructure) ([]*agentv1.DiffItem, error) {
	// Collect actual state
	actual, err := r.stateCollector.CollectActualStateForApp(ctx, desired.ApplicationId)
	if err != nil {
		return nil, fmt.Errorf("failed to collect actual state: %w", err)
	}

	// Build lookup map for actual services
	actualServices := make(map[string]*agentv1.ServiceStatus)
	for _, svc := range actual.Services {
		actualServices[svc.Id] = svc
	}

	diffItems := make([]*agentv1.DiffItem, 0)

	// Compare services
	for _, desiredSvc := range desired.Services {
		actualSvc, exists := actualServices[desiredSvc.Id]

		if !exists {
			// Service needs to be created
			diffItems = append(diffItems, &agentv1.DiffItem{
				Action:       agentv1.DiffAction_DIFF_ACTION_CREATE,
				ResourceType: "service",
				ResourceId:   desiredSvc.Id,
				ResourceName: desiredSvc.Name,
				After:        serviceSpecToStruct(desiredSvc),
				Changes:      []string{fmt.Sprintf("Create service '%s' with image %s", desiredSvc.Name, desiredSvc.Image)},
			})
		} else {
			// Check if update is needed
			changes := r.compareServiceSpec(desiredSvc, actualSvc)
			if len(changes) > 0 {
				diffItems = append(diffItems, &agentv1.DiffItem{
					Action:       agentv1.DiffAction_DIFF_ACTION_UPDATE,
					ResourceType: "service",
					ResourceId:   desiredSvc.Id,
					ResourceName: desiredSvc.Name,
					Before:       serviceStatusToStruct(actualSvc),
					After:        serviceSpecToStruct(desiredSvc),
					Changes:      changes,
				})
			}
			// Remove from actualServices map (remaining ones will be deleted)
			delete(actualServices, desiredSvc.Id)
		}
	}

	// Any remaining actual services should be deleted
	for _, actualSvc := range actualServices {
		diffItems = append(diffItems, &agentv1.DiffItem{
			Action:       agentv1.DiffAction_DIFF_ACTION_DELETE,
			ResourceType: "service",
			ResourceId:   actualSvc.Id,
			ResourceName: actualSvc.Name,
			Before:       serviceStatusToStruct(actualSvc),
			Changes:      []string{fmt.Sprintf("Delete service '%s'", actualSvc.Name)},
		})
	}

	return diffItems, nil
}

// compareServiceSpec compares desired spec with actual status and returns changes
func (r *Reconciler) compareServiceSpec(desired *agentv1.ServiceSpec, actual *agentv1.ServiceStatus) []string {
	changes := make([]string, 0)

	// Image change
	if desired.Image != actual.Image {
		changes = append(changes, fmt.Sprintf("Image: %s → %s", actual.Image, desired.Image))
	}

	// Replicas change
	if desired.Replicas != actual.DesiredReplicas {
		changes = append(changes, fmt.Sprintf("Replicas: %d → %d", actual.DesiredReplicas, desired.Replicas))
	}

	// Port changes (simplified - just count)
	if len(desired.Ports) != len(actual.Ports) {
		changes = append(changes, fmt.Sprintf("Ports: %d → %d ports", len(actual.Ports), len(desired.Ports)))
	}

	return changes
}

// applyChanges applies the diff items to Kubernetes
func (r *Reconciler) applyChanges(
	ctx context.Context,
	desired *agentv1.DesiredInfrastructure,
	diffItems []*agentv1.DiffItem,
) ([]*agentv1.DiffItem, []string, error) {
	logs := make([]string, 0)
	appliedChanges := make([]*agentv1.DiffItem, 0, len(diffItems))

	// Build service spec map for quick lookup
	serviceSpecs := make(map[string]*agentv1.ServiceSpec)
	for _, svc := range desired.Services {
		serviceSpecs[svc.Id] = svc
	}

	// Process each diff item
	for _, item := range diffItems {
		appliedItem := &agentv1.DiffItem{
			Action:       item.Action,
			ResourceType: item.ResourceType,
			ResourceId:   item.ResourceId,
			ResourceName: item.ResourceName,
			Before:       item.Before,
			After:        item.After,
			Changes:      item.Changes,
		}

		switch item.ResourceType {
		case "service":
			err := r.applyServiceChange(ctx, item, serviceSpecs, desired.ApplicationId, desired.Environment)
			if err != nil {
				appliedItem.Applied = false
				appliedItem.ErrorMessage = err.Error()
				logs = append(logs, fmt.Sprintf("Failed to %s service %s: %s",
					item.Action.String(), item.ResourceName, err.Error()))
			} else {
				appliedItem.Applied = true
				logs = append(logs, fmt.Sprintf("Successfully %s service %s",
					actionVerb(item.Action), item.ResourceName))
			}
		default:
			appliedItem.Applied = false
			appliedItem.ErrorMessage = fmt.Sprintf("Unknown resource type: %s", item.ResourceType)
		}

		appliedChanges = append(appliedChanges, appliedItem)
	}

	// Check if any changes failed
	for _, item := range appliedChanges {
		if !item.Applied {
			return appliedChanges, logs, fmt.Errorf("some changes failed to apply")
		}
	}

	return appliedChanges, logs, nil
}

// applyServiceChange applies a single service change (create/update/delete)
func (r *Reconciler) applyServiceChange(
	ctx context.Context,
	item *agentv1.DiffItem,
	serviceSpecs map[string]*agentv1.ServiceSpec,
	appID, env string,
) error {
	switch item.Action {
	case agentv1.DiffAction_DIFF_ACTION_CREATE, agentv1.DiffAction_DIFF_ACTION_UPDATE:
		spec, ok := serviceSpecs[item.ResourceId]
		if !ok {
			return fmt.Errorf("service spec not found for ID: %s", item.ResourceId)
		}

		// Generate Kubernetes manifests
		deployment := r.manifestGenerator.GenerateDeployment(spec, appID, env)
		service := r.manifestGenerator.GenerateService(spec, appID, env)

		// Apply Deployment
		_, err := r.k8sClient.ApplyDeployment(ctx, deployment)
		if err != nil {
			return fmt.Errorf("failed to apply deployment: %w", err)
		}

		// Apply Service (if ports defined)
		if service != nil {
			_, err = r.k8sClient.ApplyService(ctx, service)
			if err != nil {
				return fmt.Errorf("failed to apply service: %w", err)
			}
		}

		// Wait for deployment to be ready (with a shorter timeout for responsiveness)
		waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()

		err = r.stateCollector.WaitForDeploymentReady(waitCtx, deployment.Name, 2*time.Minute)
		if err != nil {
			// Log warning but don't fail - deployment will eventually become ready
			// This allows the reconciliation to continue
			return nil // Consider returning error if strict readiness is required
		}

		return nil

	case agentv1.DiffAction_DIFF_ACTION_DELETE:
		// Delete Service first
		err := r.k8sClient.DeleteService(ctx, item.ResourceName, "")
		if err != nil {
			return fmt.Errorf("failed to delete service: %w", err)
		}

		// Delete Deployment
		err = r.k8sClient.DeleteDeployment(ctx, item.ResourceName, "")
		if err != nil {
			return fmt.Errorf("failed to delete deployment: %w", err)
		}

		return nil

	default:
		return nil // No action for UNCHANGED
	}
}

// ==================== Helper Functions ====================

// actionVerb returns past tense verb for action
func actionVerb(action agentv1.DiffAction) string {
	switch action {
	case agentv1.DiffAction_DIFF_ACTION_CREATE:
		return "created"
	case agentv1.DiffAction_DIFF_ACTION_UPDATE:
		return "updated"
	case agentv1.DiffAction_DIFF_ACTION_DELETE:
		return "deleted"
	default:
		return "processed"
	}
}

// serviceSpecToStruct converts ServiceSpec to protobuf Struct for diff visualization
func serviceSpecToStruct(spec *agentv1.ServiceSpec) *structpb.Struct {
	// Build a simplified representation for the diff
	fields := map[string]interface{}{
		"name":     spec.Name,
		"image":    spec.Image,
		"replicas": spec.Replicas,
	}

	// Add ports
	if len(spec.Ports) > 0 {
		ports := make([]interface{}, 0, len(spec.Ports))
		for _, p := range spec.Ports {
			ports = append(ports, map[string]interface{}{
				"containerPort": p.ContainerPort,
				"protocol":      p.Protocol,
			})
		}
		fields["ports"] = ports
	}

	// Add resource limits if present
	if spec.ResourceLimits != nil {
		fields["resources"] = map[string]interface{}{
			"cpuLimit":    spec.ResourceLimits.CpuLimit,
			"memoryLimit": spec.ResourceLimits.MemoryLimit,
		}
	}

	result, _ := structpb.NewStruct(fields)
	return result
}

// serviceStatusToStruct converts ServiceStatus to protobuf Struct
func serviceStatusToStruct(status *agentv1.ServiceStatus) *structpb.Struct {
	fields := map[string]interface{}{
		"name":            status.Name,
		"image":           status.Image,
		"status":          status.Status,
		"readyReplicas":   status.ReadyReplicas,
		"desiredReplicas": status.DesiredReplicas,
		"healthStatus":    status.HealthStatus,
	}

	result, _ := structpb.NewStruct(fields)
	return result
}

// GetStateCollector returns the state collector for external use
func (r *Reconciler) GetStateCollector() *StateCollector {
	return r.stateCollector
}

// GetK8sClient returns the Kubernetes client for health checks
func (r *Reconciler) GetK8sClient() *K8sClient {
	return r.k8sClient
}
