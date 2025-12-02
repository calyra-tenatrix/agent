package agent

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/calyra-tenatrix/agent/internal/cloud"
	"github.com/calyra-tenatrix/agent/internal/config"
	agentv1 "github.com/calyra-tenatrix/agent/internal/proto/agent/v1"
	"github.com/calyra-tenatrix/agent/internal/reconciler"
	"github.com/calyra-tenatrix/agent/internal/system"
)

// Agent represents the main agent structure
type Agent struct {
	config      *config.Config
	version     string
	client      *GRPCClient
	sysInfo     *SystemInfo
	provisioner *cloud.ClusterProvisioner // Manages K8s cluster lifecycle
	reconciler  *reconciler.Reconciler    // Set by provisioner when cluster is ready
	ctx         context.Context           // Store context for async operations

	// Provisioning state
	provisionMu    sync.Mutex
	isProvisioning bool
}

// SystemInfo is an alias for system.SystemInfo
type SystemInfo = system.SystemInfo

// Run starts the agent
func Run(ctx context.Context, cfg *config.Config, version string) error {
	agent := &Agent{
		config:  cfg,
		version: version,
	}

	return agent.run(ctx)
}

func (a *Agent) run(ctx context.Context) error {
	log.Printf("🚀 Tenatrix Agent v%s starting...", a.version)
	a.ctx = ctx

	// Create gRPC client
	client, err := NewGRPCClient(a.config.BackendURL, a.config.TargetToken, a.version)
	if err != nil {
		return fmt.Errorf("failed to create gRPC client: %w", err)
	}
	a.client = client
	defer a.client.Close()

	// Connect to backend
	if err := a.client.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect to backend: %w", err)
	}

	// Initialize cluster provisioner (lazy - cluster created on first deployment)
	if err := a.initProvisioner(); err != nil {
		log.Printf("⚠️  Cluster provisioner not available: %v", err)
		// Continue without provisioner - will fail on reconcile tasks
	} else {
		log.Printf("✅ Cluster provisioner initialized (cluster will be created on first deployment)")
	}

	// Collect initial system info
	sysInfo, err := system.Collect()
	if err != nil {
		log.Printf("⚠️  Failed to collect system info: %v", err)
		sysInfo = &SystemInfo{} // Use empty info
	}
	a.sysInfo = sysInfo

	log.Printf("📊 System Info: %s | %s | IP: %s | Region: %s",
		sysInfo.Hostname, sysInfo.OSVersion, sysInfo.IPAddress, sysInfo.CloudRegion)

	// Open bidirectional streaming channel
	_, err = a.client.OpenChannel(ctx)
	if err != nil {
		return fmt.Errorf("failed to open channel: %w", err)
	}

	// Send initial status
	if err := a.client.SendInitialStatus(ctx, sysInfo); err != nil {
		return fmt.Errorf("failed to send initial status: %w", err)
	}

	// Start heartbeat goroutine
	go a.heartbeatLoop(ctx)

	// Start message receiver
	log.Printf("✅ Agent is ready and listening for messages")
	if err := a.client.ReceiveMessages(ctx, a); err != nil {
		return fmt.Errorf("message receiver failed: %w", err)
	}

	return nil
}

