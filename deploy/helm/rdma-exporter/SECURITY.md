# Security Configuration

This document details the security measures implemented in the RDMA Exporter Helm chart.

## Overview

The Helm chart follows Kubernetes security best practices to minimize attack surface while maintaining functionality for RDMA metrics collection.

## Security Features

### 1. Non-Root User Execution

```yaml
podSecurityContext:
  runAsNonRoot: true
  runAsUser: 65534      # nobody user
  runAsGroup: 65534     # nogroup
  fsGroup: 65534
```

The exporter runs as UID 65534 (nobody), preventing privilege escalation and limiting damage from potential exploits.

### 2. Read-Only Root Filesystem

```yaml
securityContext:
  readOnlyRootFilesystem: true
```

The container's root filesystem is mounted read-only, preventing:
- Malicious code injection
- Unauthorized file modifications
- Persistent backdoors

### 3. Dropped Linux Capabilities

```yaml
securityContext:
  capabilities:
    drop:
    - ALL
```

All Linux capabilities are dropped, adhering to the principle of least privilege. The exporter requires no special capabilities.

### 4. No Privilege Escalation

```yaml
securityContext:
  allowPrivilegeEscalation: false
```

Prevents processes from gaining more privileges than their parent process.

### 5. Seccomp Profile

```yaml
podSecurityContext:
  seccompProfile:
    type: RuntimeDefault
```

Uses the runtime's default seccomp profile to restrict system calls, reducing the kernel attack surface.

### 6. No Service Account Token

```yaml
serviceAccount:
  automountServiceAccountToken: false
```

The exporter doesn't need Kubernetes API access, so the service account token is not mounted.

## Host Access Configuration

### Host Network

```yaml
hostNetwork: true
```

**Required**: Enables direct access to RDMA devices and ensures accurate network interface visibility.

**Risk Mitigation**: 
- Only ports 9879 (metrics) and health endpoint are exposed
- No sensitive services on this port range
- Firewall rules can restrict access

### Host Path Mount

```yaml
volumeMounts:
  - name: sys
    mountPath: /sys
    readOnly: true

volumes:
  - name: sys
    hostPath:
      path: /sys
      type: Directory
```

**Required**: Read-only access to `/sys/class/infiniband` for RDMA statistics.

**Risk Mitigation**:
- Mounted **read-only** - no write access to sysfs
- Only sysfs is mounted, not entire root filesystem
- No access to sensitive host paths like `/proc`, `/var`, `/etc`

## Network Security

### Service Configuration

```yaml
service:
  type: ClusterIP
  clusterIP: None     # Headless service
```

Uses a headless service (ClusterIP: None) for direct pod communication without load balancing overhead.

### Prometheus Integration

Metrics are scraped via:
1. **ServiceMonitor** (Prometheus Operator): Automatic discovery and secure scraping
2. **Pod annotations**: Compatible with standard Prometheus Kubernetes SD

```yaml
podAnnotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "9879"
  prometheus.io/path: "/metrics"
```

## Scheduling Security

### Tolerations

```yaml
tolerations:
  - effect: NoSchedule
    operator: Exists
```

Allows deployment on all nodes (including tainted ones) to ensure comprehensive RDMA monitoring.

### Resource Limits

```yaml
resources:
  limits:
    cpu: 200m
    memory: 128Mi
  requests:
    cpu: 50m
    memory: 64Mi
```

Resource limits prevent:
- Resource exhaustion attacks
- Noisy neighbor problems
- Cluster instability from runaway processes

## Deployment Best Practices

### 1. Namespace Isolation

Deploy in a dedicated monitoring namespace:
```bash
helm install rdma-exporter ./rdma-exporter -n monitoring --create-namespace
```

### 2. Network Policies (Optional)

Restrict ingress to only Prometheus:
```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: rdma-exporter-netpol
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: rdma-exporter
  policyTypes:
  - Ingress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          name: monitoring
    ports:
    - protocol: TCP
      port: 9879
```

### 3. Pod Security Standards

This chart is compatible with the **Restricted** Pod Security Standard with the exception of:
- `hostNetwork: true` (required for RDMA device access)
- Host path mount to `/sys` (required, but read-only)

Apply Pod Security Standards:
```bash
kubectl label namespace monitoring \
  pod-security.kubernetes.io/enforce=baseline \
  pod-security.kubernetes.io/audit=restricted \
  pod-security.kubernetes.io/warn=restricted
```

### 4. Image Security

Use verified container images:
```yaml
image:
  repository: ghcr.io/yuuki/rdma_exporter
  pullPolicy: IfNotPresent
  tag: "0.4.1"  # Pin specific version
```

## Security Audit Checklist

- [x] Runs as non-root user (UID 65534)
- [x] Read-only root filesystem
- [x] All capabilities dropped
- [x] No privilege escalation allowed
- [x] Seccomp profile enabled
- [x] Service account token not mounted
- [x] Host path mounts are read-only
- [x] Resource limits configured
- [x] Network access restricted to necessary ports
- [x] Health checks implemented
- [x] No secrets or credentials in configuration

## Threat Model

### Mitigated Threats

1. **Container Breakout**: Mitigated by dropped capabilities, seccomp, and no privilege escalation
2. **File System Tampering**: Prevented by read-only root filesystem
3. **Resource Exhaustion**: Limited by resource constraints
4. **Lateral Movement**: Restricted by no API access and minimal permissions
5. **Data Exfiltration**: No write access, no network egress required

### Remaining Risks

1. **Host Network Visibility**: Exporter has visibility into host network interfaces
   - **Mitigation**: Monitoring and alerting on exporter behavior
   - **Justification**: Required for RDMA functionality

2. **Sysfs Read Access**: Can read system information from `/sys`
   - **Mitigation**: Read-only mount, no sensitive data in RDMA sysfs paths
   - **Justification**: Core functionality requirement

## Compliance

This configuration aligns with:
- **CIS Kubernetes Benchmark**: Sections 5.2 (Pod Security Policies), 5.7 (Network Policies)
- **NIST SP 800-190**: Container security recommendations
- **Kubernetes Hardening Guide** (NSA/CISA): Principle of least privilege

## Reporting Security Issues

If you discover a security vulnerability, please email the maintainers directly. Do not create a public issue.

## References

- [Kubernetes Security Best Practices](https://kubernetes.io/docs/concepts/security/pod-security-standards/)
- [OWASP Kubernetes Security Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Kubernetes_Security_Cheat_Sheet.html)
- [CIS Kubernetes Benchmark](https://www.cisecurity.org/benchmark/kubernetes)
