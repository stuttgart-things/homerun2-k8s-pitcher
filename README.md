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

### Startup

Before it starts watching, the pitcher waits for its target with bounded backoff, so a Redis or omni-pitcher that is still starting does not crashloop the pod:

| Mode | Waits for | Budget (Go duration) |
|------|-----------|----------------------|
| **HTTP** | `GET /ready` on omni-pitcher, derived from `spec.pitcher.addr` (falls back to `/health` on versions without `/ready`). No message is pitched. | `PITCHER_STARTUP_TIMEOUT`, default `120s` |
| **Redis** | a Redis `PING` | `REDIS_STARTUP_TIMEOUT`, default `120s` |

An invalid or non-positive value fails startup. SIGINT/SIGTERM during the wait exits 0; a target that never answers within the budget exits 1.

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
  cluster: my-cluster   # default: kubeconfig context, in-cluster "in-cluster"
  system: k8s-my-cluster  # default: k8s-<cluster>; Flux webhook events keep "flux"

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

See `profiles/` for complete examples, `profiles/crossplane-inventory.yaml` for
VM and cluster claims, `profiles/important-events.yaml` for a cluster that
should only report what matters.

### Informer options

| Field | Default | |
|---|---|---|
| `pitchInitialList` | `false` | Pitch the objects listed at start-up as `add`. Off, a restart does not re-pitch the inventory. |
| `updates` | `conditions` | `conditions`: a `metadata.generation` change or a flip of the `Ready` or `Synced` condition. `flips`: only the flips, no generation change (#93) -- a spec change is the cluster repo's git log, not an event. `generation`: only generation changes. `all`: every update, status-only ones included (the old behaviour). |
| `deleteSeverity` | `WARNING` (`info` for `Event`) | e.g. `info` for claims that are deleted on purpose |
| `includeObject` | `false` | Append the whole object as JSON to the summary. Off, the message body is the summary only. |
| `types` | *(all)* | `resource: events` only. Kubernetes Event types to pitch: `Normal`, `Warning`. |
| `reasons` | *(all)* | `resource: events` only. Allowlist of Event reasons, e.g. `[BackOff, OOMKilling]`. |
| `excludeReasons` | *(none)* | `resource: events` only. Denylist of Event reasons; mutually exclusive with `reasons`. |
| `ignoreStatuses` | *(none)* | `updates: conditions` or `flips` only. Flips to these statuses (`Unknown`, `False`) are not pitched. See [Lasting failures only](#lasting-failures-only). |
| `ignoreReasons` | *(none)* | `updates: conditions` or `flips` only. Flips to a condition with one of these reasons are not pitched. |
| `minDuration` | `0` (off) | `updates: conditions` or `flips` only. A failure is pitched once it has lasted this long, e.g. `5m`. |

A flip is pitched as `VsphereVM/vm1 Ready=False (ReconcileError)`: `Ready` to
`False` is `error`, `Synced` to `False` or either to `Unknown` `warning`, back
to `True` `success`; a generation change is `info` (not with `updates: flips`). A condition that shows up
for the first time is not a flip. Messages carry `system: k8s-<cluster>`, so
scout and the AI pitcher can tell clusters apart.

Kubernetes `Event` objects (from an informer or a collector snapshot) take
their severity from their own `type`: `Warning` is `warning`, `Normal`
`info`. An Event says that something happened, not that it went well. Their
deletion is TTL expiry and pitched as `info` unless `deleteSeverity` is set
(#85). Every other kind keeps `add`/`update` → `SUCCESS`, `delete` →
`WARNING`, a snapshot `INFO`. An added or updated Event is titled after its
reason and involved object, `BackOff: Pod shop/api-7d9` (`NodeNotReady: Node
worker-1` when cluster-scoped), and its reason is appended to the tags:
`k8s,Event,add,shop,BackOff` (#87).

### Lasting failures only

Flux flips every Kustomization to `Ready=Unknown (Progressing)`, often
`Ready=False (DependencyNotReady)`, and back to `Ready=True` within seconds
on each new revision. Three optional informer fields keep that noise out and
report real failures (#89):

```yaml
informers:
  - group: kustomize.toolkit.fluxcd.io
    version: v1
    resource: kustomizations
    events: [update]
    updates: flips
    ignoreStatuses: [Unknown]
    ignoreReasons: [Progressing, DependencyNotReady, ReconciliationInProgress]
    minDuration: 5m
```

- A flip **to** an ignored status or reason is not pitched. The next flip is
  compared against the last **reported** state, so `True → Unknown
  (Progressing) → True` sends nothing, and a failure keeps running through
  ignored states in between (a Flux retry does not restart it).
- A failure (a condition that is not `True`) is pitched only once it has
  lasted `minDuration` and is still failing. The title stays
  `Kustomization/app Ready=False (BuildFailed)`; the message starts with
  `Failing for 5m0s (since <time>)`. A failure that recovers earlier sends
  nothing.
- A recovery (`→ True`) is pitched only if its failure was pitched.
- A failure ends only with a flip back to `True`; ignored states in between
  (a retry going `Progressing`) do not end it. The delayed message is titled
  with the last failure state that is not ignored.
- With `minDuration`, a failure that is already there when the pitcher
  starts counts from the first update it sees; a healthy object seen first
  is never a recovery. Without `minDuration` the first update is compared
  with the old object, as for plain flips.
- Pending failures are kept in memory, per object, like the cooldown: a
  restart or the deletion of the object forgets them.
- Delayed failures go through cooldown and rate limit like any message.

| Case (`minDuration: 5m`) | Pitched |
|---|---|
| Burst: `True → Unknown (Progressing) → False (DependencyNotReady) → True` in seconds | nothing |
| Short failure: `False (BuildFailed)` for 4m, then `True` | nothing |
| Lasting failure: `False (BuildFailed)` for 5m (retries through `Progressing` included) | one `error` after 5m, with how long it has been failing |
| Recovery of that failure | one `success` |
| Object deleted while its failure is pending | nothing |

All three fields are optional; without them every flip is pitched as before.
They are only allowed with `updates: conditions` or `flips` and `update` in
`events`. `updates: flips` needs the release with #93; older images reject
the profile at start-up (`updates must be conditions, generation or all`).
Images before the release with #89 (v1.3.x and older) ignore these fields:
the profile is loaded with a plain `yaml.Unmarshal`, so unknown fields are
dropped without an error, and every flip is pitched.

### Cooldown and rate limit

Two `spec` fields keep a cluster from flooding the target (#87). Both are off
when unset and apply to every source (informers, collectors, webhook):

```yaml
spec:
  cooldown: 1h        # same Event (involved object + reason) or condition flip at most once per hour
  rateLimit:
    max: 20           # at most 20 messages ...
    per: 10m          # ... per 10 minutes
```

- `cooldown`: a repeat of the same problem within the window is dropped and
  counted. Keys are the involved object and reason of a Kubernetes Event, or
  the object and the flip (`Ready=False (...)`) of a condition update. The
  first repeat after the window is pitched again, with `Repeats: <n> since
  <time>` appended to the message. Adds, deletes and snapshots of other kinds
  are never deduplicated.
- `rateLimit`: messages above `max` per `per` are dropped. Once the window is
  over, a single `k8s-pitcher: <n> events suppressed` (`warning`) is pitched
  instead.

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

   The webhook can be the profile's only source: collectors and informers
   are optional when `webhook.enabled` is true.

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
