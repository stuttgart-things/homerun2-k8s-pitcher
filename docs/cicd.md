# CI/CD

## GitHub Actions Workflows

| Workflow | Trigger | Description |
|----------|---------|-------------|
| `build-test` | Push/PR | Lint + unit tests via Dagger |
| `lint-repo` | Push/PR | Repository-wide linting |
| `build-scan-image` | Push to main / PR | Build container image with ko, push to ghcr.io, scan with Trivy. PRs publish `pr-<num>-<sha>` tags |
| `release` | After image build | semantic-release: changelog, GitHub release, kustomize OCI push |
| `push-kustomize-pr` | PR | Render the namespace-scoped preview profile and push a per-PR kustomize OCI artifact |
| `comment-preview-url` | PR opened/reopened | Bot comments the per-PR preview environment URL |
| `cleanup-pr-artifacts` | PR closed | Delete the PR's `pr-<num>-*` image and kustomize OCI package versions |

## PR Preview Environments

Opening a PR with the `preview` label spins up an ephemeral namespace
(`homerun2-k8s-pitcher-pr-<num>`) running k8s-pitcher (namespace-scoped) plus
co-tenanted omni-pitcher, redis-stack and a core-catcher dashboard. The
`comment-preview-url` bot posts the dashboard URL; closing the PR tears the
environment down. Wiring lives in `stuttgart-things/argocd`
(`platforms/homerun2-pr-preview`).

## Release Process

Releases are fully automated via [semantic-release](https://semantic-release.gitbook.io/):

- `fix:` commits trigger a **patch** bump
- `feat:` commits trigger a **minor** bump
- Each release publishes the container image and kustomize OCI artifact to `ghcr.io`

## Taskfile Commands

```bash
task lint                  # Run Go linter
task build-test-binary     # Build + test with Redis via Dagger
task build-scan-image-ko   # Build, push, scan container image
task build-output-binary   # Build Go binary
```
