# Helm Chart Architecture

## Deployment Model

The RDMA Exporter is deployed as a **DaemonSet** to ensure one instance runs on every node with RDMA hardware.

```
┌───────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                     │
│                                                           │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐           │
│  │   Node 1   │  │   Node 2   │  │   Node N   │           │
│  │            │  │            │  │            │           │
│  │ ┌────────┐ │  │ ┌────────┐ │  │ ┌────────┐ │           │
│  │ │ RDMA   │ │  │ │ RDMA   │ │  │ │ RDMA   │ │           │
│  │ │Exporter│ │  │ │Exporter│ │  │ │Exporter│ │           │
│  │ │  Pod   │ │  │ │  Pod   │ │  │ │  Pod   │ │           │
│  │ └───┬────┘ │  │ └───┬────┘ │  │ └───┬────┘ │           │
│  │     │      │  │     │      │  │     │      │           │
│  │     │ RO   │  │     │ RO   │  │     │ RO   │           │
│  │     ▼      │  │     ▼      │  │     ▼      │           │
│  │   /sys/    │  │   /sys/    │  │   /sys/    │           │
│  │ class/     │  │ class/     │  │ class/     │           │
│  │ infiniband │  │ infiniband │  │ infiniband │           │
│  └────────────┘  └────────────┘  └────────────┘           │
│         │              │              │                   │
│         └──────────────┴──────────────┘                   │
│                        │                                  │
│                        ▼                                  │
│              ┌──────────────────┐                         │
│              │   Prometheus     │                         │
│              │   (Scraping)     │                         │
│              └──────────────────┘                         │
└───────────────────────────────────────────────────────────┘
```

## Components

### 1. DaemonSet

**Purpose**: Ensures RDMA metrics collection from every RDMA-capable node

**Key Features**:
- One pod per node
- Automatic scheduling on new nodes
- Rolling updates with `maxUnavailable: 1`
- Node affinity and tolerations for selective deployment

### 2. Pod Configuration

Each pod consists of a single container:

```yaml
Container: rdma-exporter
├── Image: ghcr.io/yuuki/rdma_exporter:0.4.1
├── Port: 9879 (metrics)
├── Security Context:
│   ├── User: 65534 (nobody)
│   ├── Read-only rootfs: true
│   ├── Capabilities: none (all dropped)
│   └── No privilege escalation
├── Volume Mounts:
│   └── /sys → hostPath (read-only)
├── Environment:
│   └── NODE_NAME (from downward API)
└── Probes:
    ├── Liveness: /healthz (30s interval)
    └── Readiness: /healthz (10s interval)
```

### 3. Service

**Type**: Headless (ClusterIP: None)

**Purpose**: Service discovery for Prometheus scraping

```yaml
Service: rdma-exporter
├── Type: ClusterIP
├── ClusterIP: None (headless)
├── Port: 9879
└── Selector: app.kubernetes.io/name=rdma-exporter
```

Headless service enables:
- Direct pod-to-pod communication
- Prometheus can scrape all pods individually
- DNS returns all pod IPs

### 4. ServiceMonitor (Optional)

For Prometheus Operator integration:

```yaml
ServiceMonitor: rdma-exporter
├── Selector: matches service labels
├── Endpoints:
│   ├── Port: metrics (9879)
│   ├── Path: /metrics
│   ├── Interval: 30s
│   └── Timeout: 10s
└── Labels: (customizable for Prometheus selection)
```

### 5. ServiceAccount

Minimal permissions, token not mounted:

```yaml
ServiceAccount: rdma-exporter
└── automountServiceAccountToken: false
```

## Data Flow

### Metrics Collection Flow

