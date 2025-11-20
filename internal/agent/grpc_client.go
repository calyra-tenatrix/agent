package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	agentv1 "github.com/calyra-tenatrix/agent/internal/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GRPCClient manages the gRPC connection to the backend
type GRPCClient struct {
	conn         *grpc.ClientConn
	client       agentv1.AgentServiceClient
	stream       agentv1.AgentService_AgentChannelClient
	targetToken  string
	backendURL   string
	agentVersion string
	connected    bool
}

// NewGRPCClient creates a new gRPC client
func NewGRPCClient(backendURL, targetToken, agentVersion string) (*GRPCClient, error) {
	return &GRPCClient{
		backendURL:   backendURL,
		targetToken:  targetToken,
		agentVersion: agentVersion,
		connected:    false,
	}, nil
}

// Connect establishes a connection to the backend gRPC server
func (c *GRPCClient) Connect(ctx context.Context) error {
	log.Printf("[gRPC] Connecting to backend: %s", c.backendURL)

	// TODO: In production, use TLS credentials
	// For now, using insecure for local development
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	}

	// Set connection timeout
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(connectCtx, c.backendURL, opts...)
	if err != nil {
		return fmt.Errorf("failed to connect to backend: %w", err)
	}

	c.conn = conn
	c.client = agentv1.NewAgentServiceClient(conn)
	c.connected = true

	log.Printf("[gRPC] Connected to backend successfully")
	return nil
}

// OpenChannel opens the bidirectional streaming channel
func (c *GRPCClient) OpenChannel(ctx context.Context) (agentv1.AgentService_AgentChannelClient, error) {
	if !c.connected {
		return nil, fmt.Errorf("not connected to backend")
	}

	// Add authorization metadata
	md := metadata.New(map[string]string{
		"authorization": fmt.Sprintf("Bearer %s", c.targetToken),
	})
	ctx = metadata.NewOutgoingContext(ctx, md)

	stream, err := c.client.AgentChannel(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open channel: %w", err)
	}

	c.stream = stream
	log.Printf("[gRPC] Channel opened successfully")

	return stream, nil
}

// SendInitialStatus sends the initial agent status message
func (c *GRPCClient) SendInitialStatus(ctx context.Context, sysInfo *SystemInfo) error {
	if c.stream == nil {
		return fmt.Errorf("stream not initialized")
	}

	log.Printf("[gRPC] Sending initial agent status...")

	msg := &agentv1.AgentMessage{
		AgentVersion: c.agentVersion,
		Platform:     agentv1.Platform_PLATFORM_LINUX,
		SentAt:       timestamppb.Now(),
		Payload: &agentv1.AgentMessage_Status{
			Status: &agentv1.AgentStatus{
				Platform:     agentv1.Platform_PLATFORM_LINUX,
				HealthStatus: agentv1.HealthStatus_HEALTH_STATUS_HEALTHY,
				ActiveTasks:  0,
				Metrics: &agentv1.AgentMetrics{
					MemoryUsageBytes: sysInfo.MemoryUsed,
					CpuUsagePercent:  sysInfo.CPUPercent,
					GoroutineCount:   int32(sysInfo.GoroutineCount),
					UptimeSeconds:    int64(sysInfo.Uptime.Seconds()),
					TasksCompleted:   0,
					TasksFailed:      0,
				},
				CloudInfo: &agentv1.CloudInfo{
					Provider:        agentv1.CloudProvider_CLOUD_PROVIDER_DIGITALOCEAN,
					Region:          sysInfo.CloudRegion,
					DetectionMethod: agentv1.CloudDetectionMethod_CLOUD_DETECTION_METHOD_METADATA_SERVICE,
					Metadata: map[string]string{
						"hostname": sysInfo.Hostname,
						"ip":       sysInfo.IPAddress,
					},
				},
			},
		},
	}

	if err := c.stream.Send(msg); err != nil {
		return fmt.Errorf("failed to send initial status: %w", err)
	}

	log.Printf("[gRPC] Initial status sent successfully")
	return nil
}

// ReceiveMessages listens for messages from the backend
func (c *GRPCClient) ReceiveMessages(ctx context.Context, handler MessageHandler) error {
	if c.stream == nil {
		return fmt.Errorf("stream not initialized")
	}

	log.Printf("[gRPC] Starting message receiver...")

	for {
		select {
		case <-ctx.Done():
			log.Printf("[gRPC] Message receiver stopped")
			return ctx.Err()
		default:
			msg, err := c.stream.Recv()
			if err != nil {
				return fmt.Errorf("failed to receive message: %w", err)
			}

			// Handle the message
			if err := c.handleMessage(ctx, msg, handler); err != nil {
				log.Printf("[gRPC] Error handling message: %v", err)
			}
		}
	}
}

