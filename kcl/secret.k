"""
Secrets for homerun2-k8s-pitcher
"""

import base64
import labels

config = labels.config

# Secret - Redis password (for direct Redis pitcher mode)
secretRedis = {
    apiVersion: "v1"
    kind: "Secret"
    metadata: {
        name: "{}-redis".format(config.name)
        namespace: config.namespace
        labels: labels.commonLabels
    }
    type: "Opaque"
    data: {
        password: base64.encode(config.redisPassword)
    }
} if config.redisPassword else None

# Secret - Auth token (for HTTP pitcher Bearer auth)
secretToken = {
    apiVersion: "v1"
    kind: "Secret"
    metadata: {
        name: "{}-token".format(config.name)
        namespace: config.namespace
        labels: labels.commonLabels
    }
    type: "Opaque"
    data: {
        "auth-token": base64.encode(config.authToken)
    }
} if config.authToken else None
