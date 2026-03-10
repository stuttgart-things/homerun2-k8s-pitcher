# homerun2-k8s-pitcher / KCL Deployment

KCL-based Kubernetes manifests for homerun2-k8s-pitcher. Renders Namespace, ServiceAccount, ClusterRole/ClusterRoleBinding, ConfigMap (profile), Secrets, and Deployment.

## Render Manifests

### Via Dagger (recommended)

```bash
# render with a profile file
dagger call -m github.com/stuttgart-things/dagger/kcl@v0.82.0 run \
  --source kcl \
  --parameters-file tests/kcl-deploy-profile.yaml \
  export --path /tmp/rendered-k8s-pitcher.yaml

# render with inline parameters
dagger call -m github.com/stuttgart-things/dagger/kcl@v0.82.0 run \
  --source kcl \
  --parameters 'config.image=ghcr.io/stuttgart-things/homerun2-k8s-pitcher:v0.1.1,config.namespace=homerun2-flux,config.authToken=changeme' \
  export --path /tmp/rendered-k8s-pitcher.yaml
```

### Via kcl CLI

```bash
kcl run kcl/main.k \
  -D 'config.image=ghcr.io/stuttgart-things/homerun2-k8s-pitcher:v0.1.1' \
  -D 'config.namespace=homerun2-flux' \
  -D 'config.authToken=changeme'
```

## Deploy to Cluster

```bash
# render + apply
dagger call -m github.com/stuttgart-things/dagger/kcl@v0.82.0 run \
  --source kcl \
  --parameters-file tests/kcl-deploy-profile.yaml \
  export --path /tmp/rendered-k8s-pitcher.yaml

kubectl apply -f /tmp/rendered-k8s-pitcher.yaml
```

> **Note:** KCL serializes multiline `config.profileYaml` as a single-line string with escaped `\n`.
> After rendering, verify the ConfigMap `profile.yaml` data uses proper YAML block scalars.
> If needed, apply the ConfigMap separately with the correct multiline content.

## Profile Parameters

| Parameter | Default | Description |
|---|---|---|
| `config.image` | `ghcr.io/stuttgart-things/homerun2-k8s-pitcher:v0.1.1` | Container image |
| `config.namespace` | `homerun2` | Target namespace |
| `config.replicas` | `1` | Replica count |
| `config.profileYaml` | *(empty)* | K8sPitcherProfile YAML (mounted as ConfigMap) |
| `config.pitcherAddr` | *(empty)* | HTTP pitcher endpoint URL |
| `config.pitcherInsecure` | `true` | Skip TLS verification for HTTP pitcher |
| `config.authToken` | *(empty)* | Bearer auth token (creates Secret if set) |
| `config.redisPassword` | *(empty)* | Redis password (creates Secret if set) |
| `config.watchedResources` | `[pods, nodes, events, ...]` | K8s resources for RBAC ClusterRole |
| `config.watchedApiGroups` | `["", "apps"]` | API groups for RBAC ClusterRole |
| `config.trustBundleConfigMap` | *(empty)* | ConfigMap name with `trust-bundle.pem` for custom CA (adds volume mount + `SSL_CERT_DIR`) |
| `config.extraRbacRules` | `[]` | Extra RBAC rules for CRDs (list of `{apiGroups, resources, verbs}`) |
| `config.extraEnvVars` | `{}` | Additional environment variables |
| `config.cpuRequest` | `100m` | CPU request |
| `config.cpuLimit` | `500m` | CPU limit |
| `config.memoryRequest` | `256Mi` | Memory request |
| `config.memoryLimit` | `1Gi` | Memory limit |

## Example Profiles

### movie-scripts cluster (HTTP pitcher mode)

```yaml
---
config.image: ghcr.io/stuttgart-things/homerun2-k8s-pitcher:v0.1.1
config.namespace: homerun2-flux
config.authToken: <your-token>
config.trustBundleConfigMap: cluster-trust-bundle
config.watchedResources:
  - pods
  - nodes
  - events
  - deployments
  - replicasets
config.watchedApiGroups:
  - ""
  - apps
config.profileYaml: |
  apiVersion: homerun2.sthings.io/v1alpha1
  kind: K8sPitcherProfile
  metadata:
    name: movie-scripts
  spec:
    pitcher:
      addr: https://pitcher.movie-scripts2.sthings-vsphere.labul.sva.de/pitch
      insecure: true
    auth:
      tokenFrom:
        secretKeyRef:
          name: homerun2-k8s-pitcher-token
          namespace: homerun2-flux
          key: auth-token
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
        namespace: homerun2-flux
        events: [add, update, delete]
```

### Direct Redis mode

```yaml
---
config.image: ghcr.io/stuttgart-things/homerun2-k8s-pitcher:v0.1.1
config.namespace: homerun2
config.redisPassword: <your-password>
config.authToken: <your-token>
config.profileYaml: |
  apiVersion: homerun2.sthings.io/v1alpha1
  kind: K8sPitcherProfile
  metadata:
    name: dev-cluster
  spec:
    redis:
      addr: redis-stack.homerun2.svc.cluster.local
      port: "6379"
      stream: k8s-events
      passwordFrom:
        secretKeyRef:
          name: homerun2-k8s-pitcher-redis
          namespace: homerun2
          key: password
    collectors:
      - kind: Node
        interval: 60s
      - kind: Pod
        namespace: "*"
        interval: 30s
```

### Watching Custom Resources (CRDs)

To watch CRDs, add the CRD's API group/resource to `extraRbacRules` in the deploy profile, and add a matching informer entry in `profileYaml`:

```yaml
# Deploy profile — add RBAC for your CRD
config.extraRbacRules:
  - apiGroups: ["stable.example.com"]
    resources: ["crontabs"]

# Profile YAML — add informer for the CRD
config.profileYaml: |
  ...
  spec:
    informers:
      - group: stable.example.com
        version: v1
        resource: crontabs
        namespace: "*"
        events: [add, update, delete]
```
