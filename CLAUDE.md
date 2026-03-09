# CLAUDE.md

## Project

homerun2-k8s-pitcher — Go microservice that watches a Kubernetes cluster via informers and collectors, pitching real-time events and periodic snapshots to Redis Streams.

## Tech Stack

- **Language**: Go 1.24+
- **K8s client**: `k8s.io/client-go` (dynamic informers)
- **Queue**: Redis Streams via `homerun-library`
- **Config**: YAML profile (`K8sPitcherProfile`)
- **Build**: ko (`.ko.yaml`), no Dockerfile
- **CI**: Dagger modules (`dagger/main.go`), Taskfile
- **Deploy**: KCL manifests (`kcl/`), GitHub Actions, semantic-release

## Architecture

```
K8s API → informers (real-time add/update/delete)  → Redis Streams
        → collectors (periodic snapshots)           → Redis Streams
```

## Key Paths

- `main.go` — entrypoint: flag parsing, profile loading, orchestration
- `internal/profile/` — K8sPitcherProfile types and YAML loader
- `internal/kube/` — K8s client init (kubeconfig + in-cluster), secret resolution
- `internal/collector/` — periodic snapshot gatherers (Node, Pod, Event)
- `internal/informer/` — dynamic informer manager (any GVR including CRDs)
- `internal/pitcher/` — K8sEvent → Redis Streams via homerun-library
- `internal/config/` — logging setup
- `profiles/` — example YAML profiles (dev, production)

## CLI Usage

```bash
# CLI mode with kubeconfig
homerun2-k8s-pitcher --profile profiles/dev.yaml --kubeconfig ~/.kube/config

# In-cluster (profile mounted as ConfigMap)
homerun2-k8s-pitcher --profile /etc/k8s-pitcher/profile.yaml
```

## Git Workflow

**Branch-per-issue with PR and merge.**

### Branch naming

- `fix/<issue-number>-<short-description>` for bugs
- `feat/<issue-number>-<short-description>` for features
- `test/<issue-number>-<short-description>` for test-only changes

### Commit messages

- Use conventional commits: `fix:`, `feat:`, `test:`, `chore:`, `docs:`
- End with `Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>` when Claude authored
- Include `Closes #<issue-number>` to auto-close issues

## Code Conventions

- No Dockerfile — use ko for image builds
- Config via YAML profile, secrets resolved from K8s or inline
- Tests: `go test ./...` — unit tests must not require Redis or K8s cluster

## Testing

```bash
go test ./...
task lint
task build-test-binary
task build-scan-image-ko
```
