package agent

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/calyra-tenatrix/agent/internal/config"
	agentv1 "github.com/calyra-tenatrix/agent/internal/proto/agent/v1"
	"github.com/calyra-tenatrix/agent/internal/system"
)

// Agent represents the main agent structure
type Agent struct {
	config  *config.Config
	version string
	client  *GRPCClient
	sysInfo *SystemInfo
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
