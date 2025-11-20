package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// GitHubRelease represents a GitHub release
type GitHubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	PublishedAt time.Time     `json:"published_at"`
	Assets      []GitHubAsset `json:"assets"`
	Body        string        `json:"body"`
}

// GitHubAsset represents a release asset
type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// Release represents a simplified release info
type Release struct {
	Version     string
	DownloadURL string
	Checksum    string
	ReleaseDate time.Time
}

// GitHubClient handles GitHub API operations
type GitHubClient struct {
	repo   string // e.g., "calyra-tenatrix/agent"
	client *http.Client
}

// NewGitHubClient creates a new GitHub client
func NewGitHubClient(repo string) *GitHubClient {
	return &GitHubClient{
		repo: repo,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// GetLatestRelease fetches the latest release from GitHub
func (g *GitHubClient) GetLatestRelease() (*Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", g.repo)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var ghRelease GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&ghRelease); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Find binary and checksum assets
	binaryURL := g.findAsset(ghRelease.Assets, "tenatrix-agent-linux-amd64")
	checksumURL := g.findAsset(ghRelease.Assets, "checksums.txt")

	if binaryURL == "" {
		return nil, fmt.Errorf("binary asset not found in release")
	}
	if checksumURL == "" {
		return nil, fmt.Errorf("checksum file not found in release")
	}

	// Download checksum file to extract the checksum
	checksum, err := g.downloadChecksum(checksumURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get checksum: %w", err)
	}

	return &Release{
		Version:     ghRelease.TagName,
		DownloadURL: binaryURL,
		Checksum:    checksum,
		ReleaseDate: ghRelease.PublishedAt,
	}, nil
}

// findAsset finds an asset by name
func (g *GitHubClient) findAsset(assets []GitHubAsset, name string) string {
	for _, asset := range assets {
		if asset.Name == name {
			return asset.BrowserDownloadURL
		}
	}
	return ""
}

// downloadChecksum downloads and parses the checksum file
func (g *GitHubClient) downloadChecksum(url string) (string, error) {
	resp, err := g.client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// Parse checksum file (format: "<checksum>  <filename>")
	lines := string(body)
	// Find line with "tenatrix-agent-linux-amd64"
	for _, line := range splitLines(lines) {
		if len(line) > 0 && contains(line, "tenatrix-agent-linux-amd64") {
			// Extract checksum (first part before spaces)
			parts := splitWhitespace(line)
			if len(parts) >= 1 {
				return parts[0], nil
			}
		}
	}

	return "", fmt.Errorf("checksum not found in file")
}

// DownloadBinary downloads a binary from URL
func (g *GitHubClient) DownloadBinary(url string) ([]byte, error) {
	resp, err := g.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to download binary: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read binary: %w", err)
	}

	return data, nil
}

// Helper functions

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func splitWhitespace(s string) []string {
	var parts []string
	current := ""
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			if len(current) > 0 {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(s[i])
		}
	}
	if len(current) > 0 {
		parts = append(parts, current)
	}
	return parts
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && findSubstring(s, substr) >= 0
}

func findSubstring(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
