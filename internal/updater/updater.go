package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"syscall"

	"github.com/calyra-tenatrix/agent/internal/config"
)

const (
	defaultBinaryPath = "/usr/local/bin/tenatrix-agent"
	backupSuffix      = ".bak"
	githubRepo        = "calyra-tenatrix/agent"
)

// Updater handles agent updates
type Updater struct {
	currentVersion string
	binaryPath     string
	github         *GitHubClient
}

// NewUpdater creates a new updater
func NewUpdater(currentVersion string) *Updater {
	return &Updater{
		currentVersion: currentVersion,
		binaryPath:     defaultBinaryPath,
		github:         NewGitHubClient(githubRepo),
	}
}

// CheckAndUpdate checks for updates and applies them
func CheckAndUpdate(ctx context.Context, cfg *config.Config, currentVersion string) error {
	log.Printf("🔍 Checking for updates (current version: %s)", currentVersion)

	updater := NewUpdater(currentVersion)
	return updater.Run(ctx)
}

// Run executes the update process
func (u *Updater) Run(ctx context.Context) error {
	// 1. Check latest release
	latest, err := u.github.GetLatestRelease()
	if err != nil {
		return fmt.Errorf("failed to check latest release: %w", err)
	}

	log.Printf("📦 Latest version: %s (released: %s)", latest.Version, latest.ReleaseDate)

	// 2. Compare versions
	if latest.Version == u.currentVersion {
		log.Printf("✅ Already up to date")
		return nil
	}

	log.Printf("🆕 New version available: %s -> %s", u.currentVersion, latest.Version)

	// 3. Download binary
	log.Printf("⬇️  Downloading binary...")
	binary, err := u.github.DownloadBinary(latest.DownloadURL)
	if err != nil {
		return fmt.Errorf("failed to download binary: %w", err)
	}

	log.Printf("✅ Downloaded %d bytes", len(binary))

	// 4. Verify checksum
	log.Printf("🔐 Verifying checksum...")
	if !u.verifyChecksum(binary, latest.Checksum) {
		return fmt.Errorf("checksum mismatch! expected: %s", latest.Checksum)
	}

	log.Printf("✅ Checksum verified")

	// 5. Backup current binary
	log.Printf("💾 Creating backup...")
	if err := u.backup(); err != nil {
		return fmt.Errorf("failed to create backup: %w", err)
	}

	// 6. Atomic swap
	log.Printf("🔄 Installing new binary...")
	if err := u.atomicSwap(binary); err != nil {
		log.Printf("❌ Installation failed, restoring backup...")
		if restoreErr := u.restore(); restoreErr != nil {
			return fmt.Errorf("failed to install and restore: %w, %w", err, restoreErr)
		}
		return fmt.Errorf("failed to install binary: %w", err)
	}

	log.Printf("✅ Update successful: %s -> %s", u.currentVersion, latest.Version)
	log.Printf("🔄 Restarting agent...")

	// 7. Restart self (systemd will handle the restart)
	u.restartSelf()

	return nil
}

// verifyChecksum verifies the binary checksum
func (u *Updater) verifyChecksum(data []byte, expected string) bool {
	hash := sha256.Sum256(data)
	actual := hex.EncodeToString(hash[:])
	return actual == expected
}

// backup creates a backup of the current binary
func (u *Updater) backup() error {
	backupPath := u.binaryPath + backupSuffix

	// Read current binary
	data, err := os.ReadFile(u.binaryPath)
	if err != nil {
		return fmt.Errorf("failed to read current binary: %w", err)
	}

	// Write backup
	if err := os.WriteFile(backupPath, data, 0755); err != nil {
		return fmt.Errorf("failed to write backup: %w", err)
	}

	return nil
}

// atomicSwap replaces the binary with a new one atomically
func (u *Updater) atomicSwap(newBinary []byte) error {
	tmpPath := u.binaryPath + ".new"

	// Write new binary to temp file
	if err := os.WriteFile(tmpPath, newBinary, 0755); err != nil {
		return fmt.Errorf("failed to write new binary: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, u.binaryPath); err != nil {
		os.Remove(tmpPath) // Clean up
		return fmt.Errorf("failed to rename binary: %w", err)
	}

	return nil
}

// restore restores the backup binary
func (u *Updater) restore() error {
	backupPath := u.binaryPath + backupSuffix

	if err := os.Rename(backupPath, u.binaryPath); err != nil {
		return fmt.Errorf("failed to restore backup: %w", err)
	}

	log.Printf("✅ Backup restored successfully")
	return nil
}

// restartSelf terminates the current process
// Systemd will automatically restart the agent
func (u *Updater) restartSelf() {
	// Send SIGTERM to self
	// Systemd's Restart=always will restart the agent
	syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
}
