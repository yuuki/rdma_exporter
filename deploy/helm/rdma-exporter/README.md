# RDMA Exporter Helm Chart

This Helm chart deploys the RDMA Exporter as a DaemonSet on Kubernetes clusters with RDMA-capable hardware (InfiniBand/RoCE).

## Prerequisites

- Kubernetes 1.19+
- Helm 3.0+
- Nodes with RDMA hardware (InfiniBand or RoCE NICs)
- Read access to `/sys/class/infiniband` on each node

## Installation

### Add the repository (if published)

```bash
helm repo add rdma-exporter https://yuuki.github.io/rdma_exporter
helm repo update
```

### Install from local directory

```bash
helm install rdma-exporter ./deploy/helm/rdma-exporter -n monitoring --create-namespace
```

### Install with custom values

```bash
helm install rdma-exporter ./deploy/helm/rdma-exporter \
  -n monitoring \
  --set exporter.logLevel=debug \
  --set serviceMonitor.enabled=true
```

## Configuration

The following table lists the configurable parameters of the RDMA Exporter chart and their default values.

### Image Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `image.repository` | Image repository | `ghcr.io/yuuki/rdma_exporter` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `image.tag` | Image tag (defaults to chart appVersion) | `""` |
| `imagePullSecrets` | Image pull secrets | `[]` |

### Service Account

| Parameter | Description | Default |
|-----------|-------------|---------|
| `serviceAccount.create` | Create service account | `true` |
| `serviceAccount.annotations` | Service account annotations | `{}` |
| `serviceAccount.name` | Service account name override | `""` |

### Security Context

| Parameter | Description | Default |
|-----------|-------------|---------|
| `podSecurityContext.runAsNonRoot` | Run as non-root user | `true` |
| `podSecurityContext.runAsUser` | User ID to run as | `65534` |
| `podSecurityContext.runAsGroup` | Group ID to run as | `65534` |
| `podSecurityContext.fsGroup` | FSGroup for volume ownership | `65534` |
| `securityContext.allowPrivilegeEscalation` | Allow privilege escalation | `false` |
| `securityContext.readOnlyRootFilesystem` | Read-only root filesystem | `true` |
| `securityContext.capabilities.drop` | Capabilities to drop | `["ALL"]` |

### Networking

| Parameter | Description | Default |
|-----------|-------------|---------|
| `hostNetwork` | Use host network namespace (required for device access) | `true` |
| `hostPID` | Use host PID namespace | `false` |
| `service.enabled` | Enable service creation | `true` |
| `service.type` | Service type | `ClusterIP` |
| `service.port` | Service port | `9879` |
| `service.annotations` | Service annotations | `{}` |

### Exporter Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `exporter.listenAddress` | Address to listen on | `:9879` |
| `exporter.metricsPath` | Metrics endpoint path | `/metrics` |
| `exporter.healthPath` | Health check endpoint path | `/healthz` |
| `exporter.logLevel` | Log level (debug, info, warn, error) | `info` |
| `exporter.sysfsRoot` | Sysfs root path | `/sys` |
| `exporter.scrapeTimeout` | Scrape timeout duration | `5s` |
| `exporter.enableRoCEPFCMetrics` | Enable RoCE PFC metrics | `true` |
| `exporter.excludeDevices` | List of devices to exclude | `[]` |

### Resources

| Parameter | Description | Default |
|-----------|-------------|---------|
| `resources.limits.cpu` | CPU limit | `200m` |
| `resources.limits.memory` | Memory limit | `128Mi` |
| `resources.requests.cpu` | CPU request | `50m` |
| `resources.requests.memory` | Memory request | `64Mi` |

### Scheduling

| Parameter | Description | Default |
|-----------|-------------|---------|
| `nodeSelector` | Node selector labels | `{}` |
| `tolerations` | Tolerations for pod assignment | `[{effect: NoSchedule, operator: Exists}]` |
| `affinity` | Affinity rules | `{}` |
| `priorityClassName` | Priority class name | `""` |

### Prometheus Integration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `serviceMonitor.enabled` | Enable ServiceMonitor for Prometheus Operator | `false` |
| `serviceMonitor.interval` | Scrape interval | `30s` |
| `serviceMonitor.scrapeTimeout` | Scrape timeout | `10s` |
| `serviceMonitor.honorLabels` | Honor labels from exporter | `true` |
| `serviceMonitor.additionalLabels` | Additional labels for ServiceMonitor | `{}` |

## Examples

### Exclude specific devices (NVIDIA DGX/GB200 systems)

Some systems have restricted RDMA devices that should be excluded to prevent kernel log flooding:

```bash
helm install rdma-exporter ./deploy/helm/rdma-exporter \
  -n monitoring \
  --set exporter.excludeDevices="{mlx5_0,mlx5_1}"
```

### Enable Prometheus Operator integration

```bash
helm install rdma-exporter ./deploy/helm/rdma-exporter \
  -n monitoring \
  --set serviceMonitor.enabled=true \
  --set serviceMonitor.additionalLabels.prometheus=kube-prometheus
```

### Deploy on specific node pools

```bash
helm install rdma-exporter ./deploy/helm/rdma-exporter \
  -n monitoring \
  --set nodeSelector.node-role\\.kubernetes\\.io/rdma=true
```

### Custom resource limits for high-density clusters

```bash
helm install rdma-exporter ./deploy/helm/rdma-exporter \
  -n monitoring \
  --set resources.limits.cpu=500m \
  --set resources.limits.memory=256Mi
```

## Security Considerations

This chart follows security best practices:

1. **Non-root user**: Runs as UID 65534 (nobody)
2. **Read-only root filesystem**: Container filesystem is read-only
3. **Dropped capabilities**: All Linux capabilities are dropped
4. **No privilege escalation**: Prevents privilege escalation
5. **Host path access**: Only `/sys` is mounted read-only for RDMA metrics
6. **Host network**: Required for proper device visibility, but no sensitive ports exposed

## Upgrading

```bash
helm upgrade rdma-exporter ./deploy/helm/rdma-exporter -n monitoring
```

## Uninstallation

```bash
helm uninstall rdma-exporter -n monitoring
```

## Troubleshooting

### Pods not starting

Check if nodes have RDMA hardware:
```bash
kubectl get nodes -o custom-columns=NAME:.metadata.name,RDMA:.status.capacity.rdma/hca_shared_devices
```

### No metrics collected

Verify sysfs access:
```bash
kubectl exec -n monitoring daemonset/rdma-exporter -- ls -la /sys/class/infiniband
```

Check logs:
```bash
kubectl logs -n monitoring daemonset/rdma-exporter --all-containers
```

### Firmware-restricted devices

Some systems (NVIDIA DGX, Umbriel, GB200) have devices that trigger firmware errors. Exclude them:
```bash
helm upgrade rdma-exporter ./deploy/helm/rdma-exporter \
  -n monitoring \
  --set exporter.excludeDevices="{mlx5_0,mlx5_1}"
```

## Links

- [GitHub Repository](https://github.com/yuuki/rdma_exporter)
- [Grafana Dashboard](https://grafana.com/grafana/dashboards/24241)
- [Documentation](https://github.com/yuuki/rdma_exporter/tree/main/docs)
