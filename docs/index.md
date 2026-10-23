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
Node `Ready` flips, lasting Flux failures and `Warning` Events with reasons such as
`BackOff`, `OOMKilling` or `FailedMount`. Event informers take `types`,
`reasons` and `excludeReasons`; `spec.cooldown` drops repeats of the same
problem and `spec.rateLimit: {max, per}` caps all messages, replacing the
excess with one `k8s-pitcher: N events suppressed` warning per window.

Condition informers take `ignoreStatuses`, `ignoreReasons` and `minDuration`
(#89): flips to an ignored state (Flux `Progressing`, `DependencyNotReady`)
are not pitched, and a failure is pitched only once it has lasted
`minDuration`, its recovery only if the failure was pitched. The preset uses
them for Kustomizations and HelmReleases (`[Unknown]`, `[Progressing,
DependencyNotReady, ReconciliationInProgress]`, `5m`) with `updates: flips`
(#93), which pitches flips only and no spec (generation) changes. Images before the
release with #89 (v1.3.x and older) ignore these fields and pitch every flip.
