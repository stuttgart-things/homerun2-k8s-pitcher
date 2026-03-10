# homerun2-k8s-pitcher

Go microservice that watches a Kubernetes cluster via dynamic informers and periodic collectors, pitching real-time events and snapshots to Redis Streams or an HTTP pitcher endpoint.

[![Build & Test](https://github.com/stuttgart-things/homerun2-k8s-pitcher/actions/workflows/build-test.yaml/badge.svg)](https://github.com/stuttgart-things/homerun2-k8s-pitcher/actions/workflows/build-test.yaml)

## Architecture

```
K8s API → informers (real-time add/update/delete)  ─┐
        → collectors (periodic snapshots)            ├→ Redis Streams / HTTP pitcher
Flux    → webhook (notification controller events)  ─┘
```

### Pitcher Modes

| Mode | Description | Config |
|------|-------------|--------|
| **HTTP** | POST events to omni-pitcher endpoint | `spec.pitcher.addr` |
| **Redis** | Enqueue events directly to Redis Streams | `spec.redis.*` |
| **File** | Write JSON lines to a file (dev/testing) | `PITCHER_MODE=file` |

## Usage

```bash
# CLI mode with kubeconfig
homerun2-k8s-pitcher --profile profiles/dev.yaml --kubeconfig ~/.kube/config

# In-cluster (profile mounted as ConfigMap)
homerun2-k8s-pitcher --profile /etc/k8s-pitcher/profile.yaml
```

## Configuration Profile

A single YAML profile defines what to watch/collect and how to pitch events:

```yaml
apiVersion: homerun2.sthings.io/v1alpha1
kind: K8sPitcherProfile
metadata:
  name: my-cluster
spec:
  # HTTP pitcher mode (send to omni-pitcher)
  pitcher:
    addr: https://pitcher.example.com/pitch
    insecure: false
  auth:
    token: changeme
    tokenFrom:
      secretKeyRef:
        name: pitcher-token
        namespace: homerun2
        key: auth-token

  # OR Redis pitcher mode (direct to Redis Streams)
  # redis:
  #   addr: redis-stack.homerun2.svc.cluster.local
  #   port: "6379"
  #   stream: k8s-events
  #   password: changeme
  #   passwordFrom:
  #     secretKeyRef:
  #       name: redis-credentials
  #       namespace: homerun2
  #       key: password

  # Flux notification webhook
  webhook:
    enabled: true
    port: "8080"
    hmacKey: ""  # optional HMAC key for signature verification

  collectors:
    - kind: Node
      interval: 60s
    - kind: Pod
      namespace: "*"
      interval: 30s
    - kind: Event
      namespace: "*"
      interval: 15s
  informers:
    - group: ""
      version: v1
      resource: pods
      namespace: "*"
      events: [add, update, delete]
    - group: apps
      version: v1
      resource: deployments
      namespace: homerun2
      events: [add, update, delete]
```

See `profiles/` for complete examples.

## Flux Notification Webhook

k8s-pitcher can receive events from the [Flux notification controller](https://fluxcd.io/flux/monitoring/alerts/) via a built-in webhook endpoint.

### Setup

1. Enable the webhook in your profile:
   ```yaml
   spec:
     webhook:
       enabled: true
       port: "8080"
       hmacKey: "optional-secret"  # for generic-hmac provider type
   ```

2. Create a Flux `Provider` pointing to k8s-pitcher:
   ```yaml
   apiVersion: notification.toolkit.fluxcd.io/v1beta3
   kind: Provider
   metadata:
     name: homerun2-k8s-pitcher
     namespace: flux-system
   spec:
     type: generic       # or generic-hmac if hmacKey is set
     address: http://homerun2-k8s-pitcher.homerun2-flux.svc.cluster.local:8080/flux
   ```

3. Create a Flux `Alert` to select which events to forward:
   ```yaml
   apiVersion: notification.toolkit.fluxcd.io/v1beta3
   kind: Alert
   metadata:
     name: homerun2-k8s-pitcher
     namespace: flux-system
   spec:
     providerRef:
       name: homerun2-k8s-pitcher
     eventSources:
       - kind: Kustomization
         name: "*"
       - kind: GitRepository
         name: "*"
       - kind: HelmRelease
         name: "*"
   ```

### Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/flux` | POST | Receives Flux notification events |
| `/healthz` | GET | Health check (returns `{"status":"ok"}`) |

## Deployment

```bash
docker pull ghcr.io/stuttgart-things/homerun2-k8s-pitcher:<tag>
```

See [`kcl/README.md`](kcl/README.md) for KCL-based Kubernetes deployment.

## Development

```bash
# Unit tests (no K8s/Redis needed)
go test ./...

# Integration tests (Dagger + Redis)
task build-test-binary

# Lint
task lint

# Build + scan image
task build-scan-image-ko
```

## Links

- [Releases](https://github.com/stuttgart-things/homerun2-k8s-pitcher/releases)
- [homerun-library](https://github.com/stuttgart-things/homerun-library)
