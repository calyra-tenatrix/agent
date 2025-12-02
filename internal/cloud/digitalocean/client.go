// Package digitalocean provides DigitalOcean cloud provider implementation
package digitalocean

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"
)

// Client wraps the DigitalOcean API client
type Client struct {
	client    *godo.Client
	token     string
	region    string // Agent's region (detected or configured)
	dropletID int    // Agent's droplet ID (for VPC detection)
}

// ClientConfig holds configuration for creating a DO client
type ClientConfig struct {
	Token     string
	Region    string // Optional: if empty, will be detected
	DropletID int    // Optional: if 0, will be detected from metadata
}

// NewClient creates a new DigitalOcean client
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("DigitalOcean token is required")
	}

	// Create OAuth2 token source
	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cfg.Token})
	oauthClient := oauth2.NewClient(context.Background(), tokenSource)

	// Create godo client
	client := godo.NewClient(oauthClient)

	c := &Client{
		client:    client,
		token:     cfg.Token,
		region:    cfg.Region,
		dropletID: cfg.DropletID,
	}

	return c, nil
}

// DetectEnvironment detects the current droplet's region and VPC from metadata
func (c *Client) DetectEnvironment(ctx context.Context) error {
	// Try to detect from DigitalOcean metadata service
	region, err := c.getMetadata("region")
	if err != nil {
		log.Printf("[DO] Could not detect region from metadata: %v", err)
	} else {
		c.region = region
		log.Printf("[DO] Detected region: %s", region)
	}

	// Try to detect droplet ID
	dropletIDStr, err := c.getMetadata("droplet-id")
	if err != nil {
		log.Printf("[DO] Could not detect droplet ID from metadata: %v", err)
	} else {
		var dropletID int
		fmt.Sscanf(dropletIDStr, "%d", &dropletID)
		c.dropletID = dropletID
		log.Printf("[DO] Detected droplet ID: %d", dropletID)
	}

	return nil
}

// getMetadata fetches metadata from DO metadata service
func (c *Client) getMetadata(key string) (string, error) {
	url := fmt.Sprintf("http://169.254.169.254/metadata/v1/%s", key)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata service returned %d", resp.StatusCode)
	}

	var buf [256]byte
	n, _ := resp.Body.Read(buf[:])
	return string(buf[:n]), nil
}

// GetRegion returns the detected or configured region
func (c *Client) GetRegion() string {
	return c.region
}

// GetDropletID returns the current droplet ID
func (c *Client) GetDropletID() int {
	return c.dropletID
}

// GetGodoClient returns the underlying godo client for advanced operations
func (c *Client) GetGodoClient() *godo.Client {
	return c.client
}

// GetDropletVPCUUID gets the VPC UUID of the current droplet
func (c *Client) GetDropletVPCUUID(ctx context.Context) (string, error) {
	if c.dropletID == 0 {
		return "", fmt.Errorf("droplet ID not set")
	}

	droplet, _, err := c.client.Droplets.Get(ctx, c.dropletID)
	if err != nil {
		return "", fmt.Errorf("failed to get droplet info: %w", err)
	}

	if droplet.VPCUUID == "" {
		return "", fmt.Errorf("droplet is not in a VPC")
	}

	return droplet.VPCUUID, nil
}

// GetDropletPrivateIP gets the private IP of the current droplet
func (c *Client) GetDropletPrivateIP(ctx context.Context) (string, error) {
	if c.dropletID == 0 {
		return "", fmt.Errorf("droplet ID not set")
	}

	droplet, _, err := c.client.Droplets.Get(ctx, c.dropletID)
	if err != nil {
		return "", fmt.Errorf("failed to get droplet info: %w", err)
	}

	// Find private IPv4
	for _, network := range droplet.Networks.V4 {
		if network.Type == "private" {
			return network.IPAddress, nil
		}
	}

	return "", fmt.Errorf("no private IP found")
}
