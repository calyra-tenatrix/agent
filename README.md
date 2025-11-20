# Tenatrix Agent

Tenatrix Agent is a lightweight monitoring and management agent for DigitalOcean droplets.

## Features

- ✅ Heartbeat monitoring (30s interval)
- ✅ System metrics collection (CPU, memory, disk)
- ✅ Auto-update from GitHub Releases
- ✅ gRPC bidirectional streaming
- ✅ Systemd integration

## Installation

### Automated Installation

```bash
curl -fsSL https://raw.githubusercontent.com/calyra-tenatrix/agent/main/scripts/install.sh | \
  bash -s -- --token <tenatrix_token> --do-token <do_token> --backend <backend_url>
```

### Manual Installation

1. Download the latest release:
```bash
curl -fsSL https://github.com/calyra-tenatrix/agent/releases/latest/download/tenatrix-agent-linux-amd64 \
  -o /usr/local/bin/tenatrix-agent
chmod +x /usr/local/bin/tenatrix-agent
```

2. Create configuration file:
```bash
sudo mkdir -p /etc/tenatrix
sudo cat > /etc/tenatrix/config.json <<EOF
{
  "target_token": "your-target-token",
  "do_token": "your-digitalocean-token",
  "backend_url": "grpc.tenatrix.com:9090",
  "update_check_interval": "5m",
  "heartbeat_interval": "30s"
}
EOF
sudo chmod 600 /etc/tenatrix/config.json
```

3. Create systemd service:
```bash
sudo cat > /etc/systemd/system/tenatrix-agent.service <<EOF
[Unit]
Description=Tenatrix Agent
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/tenatrix-agent run
Restart=always
RestartSec=5
User=root

[Install]
WantedBy=multi-user.target
EOF
```

4. Enable and start:
```bash
sudo systemctl daemon-reload
sudo systemctl enable tenatrix-agent
sudo systemctl start tenatrix-agent
```

## Configuration

The agent is configured via `/etc/tenatrix/config.json`:

| Field | Description | Example |
|-------|-------------|---------|
| `target_token` | Signed token from Tenatrix backend | `uuid:signature` |
| `do_token` | DigitalOcean API token | `dop_v1_...` |
| `backend_url` | gRPC server address | `grpc.tenatrix.com:9090` |
| `update_check_interval` | How often to check for updates | `5m` |
| `heartbeat_interval` | Heartbeat frequency | `30s` |

## Usage

### Run Agent
```bash
tenatrix-agent run
```

### Check for Updates
```bash
tenatrix-agent --mode update
```

### View Logs
```bash
journalctl -u tenatrix-agent -f
```

### Check Status
```bash
systemctl status tenatrix-agent
```

## Development

### Build

```bash
cd agent
go build -o tenatrix-agent cmd/agent/main.go
```

### Build Static Binary

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags "-X main.version=dev" \
  -o tenatrix-agent cmd/agent/main.go
```

### Local Testing

```bash
# Create local config
cp config.example.json /tmp/config.json
# Edit with your tokens

# Run locally
./tenatrix-agent --config /tmp/config.json run
```

## Architecture

```
┌─────────────────┐
│  Tenatrix Agent │
└────────┬────────┘
         │
         │ gRPC (TLS)
         │
┌────────▼────────┐
│  Backend Server │
│   (port 9090)   │
└─────────────────┘
```

### Components

- **cmd/agent**: Entry point and CLI
- **internal/agent**: Core agent logic and gRPC client
- **internal/config**: Configuration management
- **internal/system**: System metrics collection
- **internal/updater**: Auto-update mechanism

## Security

- Config file protected with `chmod 600` (root only)
- Token authentication via gRPC metadata
- HMAC-SHA256 signature verification
- SHA256 checksum verification for updates
- TLS encrypted gRPC connection (production)

## License

Proprietary - Tenatrix
