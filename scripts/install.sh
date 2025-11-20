#!/bin/bash
set -e

# Tenatrix Agent Installation Script
# Usage: install.sh --token <token> --do-token <do_token> --backend <url>

REPO="calyra-tenatrix/agent"
BINARY_NAME="tenatrix-agent-linux-amd64"
INSTALL_PATH="/usr/local/bin/tenatrix-agent"
CONFIG_DIR="/etc/tenatrix"
CONFIG_FILE="${CONFIG_DIR}/config.json"
SERVICE_FILE="/etc/systemd/system/tenatrix-agent.service"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Retry function for curl downloads
# Usage: retry_curl <url> <output_file>
retry_curl() {
  local url="$1"
  local output="$2"
  local max_attempts=5
  local wait_time=10
  local attempt=1

  while [ $attempt -le $max_attempts ]; do
    echo -e "${YELLOW}Attempt $attempt of $max_attempts...${NC}"

    if curl -fsSL "$url" -o "$output"; then
      echo -e "${GREEN}✅ Download successful${NC}"
      return 0
    else
      echo -e "${YELLOW}⚠️  Download failed (HTTP error or timeout)${NC}"

      if [ $attempt -lt $max_attempts ]; then
        echo -e "${YELLOW}Waiting ${wait_time} seconds before retry...${NC}"
        sleep $wait_time
      fi
    fi

    attempt=$((attempt + 1))
  done

  echo -e "${RED}❌ Failed after $max_attempts attempts${NC}"
  return 1
}

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --token)
      TENATRIX_TOKEN="$2"
      shift 2
      ;;
    --do-token)
      DO_TOKEN="$2"
      shift 2
      ;;
    --backend)
      BACKEND_URL="$2"
      shift 2
      ;;
    *)
      echo -e "${RED}Unknown option: $1${NC}"
      echo "Usage: install.sh --token <token> --do-token <do_token> --backend <url>"
      exit 1
      ;;
  esac
done

# Validate arguments
if [ -z "$TENATRIX_TOKEN" ] || [ -z "$DO_TOKEN" ] || [ -z "$BACKEND_URL" ]; then
  echo -e "${RED}Error: Missing required arguments${NC}"
  echo ""
  echo "Usage: install.sh --token <token> --do-token <do_token> --backend <url>"
  echo ""
  echo "Example:"
  echo "  install.sh --token 'uuid:signature' --do-token 'dop_v1_xxx' --backend 'grpc.tenatrix.com:9090'"
  exit 1
fi

# Check if running as root
if [ "$EUID" -ne 0 ]; then
  echo -e "${RED}Error: This script must be run as root${NC}"
  echo "Please run with sudo:"
  echo "  sudo bash install.sh --token ... --do-token ... --backend ..."
  exit 1
fi

echo -e "${GREEN}================================${NC}"
echo -e "${GREEN}Tenatrix Agent Installation${NC}"
echo -e "${GREEN}================================${NC}"
echo ""