```
┌──────────────┐
│  Host RDMA   │
│   Hardware   │
└──────┬───────┘
       │
       ▼ (kernel driver)
┌──────────────────────┐
│  /sys/class/         │
│  infiniband/         │
│    ├── mlx5_0/       │
│    │   ├── ports/    │
│    │   └── counters/ │
│    └── mlx5_1/       │
└──────┬───────────────┘
       │ (read-only mount)
       ▼
┌──────────────────────┐
│   RDMA Exporter Pod  │
│   ┌──────────────┐   │
│   │  Collector   │   │
│   │  (reads      │   │
│   │   counters)  │   │
│   └──────┬───────┘   │
│          │           │
│   ┌──────▼───────┐   │
│   │  Prometheus  │   │
│   │   Client     │   │
│   │   (exposes)  │   │
│   └──────┬───────┘   │
│          │           │
│   ┌──────▼───────┐   │
│   │ HTTP Server  │   │
│   │  :9879       │   │
│   └──────┬───────┘   │
└──────────┼───────────┘
           │ (HTTP GET /metrics)
           ▼
    ┌─────────────┐
    │ Prometheus  │
    │   Server    │
    └─────────────┘
```

### Scrape Path

1. Prometheus discovers pods via ServiceMonitor or Kubernetes SD
2. Prometheus sends HTTP GET to `http://<pod-ip>:9879/metrics`
3. Exporter reads RDMA counters from `/sys/class/infiniband`
4. Exporter formats as Prometheus text format
5. Prometheus stores time-series data

## Security Architecture

### Defense in Depth Layers

```
┌─────────────────────────────────────────────────┐
│  Layer 1: Network Isolation                     │
│  - Namespace boundaries                         │
│  - Network policies (optional)                  │
│  - Service mesh (optional)                      │
└─────────────────┬───────────────────────────────┘
                  │
┌─────────────────▼───────────────────────────────┐
│  Layer 2: Pod Security Context                  │
│  - runAsNonRoot: true                           │
│  - runAsUser: 65534                             │
│  - seccompProfile: RuntimeDefault               │
└─────────────────┬───────────────────────────────┘
                  │
┌─────────────────▼───────────────────────────────┐
│  Layer 3: Container Security                    │
│  - readOnlyRootFilesystem: true                 │
│  - allowPrivilegeEscalation: false              │
│  - capabilities: drop ALL                       │
└─────────────────┬───────────────────────────────┘
                  │
┌─────────────────▼───────────────────────────────┐
│  Layer 4: Volume Security                       │
│  - /sys mounted read-only                       │
│  - No other host mounts                         │
└─────────────────┬───────────────────────────────┘
                  │
┌─────────────────▼───────────────────────────────┐
│  Layer 5: Resource Limits                       │
│  - CPU: 200m limit, 50m request                 │
│  - Memory: 128Mi limit, 64Mi request            │
└─────────────────────────────────────────────────┘
```

## Update Strategy

### Rolling Update Flow

```
Initial State: 3 nodes with RDMA Exporter
┌────┐ ┌────┐ ┌────┐
│ v1 │ │ v1 │ │ v1 │
└────┘ └────┘ └────┘

Step 1: Update first pod (maxUnavailable: 1)
┌────┐ ┌────┐ ┌────┐
│ v2 │ │ v1 │ │ v1 │
└────┘ └────┘ └────┘

Step 2: Wait for readiness, update second pod
┌────┐ ┌────┐ ┌────┐
│ v2 │ │ v2 │ │ v1 │
└────┘ └────┘ └────┘

Step 3: Update final pod
┌────┐ ┌────┐ ┌────┐
│ v2 │ │ v2 │ │ v2 │
└────┘ └────┘ └────┘

Final State: All pods updated
```

**Configuration**:
```yaml
updateStrategy:
  type: RollingUpdate
  rollingUpdate:
    maxUnavailable: 1
```

## Resource Management

### CPU and Memory Profiles

**Default Profile** (suitable for most deployments):
```yaml
resources:
  requests:
    cpu: 50m      # 5% of one core
    memory: 64Mi
  limits:
    cpu: 200m     # 20% of one core
    memory: 128Mi
```

