# Pocket RelayMiner - Tilt Local Development

This directory contains the Tilt-based local development environment for Pocket RelayMiner (HA mode).

## Components

### 1. Genesis Generator (`genesis-generator/`)

Self-contained Go binary that generates `genesis.json` from YAML configuration.

**Features:**
- Uses Cosmos SDK types for 100% compatibility
- Derives addresses from mnemonics
- Builds poktroll module state (application, supplier, gateway, service)
- No external dependencies (no ignite CLI required)

**Usage:**
```bash
cd genesis-generator
go build -o ../../bin/genesis-generator
../../bin/genesis-generator -c ../../tilt_config.yaml -o ../../localnet/genesis.json
```

### 2. Backend Server (`backend-server/`)

Simple multi-protocol demo server for testing relay-miner capabilities.

**Protocols Supported:**
- HTTP JSON-RPC (`:8545`)
- WebSocket subscriptions (`:8545`)
- gRPC (`:50051`)
- SSE streaming (`/stream/sse`)
- NDJSON streaming (`/stream/ndjson`)

**Features:**
- Static mock JSON responses (simple, deterministic)
- Optional error injection (configurable via `config.yaml`)
- Prometheus metrics (`:9095`)
- Health check endpoint (`/health`)

**Configuration:**
```yaml
# backend-server/config.yaml
http_port: 8545
grpc_port: 50051
metrics_port: 9095
error_rate: 0.0      # 0.0-1.0 (0.05 = 5% error rate)
error_code: 500      # HTTP error code to inject
delay_ms: 0          # Latency simulation in milliseconds
```

**Build & Run:**
```bash
cd backend-server
docker build -t demo-backend .
docker run -p 8545:8545 -p 50051:50051 -p 9095:9095 demo-backend
```

## Tiltfile Architecture

Clean, modular, config-driven Tiltfile avoiding poktroll's messy patterns.

### File Structure

```
Tiltfile                    # Main orchestrator (thin glue layer)
tilt_config.yaml            # User-facing config (SINGLE SOURCE OF TRUTH)
tilt/
├── config.tilt            # Config loading & validation
├── defaults.tilt          # Default values
├── ports.tilt             # Centralized port registry
├── utils.tilt             # Helper functions (deep_merge)
├── redis.tilt             # Redis deployment (standalone + cluster)
├── validator.tilt         # Validator + genesis
├── miner.tilt             # Miner (2 replicas, pprof)
├── relayer.tilt           # Relayer (2 replicas, pprof)
├── backend.tilt           # Backend server
├── observability.tilt     # Prometheus + Grafana
├── genesis-generator/     # Genesis builder (Go binary)
└── backend-server/        # Demo backend (HTTP/WS/gRPC/Stream)
```

### Design Principles

**Avoided (from poktroll):**
- ❌ Hardcoded ports scattered everywhere
- ❌ Port arithmetic (8084 + actor_number)
- ❌ String concatenation for helm flags
- ❌ Monolithic 577-line Tiltfile
- ❌ Config duplication

**Implemented:**
- ✅ Centralized port registry (`ports.tilt`)
- ✅ Port generation functions (no arithmetic)
- ✅ Structured config templates
- ✅ Modular files (each <200 lines)
- ✅ Single source of truth (`tilt_config.yaml`)

### Startup Sequence

```
Redis → Validator (+ genesis) → Miners → Relayers → Backend → Observability
```

**Dependency Management:**
- **Validator**: Waits for Redis health
- **Miner**: Waits for validator health
- **Relayer**: Waits for validator + miner readiness (cache population)
- Relayers use Kubernetes readiness probes to avoid premature traffic

## Quick Start

### Prerequisites

- Docker
- Kubernetes (kind, minikube, or Docker Desktop)
- Tilt (`brew install tilt`)
- kubectl

### 1. Start Localnet

```bash
# From project root
tilt up
```

This will:
1. Build genesis generator
2. Build relay-miner Docker image (with hot reload)
3. Build backend server
4. Deploy Redis (standalone mode)
5. Deploy validator with genesis
6. Deploy 2 miners (with leader election)
7. Deploy 2 relayers (waits for miners)
8. Deploy backend server
9. Deploy Prometheus + Grafana

### 2. Access Services

- **Tilt UI**: http://localhost:10350
- **Redis Commander**: http://localhost:8081
- **Grafana**: http://localhost:3000 (admin/admin)
- **Prometheus**: http://localhost:9091
- **Relayer 1**: http://localhost:8180
- **Relayer 2**: http://localhost:8181
- **Validator RPC**: http://localhost:26657
- **Backend HTTP**: http://localhost:8545

