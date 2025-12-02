// Package digitalocean provides DigitalOcean cloud provider implementation
package digitalocean

import (
	"context"
	"fmt"
	"log"

	"github.com/digitalocean/godo"
)

// VPCInfo holds information about a VPC
type VPCInfo struct {
	UUID      string
	Name      string
	IPRange   string
	Region    string
	IsDefault bool
}

// GetOrCreateVPC ensures a VPC exists for the agent
// If the agent's droplet is already in a VPC, returns that VPC
// Otherwise creates a new VPC for Tenatrix resources
func (c *Client) GetOrCreateVPC(ctx context.Context, targetID string) (*VPCInfo, error) {
	// First, try to get the VPC from the current droplet
	if c.dropletID != 0 {
		vpcUUID, err := c.GetDropletVPCUUID(ctx)
		if err == nil && vpcUUID != "" {
			vpc, _, err := c.client.VPCs.Get(ctx, vpcUUID)
			if err == nil {
				log.Printf("[DO] Using existing VPC from droplet: %s (%s)", vpc.Name, vpc.ID)
				return &VPCInfo{
					UUID:      vpc.ID,
					Name:      vpc.Name,
					IPRange:   vpc.IPRange,
					Region:    vpc.RegionSlug,
					IsDefault: vpc.Default,
				}, nil
			}
		}
	}

	// If no VPC from droplet, look for existing Tenatrix VPC
	vpcName := fmt.Sprintf("tenatrix-%s", targetID[:8])
	existingVPC, err := c.findVPCByName(ctx, vpcName)
	if err == nil && existingVPC != nil {
		log.Printf("[DO] Found existing Tenatrix VPC: %s", existingVPC.UUID)
		return existingVPC, nil
	}

	// Create new VPC
	log.Printf("[DO] Creating new VPC: %s in region %s", vpcName, c.region)
	return c.createVPC(ctx, vpcName)
}

// findVPCByName searches for a VPC by name in the current region
func (c *Client) findVPCByName(ctx context.Context, name string) (*VPCInfo, error) {
	opt := &godo.ListOptions{PerPage: 100}

	vpcs, _, err := c.client.VPCs.List(ctx, opt)
	if err != nil {
		return nil, fmt.Errorf("failed to list VPCs: %w", err)
	}

	for _, vpc := range vpcs {
		if vpc.Name == name && vpc.RegionSlug == c.region {
			return &VPCInfo{
				UUID:      vpc.ID,
				Name:      vpc.Name,
				IPRange:   vpc.IPRange,
				Region:    vpc.RegionSlug,
				IsDefault: vpc.Default,
			}, nil
		}
	}

	return nil, fmt.Errorf("VPC not found: %s", name)
}

// createVPC creates a new VPC for Tenatrix
func (c *Client) createVPC(ctx context.Context, name string) (*VPCInfo, error) {
	createReq := &godo.VPCCreateRequest{
		Name:        name,
		RegionSlug:  c.region,
		Description: "Tenatrix managed VPC for Kubernetes cluster",
		// Let DO auto-assign IP range
	}

	vpc, _, err := c.client.VPCs.Create(ctx, createReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create VPC: %w", err)
	}

	log.Printf("[DO] Created VPC: %s (%s) with IP range %s", vpc.Name, vpc.ID, vpc.IPRange)

	return &VPCInfo{
		UUID:      vpc.ID,
		Name:      vpc.Name,
		IPRange:   vpc.IPRange,
		Region:    vpc.RegionSlug,
		IsDefault: vpc.Default,
	}, nil
}

// GetVPCInfo retrieves information about a specific VPC
func (c *Client) GetVPCInfo(ctx context.Context, vpcUUID string) (*VPCInfo, error) {
	vpc, _, err := c.client.VPCs.Get(ctx, vpcUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to get VPC: %w", err)
	}

	return &VPCInfo{
		UUID:      vpc.ID,
		Name:      vpc.Name,
		IPRange:   vpc.IPRange,
		Region:    vpc.RegionSlug,
		IsDefault: vpc.Default,
	}, nil
}