**High-Density Profile** (many devices per node):
```yaml
resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 256Mi
```

**Minimal Profile** (resource-constrained environments):
```yaml
resources:
  requests:
    cpu: 25m
    memory: 32Mi
  limits:
    cpu: 100m
    memory: 64Mi
```

## Failure Modes and Recovery

### 1. Pod Failure
- **Detection**: Liveness probe fails
- **Action**: Kubelet restarts container
- **Impact**: ~30s metrics gap for that node

### 2. Node Failure
- **Detection**: Node NotReady
- **Action**: No automatic recovery (DaemonSet doesn't reschedule)
- **Impact**: No metrics from failed node
- **Recovery**: When node returns, pod automatically starts

### 3. Sysfs Read Errors
- **Detection**: `rdma_scrape_errors_total` metric increments
- **Action**: Exporter continues, logs error
- **Impact**: Missing metrics for affected devices
- **Recovery**: Automatic on next scrape

### 4. Resource Exhaustion
- **Detection**: OOMKilled or CPU throttling
- **Action**: Pod restart with backoff
- **Impact**: Temporary metrics loss
- **Recovery**: Automatic restart, alert on frequent OOM

## Monitoring the Monitor

### Key Metrics to Watch

1. **Pod Health**:
   ```promql
   kube_pod_status_phase{pod=~"rdma-exporter.*", phase!="Running"}
   ```

2. **Scrape Errors**:
   ```promql
   rate(rdma_scrape_errors_total[5m]) > 0
   ```

3. **Resource Usage**:
   ```promql
   container_memory_working_set_bytes{pod=~"rdma-exporter.*"}
   container_cpu_usage_seconds_total{pod=~"rdma-exporter.*"}
   ```

4. **Scrape Success**:
   ```promql
   up{job="rdma-exporter"}
   ```

## Scaling Considerations

### Vertical Scaling (Per-Pod Resources)

Increase when:
- Many RDMA devices per node (>8)
- High scrape frequency (<10s)
- Complex network topologies
- RoCE PFC metrics enabled

### Horizontal Scaling

DaemonSets automatically scale horizontally:
- One pod per node
- New nodes automatically get pods
- Node removal automatically removes pods

No manual scaling required.

## Integration Points

### Prometheus

**Discovery Methods**:
1. ServiceMonitor (Prometheus Operator)
2. Kubernetes SD with pod annotations
3. Static configuration

**Recommended Scrape Config**:
```yaml
scrape_interval: 30s
scrape_timeout: 10s
metric_relabel_configs:
  - source_labels: [__meta_kubernetes_pod_node_name]
    target_label: node
```

### Grafana

Import dashboard: https://grafana.com/grafana/dashboards/24241

Key panels:
- RDMA traffic rates by port
- Error counters
- Link state and speed
- RoCE PFC metrics

### Alerting

Example alerts in `examples/alerts.yaml`:
- High error rate
- Link down
- Scrape failures
- Resource exhaustion

## Design Decisions

### Why DaemonSet?

✅ **Advantages**:
- Automatic per-node deployment
- No manual scheduling needed
- Tolerates node additions/removals
- Simple operational model

❌ **Alternatives Considered**:
- **Deployment**: Would require complex anti-affinity rules
- **StatefulSet**: Unnecessary state management overhead
- **Job**: Not suitable for continuous monitoring

### Why Host Network?

✅ **Required For**:
- Direct RDMA device visibility
- Accurate network interface correlation
- RoCE PFC metrics collection

⚠️ **Security Implications**:
- Mitigated by running as unprivileged user
- Only port 9879 exposed
- No sensitive services on that port

### Why Read-Only /sys Mount?

✅ **Benefits**:
- Principle of least privilege
- Prevents accidental system modification
- Reduces attack surface

✅ **Sufficient Because**:
- Exporter only reads counters
- No device configuration needed
- Metrics are read-only by nature
