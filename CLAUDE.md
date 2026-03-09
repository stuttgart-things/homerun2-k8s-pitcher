# CLAUDE.md

## Project

homerun2-homerun2-k8s-pitcher — Microservice that watches a Kubernetes cluster and pitches gathered information and real-time events to Redis Streams

## Tech Stack

- **Language**: Go 1.24+
- **HTTP**: stdlib `net/http` (no framework)
- **Queue**: Redis Streams via `homerun-library`
- **Build**: ko (`.ko.yaml`), no Dockerfile
- **CI**: Dagger modules (`dagger/main.go`), Taskfile
- **Infra**: GitHub Actions, semantic-release, renovate

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
- Config via environment variables, loaded once at startup
- Tests: `go test ./...` — unit tests must not require Redis

## Testing

```bash
go test ./...
task build-test-binary
task lint
task build-scan-image-ko
```