### 3. Test Relay

```bash
# Send a test JSON-RPC request
curl -X POST http://localhost:8180 \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "eth_blockNumber",
    "params": [],
    "id": 1
  }'
```

### 4. View Metrics

- **Grafana Dashboards**: http://localhost:3000
  - Redis metrics (connections, memory, ops/sec)
  - Relayer metrics (RPS, latency, errors)
  - Miner metrics (SMST ops, claims, proofs)

- **Prometheus**: http://localhost:9091/targets
  - View all scrape targets and their health

### 5. Hot Reload

Code changes are automatically detected and the binary is rebuilt:

```bash
# Edit any Go file
vim relayer/proxy.go

# Binary rebuilds automatically
# Pods restart with new binary
```

## Configuration

Edit `tilt_config.yaml` to customize your environment:

### Scale Relayers/Miners

```yaml
relayer:
  count: 5  # Deploy 5 relayers

miner:
  count: 3  # Deploy 3 miners
```

### Use Redis Cluster

```yaml
redis:
  mode: "cluster"
  cluster:
    replicas: 3
    shards: 3
```

### Disable Observability

```yaml
observability:
  enabled: false
```

### Change Validation Mode

```yaml
relayer:
  config:
    validation_mode: eager  # or "optimistic"
```

## Debugging

### View Logs

```bash
# All resources
tilt logs

# Specific resource
tilt logs relayer1
tilt logs miner1
tilt logs redis
```

### pprof Profiling

```bash
# Relayer 1
go tool pprof http://localhost:6060/debug/pprof/profile

# Miner 1
go tool pprof http://localhost:6065/debug/pprof/profile
```

### Redis Debugging

- **Redis Commander UI**: http://localhost:8081
- **Redis CLI**:
  ```bash
  kubectl exec -it redis-0 -- redis-cli
  ```

### Port Conflicts

If you encounter port conflicts, edit `tilt_config.yaml`:

```yaml
relayer:
  base_port: 18180  # Use different base port
  metrics_base_port: 19190
  pprof_port: 16060
```

## Cleanup

```bash
# Stop all services
tilt down

# Delete kind cluster (optional)
kind delete cluster --name pocket-localnet
```

## Troubleshooting

### "Redis must be enabled"

At least one relayer or miner must be configured, and Redis is required for HA mode.

**Solution**: Ensure `redis.enabled: true` in `tilt_config.yaml`.

### "Keys file not found"

The keys file specified in `relayer.keys_file` doesn't exist.

**Solution**: Create `localnet/keyring/supplier-keys.yaml` or update the path.

### Relayers not receiving traffic

Relayers wait for miners to populate cache before becoming ready.

**Solution**: Check miner logs for cache initialization. Wait for miner readiness probes to pass.

### Genesis not applied

The genesis ConfigMap may not be created.

**Solution**: Run genesis-generator manually and create ConfigMap:
```bash
./bin/genesis-generator -c tilt_config.yaml -o localnet/genesis.json
kubectl create configmap genesis-config --from-file=genesis.json=localnet/genesis.json
```

## Architecture Diagrams

### Request Flow

```
Client
  ↓ HTTP/WS
Relayer (validates, signs, publishes)
  ↓ Redis Streams
Miner (builds SMST, submits claims/proofs)
  ↓ gRPC
Validator (Pocket Shannon blockchain)
```

### HA Failover

```
┌─────────┐     ┌─────────┐
│ Miner 1 │────▶│ Miner 2 │
│(Leader) │     │(Standby)│
└─────────┘     └─────────┘
     │               │
     └───────┬───────┘
             ↓
         Redis Lock
      (Leader Election)
```

## Performance Targets

- **Relayer**: 1000+ RPS per replica
- **Relay Validation**: <1ms average
- **SMST Update**: <100µs (in-memory + Redis)
- **Cache L1 Hit**: <100ns
- **Cache L2 Hit**: <2ms

## Next Steps

1. **Add Genesis Configuration**: Edit `tilt_config.yaml` to define accounts, suppliers, applications
2. **Create Supplier Keys**: Generate keys in `localnet/keyring/supplier-keys.yaml`
3. **Customize Dashboards**: Add Grafana dashboards in `localnet/grafana/`
4. **Load Testing**: Use `hey` or `wrk` to test relay throughput
5. **Production Testing**: Enable Redis cluster mode and test failover scenarios
