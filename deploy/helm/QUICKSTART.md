# Helm Deployment Quick Start

## Prerequisites

- Kubernetes cluster with RDMA-capable nodes (InfiniBand or RoCE)
- Helm 3.0+
- `kubectl` configured with cluster access

## Basic Installation

```bash
helm install rdma-exporter ./rdma-exporter \
  --namespace monitoring \
  --create-namespace
```

## Verify Deployment

```bash
kubectl get daemonset -n monitoring
kubectl get pods -n monitoring -l app.kubernetes.io/name=rdma-exporter
kubectl logs -n monitoring daemonset/rdma-exporter
```

## Access Metrics

Port-forward to a pod:
```bash
POD=$(kubectl get pods -n monitoring -l app.kubernetes.io/name=rdma-exporter -o jsonpath='{.items[0].metadata.name}')
kubectl port-forward -n monitoring $POD 9879:9879
```

Then visit:
- Metrics: http://localhost:9879/metrics
- Health: http://localhost:9879/healthz

## Common Configurations

### Enable Prometheus Operator ServiceMonitor

```bash
helm upgrade rdma-exporter ./rdma-exporter \
  -n monitoring \
  --set serviceMonitor.enabled=true \
  --set serviceMonitor.additionalLabels.prometheus=kube-prometheus
```

### Exclude Devices (NVIDIA DGX/GB200)

```bash
helm upgrade rdma-exporter ./rdma-exporter \
  -n monitoring \
  --set exporter.excludeDevices="{mlx5_0,mlx5_1}"
```

### Deploy on Specific Nodes

```bash
helm upgrade rdma-exporter ./rdma-exporter \
  -n monitoring \
  --set nodeSelector.node-role\\.kubernetes\\.io/rdma="true"
```

### Use Example Configuration

```bash
helm install rdma-exporter ./rdma-exporter \
  -n monitoring \
  -f examples/prometheus-operator.yaml
```

## Troubleshooting

### Check if pods are running
```bash
kubectl get pods -n monitoring -l app.kubernetes.io/name=rdma-exporter
```

### View logs
```bash
kubectl logs -n monitoring daemonset/rdma-exporter --all-containers
```

### Verify RDMA devices are accessible
```bash
kubectl exec -n monitoring daemonset/rdma-exporter -- ls -la /sys/class/infiniband
```

### Test metrics endpoint
```bash
kubectl exec -n monitoring daemonset/rdma-exporter -- wget -qO- http://localhost:9879/metrics
```

## Uninstall

```bash
helm uninstall rdma-exporter -n monitoring
```

## Next Steps

- Review [README.md](rdma-exporter/README.md) for detailed configuration options
- Check [examples/](rdma-exporter/examples/) for more deployment scenarios
- Visit the [Grafana dashboard](https://grafana.com/grafana/dashboards/24241) for visualization
