# homerun2-k8s-pitcher

Go microservice that watches a Kubernetes cluster via dynamic informers and periodic collectors, pitching real-time events and snapshots to Redis Streams.

## Quick Start

```bash
# CLI mode with kubeconfig
homerun2-k8s-pitcher --profile profiles/dev.yaml --kubeconfig ~/.kube/config

# Dev mode (file pitcher, no Redis)
PITCHER_MODE=file homerun2-k8s-pitcher --profile profiles/dev.yaml --kubeconfig ~/.kube/config
```

At startup the pitcher waits for its target: omni-pitcher's `/ready` in HTTP mode (`PITCHER_STARTUP_TIMEOUT`), a Redis `PING` in Redis mode (`REDIS_STARTUP_TIMEOUT`). Both default to `120s`; SIGINT/SIGTERM during the wait exits 0.

## Architecture

```
K8s API → informers (real-time add/update/delete)  → Redis Streams
        → collectors (periodic snapshots)           → Redis Streams
```

## Configuration

All configuration is done via a YAML profile. See `profiles/` for examples.
