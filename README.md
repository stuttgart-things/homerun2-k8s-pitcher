# homerun2-k8s-pitcher

Go microservice that watches a Kubernetes cluster via dynamic informers and periodic collectors, pitching real-time events and snapshots to Redis Streams or an HTTP pitcher endpoint.

[![Build & Test](https://github.com/stuttgart-things/homerun2-k8s-pitcher/actions/workflows/build-test.yaml/badge.svg)](https://github.com/stuttgart-things/homerun2-k8s-pitcher/actions/workflows/build-test.yaml)

## Architecture

```
K8s API → informers (real-time add/update/delete)  ─┐
        → collectors (periodic snapshots)            ├→ Redis Streams / HTTP pitcher
Flux    → webhook (notification controller events)  ─┤
Ingress/HTTPRoute hosts → endpoint probes (#109)     ─┘ (+ /metrics)
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

  # kind: Node, Pod or Event -- or any resource by group/version/resource.
  # mode: items (default, one message per object) or snapshot (one message
  # per run, the objects as its payload).
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
should only report what matters, `profiles/k8sgpt-results.yaml` for K8sGPT
findings.

### Informer options

| Field | Default | |
|---|---|---|
| `pitchInitialList` | `false` | Pitch the objects listed at start-up as `add`. Off, a restart does not re-pitch the inventory. |
| `updates` | `conditions` | `conditions`: a `metadata.generation` change or a flip of the `Ready` or `Synced` condition. `flips`: only the flips, no generation change (#93) -- a spec change is the cluster repo's git log, not an event. `generation`: only generation changes. `all`: every update, status-only ones included (the old behaviour). |
| `deleteSeverity` | `WARNING` (`info` for `Event`) | e.g. `info` for claims that are deleted on purpose |
| `system` | `spec.system` | The message system of this informer only (also on collectors, #106), e.g. `k8sgpt-<cluster>` for findings the central omni-pitcher routes to the archive while the other informers keep `k8s-<cluster>`. |
| `throttle` | `true` | `false` bypasses `spec.cooldown` and `spec.rateLimit` for this informer (also on collectors, #102): for archive-only data such as K8sGPT findings, where a dropped event is missing data rather than a quieter channel. Unthrottled events do not count toward the rate-limit window. |
| `includeObject` | `false` | Send the object along as the message `payload`: nested JSON in the Redis document, without `metadata.managedFields` and the `last-applied-configuration` annotation (#99). The message body stays the summary. Up to 256 KiB; a larger object is dropped from the message with a warning. Over HTTP the omni-pitcher needs homerun-library ≥ v4.6.0 to keep it. |
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

### K8sGPT findings

[K8sGPT](https://github.com/k8sgpt-ai/k8sgpt-operator) in operator mode with
`ai.enabled: false` writes one `Result` CR (`core.k8sgpt.ai/v1alpha1`) per
finding. An informer on `results` turns them into findings (#99):

| Event | Meaning | Title | Severity |
|---|---|---|---|
| `add` | new finding | `Pod default/web-0: 2 findings` | `warning` |
| `update` | finding changed | the same | `warning` |
| `delete` | no longer reported | `Pod default/web-0: resolved` | `success` (or `deleteSeverity`) |

The body lists the parent object and every error text. Use `updates:
generation`: the operator re-stamps unchanged Results in their status on every
scan, and only a changed finding bumps the generation. With `includeObject`,
the Result goes along as the payload, so an archive gets `spec.error[]` as
JSON. See `profiles/k8sgpt-results.yaml`.

**Not installed yet.** An informer on a resource the cluster does not serve
(no K8sGPT CRDs) logs once and checks again every 10 minutes; it starts when
the CRD appears. A collector skips such a run without an error (#106). So one
profile can carry the K8sGPT part on every cluster.

**Fingerprint.** Every Result event carries the tag `fp=<16 hex>`: a hash of
cluster, object and sorted error texts. The same problem on the same object
keeps its fingerprint across scans and restarts, so an archive can join
events, snapshots and clusters (#103).

**Snapshot.** Events alone miss what happened while the pitcher was down (a
deleted Result never becomes "resolved") and findings older than the pitcher.
A collector in `mode: snapshot` pitches all Results as **one** message per
interval -- the current state of the cluster -- titled
`K8sGPT snapshot <cluster>: N findings`, severity `info`, with the payload

```json
{"cluster": "labda", "resource": "results", "count": 2,
 "items": [{"fingerprint": "3f1c…", "object": {"kind": "Result", "spec": {…}}}]}
```

`0 findings` is a snapshot too: the cluster is clean. Above homerun-library's
payload limit the objects shrink to apiVersion, kind, name and spec
(`trimmed: true`), then items are cut (`truncated: true`, `count` stays the
total).

```yaml
apiVersion: core.k8sgpt.ai/v1alpha1
kind: K8sGPT
metadata:
  name: scan
  namespace: k8sgpt-operator-system
spec:
  ai:
    enabled: false
  analysis:
    interval: 5m
  noCache: true
```

### Inventory: versions and images

A collector of `kind: Inventory` (#108) pitches one snapshot per interval
with what runs on the cluster, as system `inventory-<cluster>` (or
`system`), payload `inventory/v1` — for the archive and the fleet analysis
(stuttgart-things/stuttgart-things#4556):

```yaml
collectors:
  - kind: Inventory
    interval: 24h
    throttle: false
```

| Field | Content |
|---|---|
| `version` | API server gitVersion |
| `nodes[]` | name, roles, kubelet, OS image, kernel, container runtime, ready |
| `images[]` | image reference + digest with pods, namespaces, workloads (`Deployment/ns/name`, CronJob for its Jobs); `latest: true` for `:latest` or untagged |
| `releases[]` | Flux HelmReleases, Argo CD Applications, Crossplane Configurations/Providers/Functions — each only if served and readable |

It reports and does not judge: outdated versions or `:latest` images are
for the analysis. Above 256 KiB the workload lists shrink to one entry
(`trimmed`), then images are cut (`truncated: n`). RBAC: list `nodes` and
`pods` cluster-wide, plus the release kinds (KCL `extraRules`). Example:
[`profiles/inventory.yaml`](profiles/inventory.yaml). On platform-sthings
the snapshot is ~74 KB (4 nodes, 106 images, 185 releases).

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
- Exception: an informer or collector with `throttle: false` bypasses both,
  and its events do not count toward the window (#102). Meant for data that
  goes only to an archive, such as K8sGPT findings.

## Endpoint Probes

`spec.probes` (#109) checks the cluster's endpoints: the hosts of every
`Ingress` (`spec.rules[].host`, https if the host is in `spec.tls`) and
`HTTPRoute` (`spec.hostnames`, https by default) plus explicit URLs. The path
is the first path of the rule (Ingress) or the first match (HTTPRoute); a
regex-like path becomes `/`. An `HTTPRoute` API that is not served is skipped
until it is. See `profiles/endpoint-probes.yaml`.

```yaml
spec:
  probes:
    enabled: true
    interval: 5m             # default 5m
    timeout: 5s              # default 5s
    snapshotInterval: 1h     # default 1h
    # system: endpoints-<cluster>   (default)
    # expectedStatus: [200]  # default: any 2xx/3xx; redirects are not followed
    failureThreshold: 2      # default 2 (>= 1): consecutive failed rounds before an Endpoint finding opens
    certWarnDays: 14         # default 14
    certErrorDays: 3         # default 3
    # resolver: 10.96.0.10:53  # DNS server for the probes, default system resolver
    metricsPort: "9090"      # default 9090
    # throttle: false        # state change events bypass cooldown/rateLimit
    discover:
      ingress: true
      httpRoute: true
      namespace: "*"         # "*" (default), "@self" or a namespace
      httpRouteScheme: https # or http
    targets:                 # explicit endpoints, e.g. outside the cluster
      - name: homepage       # default: the URL's host
        url: https://www.example.com/
        expectedStatus: [200]
        insecureSkipVerify: false  # true skips the trust check only
```

Annotations on an Ingress or HTTPRoute:

| Annotation | Effect |
|---|---|
| `homerun2.sthings.io/probe: "false"` | not probed |
| `homerun2.sthings.io/probe-scheme: http\|https` | overrides the scheme |
| `homerun2.sthings.io/probe-path: /healthz` | overrides the path |

**Checks per target**: HTTP GET (status, latency), DNS, connect and TLS
errors, and over TLS the leaf certificate: days to expiry (`warning` below
`certWarnDays`, `error` below `certErrorDays` or expired), SAN match and chain
trust (system roots; `insecureSkipVerify` skips only the trust check).

| Finding kind | check | severity |
|---|---|---|
| `Endpoint` | `dns`, `connect`, `tls` (handshake failed, chain not trusted), `status` | `error` |
| `Certificate` | `expiry` | `warning` / `error` |
| `Certificate` | `san` (host not in the certificate) | `error` |

**Messages** follow the `findings/v1` contract (system `endpoints-<cluster>`,
payload `{format, source: endpoints, target: <cluster>, count, items[,
truncated]}`, items `{fingerprint, kind, name, namespace, parent, severity,
texts}`). The fingerprint is the first 16 hex characters of
sha256(`endpoints|<cluster>|<kind>|<namespace>/<name>|<check>`), without the
texts, so it stays while the problem does. `name` is the host (the target's
name for explicit targets), `parent` the `Ingress/<ns>/<name>` or
`HTTPRoute/<ns>/<name>`.

- **State change** (no `snapshot` tag, tag `fp=<fingerprint>`): a finding
  opened (`opened`), changed its severity (`changed`, e.g. a certificate
  entering the error window) or resolved (`resolved`, severity `success`,
  empty `items`). Throttled by `spec.cooldown`/`spec.rateLimit` unless
  `throttle: false`. An `Endpoint` finding opens only after
  `failureThreshold` consecutive failed rounds (default 2) and resolves on
  the first success; `Certificate` findings open at once. The first round
  after start sends no events: it seeds the state (everything found counts
  as open, without threshold) and the first snapshot carries it, so a
  restart does not re-send `opened` for known problems; their later recovery
  is still sent as `resolved`. A target that disappears (object deleted or
  opted out) drops its findings without a resolve. A failed pitch is retried
  next round.
- **Snapshot** (tag `snapshot`): all open findings after the first round and
  then every `snapshotInterval`, severity = worst finding or `success`; never
  throttled. Above 256 KiB the worst findings are kept and `truncated` holds
  the number omitted (`count` stays the total).

**Metrics** on `:<metricsPort>/metrics` (own server, separate from the
webhook port; also `/healthz`), labels `host`, `target` (URL), `kind`
(`Ingress`, `HTTPRoute`, `Target`), `namespace`, `name`:
`probe_success`, `probe_duration_seconds`, `probe_http_status_code`,
`probe_cert_expiry_timestamp_seconds` (TLS targets only), plus Go and
process metrics. RBAC needs `list` on `ingresses` and `httproutes`; the KCL
`config.probesEnabled` adds it with the port, a Service and optionally a
ServiceMonitor.

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
