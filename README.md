# homerun2-k8s-pitcher

Go microservice that watches a Kubernetes cluster via dynamic informers and periodic collectors, pitching real-time events and snapshots to Redis Streams.

[![Build & Test](https://github.com/stuttgart-things/homerun2-k8s-pitcher/actions/workflows/build-test.yaml/badge.svg)](https://github.com/stuttgart-things/homerun2-k8s-pitcher/actions/workflows/build-test.yaml)

## Architecture

```
K8s API → informers (real-time add/update/delete)  → Redis Streams
        → collectors (periodic snapshots)           → Redis Streams
```

## Usage

```bash
# CLI mode with kubeconfig
homerun2-k8s-pitcher --profile profiles/dev.yaml --kubeconfig ~/.kube/config

# In-cluster (profile mounted as ConfigMap)
homerun2-k8s-pitcher --profile /etc/k8s-pitcher/profile.yaml
```

## Configuration Profile

A single YAML profile defines what to watch/collect:

```yaml
apiVersion: homerun2.sthings.io/v1alpha1
kind: K8sPitcherProfile
metadata:
  name: my-cluster
spec:
  redis:
    addr: redis-stack.homerun2.svc.cluster.local
    port: "6379"
    stream: k8s-events
    password: changeme          # inline for CLI/dev
    passwordFrom:               # from K8s secret for in-cluster
      secretKeyRef:
        name: redis-credentials
        namespace: homerun2
        key: password
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

## Deployment

```bash
docker pull ghcr.io/stuttgart-things/homerun2-k8s-pitcher:<tag>
```

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
