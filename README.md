# homerun2-k8s-pitcher

Microservice that watches a Kubernetes cluster and pitches gathered information and real-time events to Redis Streams

[![Build & Test](https://github.com/stuttgart-things/homerun2-k8s-pitcher/actions/workflows/build-test.yaml/badge.svg)](https://github.com/stuttgart-things/homerun2-k8s-pitcher/actions/workflows/build-test.yaml)

## API Endpoints

| Endpoint | Method | Auth | Description |
|----------|--------|------|-------------|
| `/health` | `GET` | None | Health check (returns version, commit, date) |
| `/pitch` | `POST` | Bearer token | Submit a message to Redis Streams |

<details>
<summary><b>Pitch a message</b></summary>

```bash
curl -X POST http://localhost:8080/pitch \
  -H "Authorization: Bearer <YOUR_AUTH_TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{
    "title": "Test Notification",
    "message": "Hello from homerun2-k8s-pitcher",
    "severity": "info",
    "author": "test"
  }'
```

</details>

## Deployment

<details>
<summary><b>Container image (ko / ghcr.io)</b></summary>

```bash
docker pull ghcr.io/stuttgart-things/homerun2-k8s-pitcher:<tag>

docker run \
  -e REDIS_ADDR=redis -e REDIS_PORT=6379 \
  -e REDIS_STREAM=homerun \
  -e AUTH_TOKEN=mysecret \
  -p 8080:8080 \
  ghcr.io/stuttgart-things/homerun2-k8s-pitcher:<tag>
```

</details>

## Development

<details>
<summary><b>Configuration reference</b></summary>

| Variable | Description | Default |
|----------|-------------|---------|
| `REDIS_ADDR` | Redis server address | `localhost` |
| `REDIS_PORT` | Redis server port | `6379` |
| `REDIS_PASSWORD` | Redis password | (empty) |
| `REDIS_STREAM` | Redis stream name | `homerun` |
| `PORT` | HTTP server port | `8080` |
| `AUTH_TOKEN` | Bearer token for auth | (required) |
| `PITCHER_MODE` | Backend: `redis` or `file` | `redis` |
| `LOG_FORMAT` | `json` or `text` | `json` |
| `LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `info` |

</details>

## Testing

```bash
# Unit tests (no Redis needed)
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
