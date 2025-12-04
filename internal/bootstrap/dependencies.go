package bootstrap

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// ToolRequirement defines a required tool and its minimum version
type ToolRequirement struct {
	Name         string   // Tool name (e.g., "docker", "git", "nixpacks")
	MinVersion   string   // Minimum required version (semver, e.g., "v24.0.0")
	VersionCmd   []string // Command to get version (e.g., ["docker", "--version"])
	VersionRegex string   // Regex to extract version from output
	InstallCmds  []string // Commands to install/update the tool
	Description  string   // Human-readable description
}

// DefaultRequirements defines the tools required for build operations
var DefaultRequirements = []ToolRequirement{
	{
		Name:         "docker",
		MinVersion:   "v24.0.0",
		VersionCmd:   []string{"docker", "--version"},
		VersionRegex: `Docker version (\d+\.\d+\.\d+)`,
		InstallCmds: []string{
			"apt-get update",
			"apt-get install -y docker.io",
			"systemctl enable docker",
			"systemctl start docker",
		},
		Description: "Container runtime for building and pushing images",
	},
	{
		Name:         "git",
		MinVersion:   "v2.30.0",
		VersionCmd:   []string{"git", "--version"},
		VersionRegex: `git version (\d+\.\d+\.\d+)`,
		InstallCmds: []string{
			"apt-get update",
			"apt-get install -y git",
		},
		Description: "Version control for cloning repositories",
	},
	{
		Name:         "nixpacks",
		MinVersion:   "v1.21.0",
		VersionCmd:   []string{"nixpacks", "--version"},
		VersionRegex: `nixpacks (\d+\.\d+\.\d+)`,
		InstallCmds: []string{
			"curl -sSL https://nixpacks.com/install.sh | bash",
		},
		Description: "Auto-detect and build applications without Dockerfile",
	},
}

// DependencyManager handles checking and installing required tools
type DependencyManager struct {
	requirements []ToolRequirement
	timeout      time.Duration
}

// NewDependencyManager creates a new dependency manager with default requirements
func NewDependencyManager() *DependencyManager {
	return &DependencyManager{
		requirements: DefaultRequirements,
		timeout:      5 * time.Minute, // Default timeout for installations
	}
}

// WithRequirements sets custom requirements
func (dm *DependencyManager) WithRequirements(reqs []ToolRequirement) *DependencyManager {
	dm.requirements = reqs
	return dm
}

// WithTimeout sets the installation timeout
func (dm *DependencyManager) WithTimeout(timeout time.Duration) *DependencyManager {
	dm.timeout = timeout
	return dm
}

// ToolStatus represents the status of a single tool
type ToolStatus struct {
	Name         string
	Required     string // Required version
	Installed    string // Installed version (empty if not installed)
	NeedsInstall bool
	NeedsUpgrade bool
	Error        error
}

// CheckResult represents the result of checking all dependencies
type CheckResult struct {
	AllSatisfied bool
	Tools        []ToolStatus
}

// Check verifies all dependencies without installing
func (dm *DependencyManager) Check(ctx context.Context) *CheckResult {
	result := &CheckResult{
		AllSatisfied: true,
		Tools:        make([]ToolStatus, 0, len(dm.requirements)),
	}

	for _, req := range dm.requirements {
		status := dm.checkTool(ctx, req)
		result.Tools = append(result.Tools, status)

		if status.NeedsInstall || status.NeedsUpgrade || status.Error != nil {
			result.AllSatisfied = false
		}
	}

	return result
}

