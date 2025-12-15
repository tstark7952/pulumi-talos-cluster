# pulumi-talos-cluster

Production-grade Talos Linux Kubernetes cluster for local development with full security hardening.

## What is Talos?

[Talos Linux](https://www.talos.dev/) is a secure, immutable, minimal OS designed for Kubernetes:

- **Immutable**: No shell, no SSH, no package manager - managed entirely via API
- **Secure**: Encryption at rest, secure boot, minimal attack surface
- **Automated**: Declarative configuration, self-healing

## Security Features

| Feature | Status |
|---------|--------|
| Immutable OS | Built-in |
| No SSH/Shell access | Built-in |
| API-only management | Built-in |
| Encryption at rest (etcd) | Built-in |
| Secure boot capable | Built-in |
| Pod Security Standards | Applied |
| Default-deny NetworkPolicies | Applied |

## Prerequisites

- Docker running (via Lima or Docker Desktop)
- talosctl: `brew install siderolabs/tap/talosctl`
- Pulumi: `brew install pulumi`

## Quick Start

```bash
# Start the cluster
talosup

# Check cluster status
kubectl get nodes
talosctl dashboard

# Stop the cluster
talosdown
```

## Manual Usage

```bash
# Create cluster
cd /Users/justin/Desktop/Programming/talos
pulumi up --yes

# Destroy cluster
pulumi destroy --yes
```

## Configuration

After cluster creation:
- Kubeconfig: `~/.kube/talos-config`
- Talosconfig: `~/.talos/config`

```bash
export KUBECONFIG=~/.kube/talos-config
kubectl get nodes
```

## Talos Dashboard

View cluster health and node status:

```bash
talosctl dashboard
```

## Architecture

```
┌─────────────────────────────────────────┐
│           Docker (Lima VM)              │
│  ┌─────────────┐  ┌─────────────┐      │
│  │ Control     │  │ Worker 1    │      │
│  │ Plane       │  │             │      │
│  │ (Talos)     │  │ (Talos)     │      │
│  └─────────────┘  └─────────────┘      │
│                   ┌─────────────┐      │
│                   │ Worker 2    │      │
│                   │             │      │
│                   │ (Talos)     │      │
│                   └─────────────┘      │
└─────────────────────────────────────────┘
```

## Comparison with Kind

| Feature | Kind | Talos |
|---------|------|-------|
| OS | Container (Ubuntu) | Immutable (Talos) |
| SSH Access | Yes (docker exec) | No |
| Attack Surface | Larger | Minimal |
| Audit Logging | Manual config | Built-in |
| Encryption at Rest | Manual config | Built-in |
| Production Parity | Low | High |

## License

MIT
