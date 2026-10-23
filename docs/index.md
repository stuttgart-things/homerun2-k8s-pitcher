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

All configuration is done via a YAML profile. See `profiles/` for examples and the
[README](https://github.com/stuttgart-things/homerun2-k8s-pitcher#configuration-profile) for all fields.

`profiles/important-events.yaml` reports only what matters for a cluster:
Node and Flux `Ready` flips and `Warning` Events with reasons such as
`BackOff`, `OOMKilling` or `FailedMount`. Event informers take `types`,
`reasons` and `excludeReasons`; `spec.cooldown` drops repeats of the same
problem and `spec.rateLimit: {max, per}` caps all messages, replacing the
excess with one `k8s-pitcher: N events suppressed` warning per window.