// initProvisioner initializes the cluster provisioner
// The actual K8s cluster is created lazily on first reconcile task
func (a *Agent) initProvisioner() error {
	// Extract target ID from token (format: uuid:signature)
	targetID := a.config.TargetToken
	if idx := len(targetID); idx > 36 {
		targetID = targetID[:36] // Get just the UUID part
	}

	provisioner, err := cloud.NewClusterProvisioner(a.ctx, cloud.ProvisionerConfig{
		DOToken:   a.config.DOToken,
		TargetID:  targetID,
		Namespace: a.config.KubernetesNamespace,
		StatusCallback: func(status cloud.ProvisioningStatus) {
			// Log provisioning status
			log.Printf("[Provisioner] %s: %s (progress: %d%%)",
				status.State, status.Message, status.Progress)
			// TODO: Send status to backend via gRPC
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create provisioner: %w", err)
	}

	a.provisioner = provisioner

	// Check if cluster already exists and is ready
	if provisioner.IsClusterReady() {
		a.reconciler = provisioner.GetReconciler()
		log.Printf("✅ Existing Kubernetes cluster connected")
	}

	return nil
}

// ensureCluster ensures the K8s cluster is ready before reconciliation
// This is called on-demand when a reconcile task is received
func (a *Agent) ensureCluster() (*reconciler.Reconciler, error) {
	a.provisionMu.Lock()
	defer a.provisionMu.Unlock()

	// If already provisioning, wait
	if a.isProvisioning {
		return nil, fmt.Errorf("cluster provisioning already in progress")
	}

	// If reconciler is already available, return it
	if a.reconciler != nil {
		return a.reconciler, nil
	}

	// Check provisioner
	if a.provisioner == nil {
		return nil, fmt.Errorf("cluster provisioner not initialized")
	}

	a.isProvisioning = true
	defer func() { a.isProvisioning = false }()

	log.Printf("🚀 First deployment - provisioning Kubernetes cluster...")

	// This may take 5-10 minutes for new cluster
	rec, err := a.provisioner.EnsureCluster(a.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to provision cluster: %w", err)
	}

	a.reconciler = rec
	return rec, nil
}

// heartbeatLoop sends periodic heartbeats
func (a *Agent) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(a.config.HeartbeatInterval)
	defer ticker.Stop()

	log.Printf("💓 Heartbeat started (interval: %s)", a.config.HeartbeatInterval)

	for {
		select {
		case <-ctx.Done():
			log.Printf("💓 Heartbeat stopped")
			return
		case <-ticker.C:
			// Collect fresh system info
			sysInfo, err := system.Collect()
			if err != nil {
				log.Printf("⚠️  Failed to collect system info for heartbeat: %v", err)
				continue
			}
			a.sysInfo = sysInfo

			// Send heartbeat
			if err := a.client.SendHeartbeat(ctx, sysInfo); err != nil {
				log.Printf("⚠️  Heartbeat failed: %v", err)
				continue
			}

			log.Printf("💓 Heartbeat sent (CPU: %.1f%%, Mem: %.1f%%, Disk: %.1f%%)",
				sysInfo.CPUPercent, sysInfo.MemoryPercent, sysInfo.DiskPercent)
		}
	}
}

// MessageHandler implementation

func (a *Agent) HandleWelcome(msg *agentv1.WelcomeMessage) error {
	log.Printf("👋 Welcome message from backend: %s (server version: %s)",
		msg.Message, msg.ServerVersion)
	return nil
}

func (a *Agent) HandleTask(task *agentv1.Task) error {
	log.Printf("📋 Received task: %s (type: %s)", task.Id, task.Type)
	// TODO: Implement task execution in future versions
	log.Printf("⚠️  Task execution not implemented yet (V1 only has heartbeat)")
	return nil
}

func (a *Agent) HandleConfigUpdate(update *agentv1.ConfigUpdate) error {
	log.Printf("⚙️  Received config update: version %s", update.Version)
	// TODO: Implement config update in future versions
	log.Printf("⚠️  Config update not implemented yet")
	return nil
}

func (a *Agent) HandleTaskCancellation(cancellation *agentv1.TaskCancellation) error {
	log.Printf("❌ Task cancellation: %s (reason: %s)", cancellation.TaskId, cancellation.Reason)
	// TODO: Implement task cancellation in future versions
	return nil
}

func (a *Agent) HandleHealthCheck(req *agentv1.HealthCheckRequest) error {
	log.Printf("🏥 Received health check request: %s", req.RequestId)
	// TODO: Respond with health status via stream
	return nil
}

func (a *Agent) HandleUpdateNotification(notification *agentv1.UpdateNotification) error {
	log.Printf("🔄 Update available: %s -> %s", notification.CurrentVersion, notification.NewVersion)
	log.Printf("   Download URL: %s", notification.DownloadUrl)
	log.Printf("   Checksum: %s", notification.Checksum)

	if notification.ForceUpdate {
		log.Printf("⚠️  FORCED UPDATE - Agent will update automatically")
		// TODO: Trigger auto-update mechanism
	} else {
		log.Printf("ℹ️  Optional update available (will check during next update cycle)")
	}

	return nil
}

func (a *Agent) HandleReconcileTask(task *agentv1.ReconcileTask) error {
	log.Printf("🔄 Received reconcile task: %s (app: %s, env: %s, dry_run: %v)",
		task.TaskId,
		task.Desired.ApplicationId,
		task.Desired.Environment,
		task.DryRun)

	// Execute reconciliation asynchronously to not block message processing
	go func() {
		// Ensure cluster is ready (creates on first deployment)
		rec, err := a.ensureCluster()
		if err != nil {
			log.Printf("❌ Failed to ensure cluster: %v", err)
			result := &agentv1.ReconcileResult{
				TaskId:  task.TaskId,
				Success: false,
				Error: &agentv1.ErrorInfo{
					Code:    "CLUSTER_PROVISIONING_FAILED",
					Message: fmt.Sprintf("Failed to provision Kubernetes cluster: %v", err),
				},
			}
			if sendErr := a.client.SendReconcileResult(result); sendErr != nil {
				log.Printf("❌ Failed to send reconcile result: %v", sendErr)
			}
			return
		}

		log.Printf("⚙️  Starting reconciliation for task %s...", task.TaskId)

		// Execute reconciliation
		result := rec.ReconcileTask(a.ctx, task)

		// Log result
		if result.Success {
			log.Printf("✅ Reconciliation task %s completed successfully", task.TaskId)
			if result.DryRun {
				log.Printf("   📋 Planned changes: %d", len(result.PlannedChanges))
			} else {
				log.Printf("   📋 Applied changes: %d", len(result.AppliedChanges))
			}
		} else {
			log.Printf("❌ Reconciliation task %s failed: %s", task.TaskId, result.Error.Message)
		}

		// Send result back to backend
		if err := a.client.SendReconcileResult(result); err != nil {
			log.Printf("❌ Failed to send reconcile result: %v", err)
		}
	}()

	return nil
}