# Get latest version
echo "📦 Fetching latest version..."
LATEST_VERSION=$(curl -s "https://api.github.com/repos/${REPO}/releases/latest" | \
  grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

if [ -z "$LATEST_VERSION" ]; then
  echo -e "${RED}❌ Failed to fetch latest version${NC}"
  exit 1
fi

echo -e "${GREEN}✅ Latest version: ${LATEST_VERSION}${NC}"
echo ""

# Download binary
echo "⬇️  Downloading binary..."
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${LATEST_VERSION}/${BINARY_NAME}"

if ! retry_curl "${DOWNLOAD_URL}" "/tmp/${BINARY_NAME}"; then
  exit 1
fi

if [ ! -f /tmp/${BINARY_NAME} ]; then
  echo -e "${RED}❌ Binary file not found after download${NC}"
  exit 1
fi

# Download and verify checksum
echo "🔐 Downloading checksums..."
CHECKSUM_URL="https://github.com/${REPO}/releases/download/${LATEST_VERSION}/checksums.txt"

if ! retry_curl "${CHECKSUM_URL}" "/tmp/checksums.txt"; then
  rm -f /tmp/${BINARY_NAME}
  exit 1
fi

cd /tmp
if sha256sum -c checksums.txt 2>&1 | grep -q "${BINARY_NAME}: OK"; then
  echo -e "${GREEN}✅ Checksum verified${NC}"
else
  echo -e "${RED}❌ Checksum verification failed!${NC}"
  rm -f /tmp/${BINARY_NAME} /tmp/checksums.txt
  exit 1
fi

# Install binary
echo "📥 Installing binary..."
mv /tmp/${BINARY_NAME} "${INSTALL_PATH}"
chmod +x "${INSTALL_PATH}"
echo -e "${GREEN}✅ Binary installed to ${INSTALL_PATH}${NC}"

# Create config directory
echo "📁 Creating configuration directory..."
mkdir -p "${CONFIG_DIR}"
echo -e "${GREEN}✅ Configuration directory created${NC}"

# Create config file
echo "⚙️  Creating configuration file..."
cat > "${CONFIG_FILE}" <<EOF
{
  "target_token": "${TENATRIX_TOKEN}",
  "do_token": "${DO_TOKEN}",
  "backend_url": "${BACKEND_URL}",
  "update_check_interval": "5m",
  "heartbeat_interval": "30s"
}
EOF

chmod 600 "${CONFIG_FILE}"
echo -e "${GREEN}✅ Configuration file created${NC}"

# Create systemd service
echo "🔧 Creating systemd service..."
cat > "${SERVICE_FILE}" <<'EOF'
[Unit]
Description=Tenatrix Agent
After=network.target
Documentation=https://github.com/calyra-tenatrix/agent

[Service]
Type=simple
ExecStart=/usr/local/bin/tenatrix-agent run
Restart=always
RestartSec=5
User=root

# Auto-restart on successful exit (for updates)
RestartForceExitStatus=0

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=tenatrix-agent

# Security
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

echo -e "${GREEN}✅ Systemd service created${NC}"

# Create systemd timer service for auto-updates
echo "⏰ Creating auto-update timer service..."
cat > "/etc/systemd/system/tenatrix-agent-update.service" <<'EOF'
[Unit]
Description=Tenatrix Agent Update Check
Documentation=https://github.com/calyra-tenatrix/agent

[Service]
Type=oneshot
ExecStart=/usr/local/bin/tenatrix-agent --mode update
User=root

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=tenatrix-agent-update
EOF

echo -e "${GREEN}✅ Update service created${NC}"

# Create systemd timer for auto-updates
echo "⏰ Creating auto-update timer..."
cat > "/etc/systemd/system/tenatrix-agent-update.timer" <<'EOF'
[Unit]
Description=Tenatrix Agent Update Check Timer
Documentation=https://github.com/calyra-tenatrix/agent

[Timer]
# Run every 1 minute
OnBootSec=1min
OnUnitActiveSec=1min

[Install]
WantedBy=timers.target
EOF

echo -e "${GREEN}✅ Update timer created${NC}"

# Reload systemd
echo "🔄 Reloading systemd..."
systemctl daemon-reload
echo -e "${GREEN}✅ Systemd reloaded${NC}"

# Enable and start agent service
echo "🚀 Enabling agent service..."
systemctl enable tenatrix-agent
echo -e "${GREEN}✅ Agent service enabled${NC}"

echo "▶️  Starting agent service..."
systemctl start tenatrix-agent
echo -e "${GREEN}✅ Agent service started${NC}"

# Enable and start update timer
echo "⏰ Enabling auto-update timer..."
systemctl enable tenatrix-agent-update.timer
echo -e "${GREEN}✅ Auto-update timer enabled${NC}"

echo "▶️  Starting auto-update timer..."
systemctl start tenatrix-agent-update.timer
echo -e "${GREEN}✅ Auto-update timer started${NC}"

# Clean up
rm -f /tmp/checksums.txt

echo ""
echo -e "${GREEN}================================${NC}"
echo -e "${GREEN}✅ Installation complete!${NC}"
echo -e "${GREEN}================================${NC}"
echo ""
echo "Tenatrix Agent ${LATEST_VERSION} is now running."
echo "Auto-update timer is active (checks every minute)."
echo ""
echo "Useful commands:"
echo "  Agent status:        sudo systemctl status tenatrix-agent"
echo "  Agent logs:          sudo journalctl -u tenatrix-agent -f"
echo "  Update timer status: sudo systemctl status tenatrix-agent-update.timer"
echo "  Update logs:         sudo journalctl -u tenatrix-agent-update -f"
echo "  Manual update check: sudo /usr/local/bin/tenatrix-agent --mode update"
echo "  Restart agent:       sudo systemctl restart tenatrix-agent"
echo "  Stop agent:          sudo systemctl stop tenatrix-agent"
echo ""