// handleMessage processes a received message
func (c *GRPCClient) handleMessage(ctx context.Context, msg *agentv1.BackendMessage, handler MessageHandler) error {
	switch payload := msg.Payload.(type) {
	case *agentv1.BackendMessage_Welcome:
		return handler.HandleWelcome(payload.Welcome)
	case *agentv1.BackendMessage_Task:
		return handler.HandleTask(payload.Task)
	case *agentv1.BackendMessage_ConfigUpdate:
		return handler.HandleConfigUpdate(payload.ConfigUpdate)
	case *agentv1.BackendMessage_Cancellation:
		return handler.HandleTaskCancellation(payload.Cancellation)
	case *agentv1.BackendMessage_HealthCheckRequest:
		return handler.HandleHealthCheck(payload.HealthCheckRequest)
	case *agentv1.BackendMessage_UpdateNotification:
		return handler.HandleUpdateNotification(payload.UpdateNotification)
	default:
		log.Printf("[gRPC] Received unknown message type")
		return nil
	}
}

// SendHeartbeat sends a heartbeat to the backend
func (c *GRPCClient) SendHeartbeat(ctx context.Context, sysInfo *SystemInfo) error {
	if !c.connected {
		return fmt.Errorf("not connected to backend")
	}

	// Add authorization metadata
	md := metadata.New(map[string]string{
		"authorization": fmt.Sprintf("Bearer %s", c.targetToken),
	})
	ctx = metadata.NewOutgoingContext(ctx, md)

	req := &agentv1.HeartbeatRequest{
		Platform:  agentv1.Platform_PLATFORM_LINUX,
		Timestamp: timestamppb.Now(),
		PlatformInfo: &agentv1.HeartbeatRequest_Linux{
			Linux: &agentv1.LinuxNodeInfo{
				Hostname:      sysInfo.Hostname,
				OsVersion:     sysInfo.OSVersion,
				KernelVersion: sysInfo.KernelVersion,
				Architecture:  sysInfo.Architecture,
				Health: &agentv1.NodeHealth{
					IsHealthy:    true,
					CpuUsage:     fmt.Sprintf("%.2f%%", sysInfo.CPUPercent),
					MemoryUsage:  fmt.Sprintf("%.2f%%", sysInfo.MemoryPercent),
					DiskUsage:    fmt.Sprintf("%.2f%%", sysInfo.DiskPercent),
					ProcessCount: int32(sysInfo.ProcessCount),
				},
			},
		},
		ActiveTasks: 0,
		Metrics: &agentv1.AgentMetrics{
			MemoryUsageBytes: sysInfo.MemoryUsed,
			CpuUsagePercent:  sysInfo.CPUPercent,
			GoroutineCount:   int32(sysInfo.GoroutineCount),
			UptimeSeconds:    int64(sysInfo.Uptime.Seconds()),
			TasksCompleted:   0,
			TasksFailed:      0,
		},
		CloudInfo: &agentv1.CloudInfo{
			Provider:        agentv1.CloudProvider_CLOUD_PROVIDER_DIGITALOCEAN,
			Region:          sysInfo.CloudRegion,
			DetectionMethod: agentv1.CloudDetectionMethod_CLOUD_DETECTION_METHOD_METADATA_SERVICE,
			Metadata: map[string]string{
				"hostname": sysInfo.Hostname,
				"ip":       sysInfo.IPAddress,
			},
		},
	}

	resp, err := c.client.Heartbeat(ctx, req)
	if err != nil {
		return fmt.Errorf("heartbeat failed: %w", err)
	}

	// Log warnings from backend
	if len(resp.Warnings) > 0 {
		log.Printf("[gRPC] Backend warnings: %s", strings.Join(resp.Warnings, ", "))
	}

	// Check for updates
	if resp.UpdateInfo != nil && resp.UpdateInfo.UpdateAvailable {
		log.Printf("[gRPC] Update available: %s -> %s", c.agentVersion, resp.UpdateInfo.NewVersion)
		if resp.UpdateInfo.Critical {
			log.Printf("[gRPC] ⚠️  CRITICAL SECURITY UPDATE AVAILABLE!")
		}
	}

	return nil
}

// Close closes the gRPC connection
func (c *GRPCClient) Close() error {
	if c.stream != nil {
		if err := c.stream.CloseSend(); err != nil {
			log.Printf("[gRPC] Error closing stream: %v", err)
		}
	}
	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			return fmt.Errorf("failed to close connection: %w", err)
		}
	}
	c.connected = false
	log.Printf("[gRPC] Connection closed")
	return nil
}

// MessageHandler defines the interface for handling backend messages
type MessageHandler interface {
	HandleWelcome(*agentv1.WelcomeMessage) error
	HandleTask(*agentv1.Task) error
	HandleConfigUpdate(*agentv1.ConfigUpdate) error
	HandleTaskCancellation(*agentv1.TaskCancellation) error
	HandleHealthCheck(*agentv1.HealthCheckRequest) error
	HandleUpdateNotification(*agentv1.UpdateNotification) error
}
