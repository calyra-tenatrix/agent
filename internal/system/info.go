package system

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/process"
)

// SystemInfo holds system information
type SystemInfo struct {
	// Host info
	Hostname      string
	IPAddress     string
	Architecture  string
	OSVersion     string
	KernelVersion string

	// Resource usage
	CPUPercent     float64
	MemoryUsed     int64
	MemoryTotal    int64 // Total system memory (bytes)
	MemoryPercent  float64
	DiskUsed       int64 // Used disk space (bytes)
	DiskTotal      int64 // Total disk space (bytes)
	DiskPercent    float64
	ProcessCount   int
	GoroutineCount int

	// Cloud info
	CloudRegion string

	// Uptime
	Uptime       time.Duration // Agent process uptime
	SystemUptime time.Duration // Droplet/system uptime
}

var startTime = time.Now()

// Collect gathers system information
func Collect() (*SystemInfo, error) {
	info := &SystemInfo{
		Architecture:   runtime.GOARCH,
		GoroutineCount: runtime.NumGoroutine(),
		Uptime:         time.Since(startTime),
	}

	// Get hostname
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	info.Hostname = hostname

	// Get host info (includes system uptime)
	hostInfo, err := host.Info()
	if err == nil {
		info.OSVersion = fmt.Sprintf("%s %s", hostInfo.Platform, hostInfo.PlatformVersion)
		info.KernelVersion = hostInfo.KernelVersion
		// System uptime (droplet uptime, not agent uptime)
		info.SystemUptime = time.Duration(hostInfo.Uptime) * time.Second
	}

	// Get CPU usage
	cpuPercent, err := cpu.Percent(time.Second, false)
	if err == nil && len(cpuPercent) > 0 {
		info.CPUPercent = cpuPercent[0]
	}

	// Get memory usage (used + total)
	memInfo, err := mem.VirtualMemory()
	if err == nil {
		info.MemoryUsed = int64(memInfo.Used)
		info.MemoryTotal = int64(memInfo.Total)
		info.MemoryPercent = memInfo.UsedPercent
	}

	// Get disk usage (used + total)
	diskInfo, err := disk.Usage("/")
	if err == nil {
		info.DiskUsed = int64(diskInfo.Used)
		info.DiskTotal = int64(diskInfo.Total)
		info.DiskPercent = diskInfo.UsedPercent
	}

	// Get process count
	processes, err := process.Processes()
	if err == nil {
		info.ProcessCount = len(processes)
	}

	// Detect DigitalOcean metadata
	info.IPAddress = detectIP()
	info.CloudRegion = detectRegion()

	return info, nil
}

// detectIP tries to get the droplet's IP address
func detectIP() string {
	// Try DigitalOcean metadata service
	ip := getMetadata("http://169.254.169.254/metadata/v1/interfaces/public/0/ipv4/address")
	if ip != "" {
		return ip
	}

	// Fallback: try external IP services
	ip = getExternalIP("https://api.ipify.org")
	if ip != "" {
		return ip
	}

	return "unknown"
}

// detectRegion tries to get the droplet's region
func detectRegion() string {
	// Try DigitalOcean metadata service
	region := getMetadata("http://169.254.169.254/metadata/v1/region")
	if region != "" {
		return region
	}

	return "unknown"
}

// getMetadata fetches data from DigitalOcean metadata service
func getMetadata(url string) string {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(body))
}

// getExternalIP fetches the public IP from an external service
func getExternalIP(url string) string {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(body))
}
