"""
Schema definition for homerun2-k8s-pitcher deployment configuration
"""

schema K8sPitcher:
    """Configuration for homerun2-k8s-pitcher Kubernetes deployment"""

    # Metadata
    name: str = "homerun2-k8s-pitcher"
    namespace: str = "homerun2"

    # Image
    image: str = "ghcr.io/stuttgart-things/homerun2-k8s-pitcher:v0.1.1"
    imagePullPolicy: str = "IfNotPresent"

    # Replicas and resources
    replicas: int = 1
    cpuRequest: str = "100m"
    cpuLimit: str = "500m"
    memoryRequest: str = "256Mi"
    memoryLimit: str = "1Gi"

    # Profile (embedded YAML mounted as ConfigMap)
    profileYaml: str = ""

    # Pitcher mode: HTTP (post to omni-pitcher) or Redis (direct)
    pitcherAddr: str = ""
    pitcherInsecure: bool = True

    # Redis (for direct Redis pitcher mode)
    redisPassword: str = ""

    # Auth token (used by HTTP pitcher for Bearer auth)
    authToken: str = ""

    # RBAC
    watchedResources: [str] = ["pods", "nodes", "events", "deployments", "replicasets", "configmaps"]
    watchedApiGroups: [str] = ["", "apps"]

    # Extra environment variables
    extraEnvVars: {str:str} = {}

    # Labels and annotations
    labels: {str:str} = {}
    annotations: {str:str} = {}

    check:
        replicas >= 0, "replicas must be >= 0"
        name != "", "name must not be empty"
        namespace != "", "namespace must not be empty"