// checkTool checks a single tool's availability and version
func (dm *DependencyManager) checkTool(ctx context.Context, req ToolRequirement) ToolStatus {
	status := ToolStatus{
		Name:     req.Name,
		Required: req.MinVersion,
	}

	// Run version command
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, req.VersionCmd[0], req.VersionCmd[1:]...)
	output, err := cmd.CombinedOutput()

	if err != nil {
		// Tool not found or error running
		status.NeedsInstall = true
		return status
	}

	// Extract version using regex
	re := regexp.MustCompile(req.VersionRegex)
	matches := re.FindStringSubmatch(string(output))

	if len(matches) < 2 {
		status.Error = fmt.Errorf("could not parse version from output: %s", string(output))
		return status
	}

	// Add 'v' prefix for semver comparison if not present
	installedVersion := matches[1]
	if !strings.HasPrefix(installedVersion, "v") {
		installedVersion = "v" + installedVersion
	}
	status.Installed = installedVersion

	// Compare versions
	if !semver.IsValid(installedVersion) {
		status.Error = fmt.Errorf("invalid installed version: %s", installedVersion)
		return status
	}

	if !semver.IsValid(req.MinVersion) {
		status.Error = fmt.Errorf("invalid required version: %s", req.MinVersion)
		return status
	}

	// Check if upgrade needed
	if semver.Compare(installedVersion, req.MinVersion) < 0 {
		status.NeedsUpgrade = true
	}

	return status
}

// EnsureDependencies checks and installs/upgrades all required dependencies
// Returns error if any dependency cannot be satisfied
func (dm *DependencyManager) EnsureDependencies(ctx context.Context) error {
	log.Printf("🔍 Checking build dependencies...")

	// First, check current state
	checkResult := dm.Check(ctx)

	// Log current state
	for _, tool := range checkResult.Tools {
		if tool.Error != nil {
			log.Printf("   ⚠️  %s: error checking - %v", tool.Name, tool.Error)
		} else if tool.NeedsInstall {
			log.Printf("   ❌ %s: not installed (need %s)", tool.Name, tool.Required)
		} else if tool.NeedsUpgrade {
			log.Printf("   ⬆️  %s: %s installed (need %s)", tool.Name, tool.Installed, tool.Required)
		} else {
			log.Printf("   ✅ %s: %s", tool.Name, tool.Installed)
		}
	}

	if checkResult.AllSatisfied {
		log.Printf("✅ All build dependencies satisfied")
		return nil
	}

	// Install/upgrade required tools
	log.Printf("📦 Installing/upgrading missing dependencies...")

	for _, req := range dm.requirements {
		status := dm.checkTool(ctx, req)

		if !status.NeedsInstall && !status.NeedsUpgrade {
			continue
		}

		action := "Installing"
		if status.NeedsUpgrade {
			action = "Upgrading"
		}

		log.Printf("   %s %s...", action, req.Name)

		if err := dm.installTool(ctx, req); err != nil {
			return fmt.Errorf("failed to install %s: %w", req.Name, err)
		}

		// Verify installation
		newStatus := dm.checkTool(ctx, req)
		if newStatus.NeedsInstall || newStatus.NeedsUpgrade {
			return fmt.Errorf("%s installation failed: still needs %s, have %s",
				req.Name, req.MinVersion, newStatus.Installed)
		}

		log.Printf("   ✅ %s %s installed", req.Name, newStatus.Installed)
	}

	log.Printf("✅ All build dependencies installed successfully")
	return nil
}

// installTool runs the install commands for a tool
func (dm *DependencyManager) installTool(ctx context.Context, req ToolRequirement) error {
	ctx, cancel := context.WithTimeout(ctx, dm.timeout)
	defer cancel()

	for _, cmdStr := range req.InstallCmds {
		log.Printf("      Running: %s", cmdStr)

		// Parse command - handle pipes and shell features
		cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
		output, err := cmd.CombinedOutput()

		if err != nil {
			return fmt.Errorf("command '%s' failed: %w\nOutput: %s", cmdStr, err, string(output))
		}
	}

	return nil
}

// GetRequirement returns the requirement for a specific tool
func (dm *DependencyManager) GetRequirement(name string) *ToolRequirement {
	for _, req := range dm.requirements {
		if req.Name == name {
			return &req
		}
	}
	return nil
}

// IsToolAvailable checks if a specific tool is available with minimum version
func (dm *DependencyManager) IsToolAvailable(ctx context.Context, name string) bool {
	req := dm.GetRequirement(name)
	if req == nil {
		return false
	}

	status := dm.checkTool(ctx, *req)
	return !status.NeedsInstall && !status.NeedsUpgrade && status.Error == nil
}
