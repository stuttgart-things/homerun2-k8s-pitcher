"""
Main entry point for homerun2-k8s-pitcher Kubernetes deployment
Imports all resources and exports manifests
"""

import serviceaccount
import clusterrole
import configmap
import secret
import deploy

# Export all manifests (filter out None values).
# Namespace is NOT emitted from the KCL profile — the consuming Application
# uses `CreateNamespace=true`, and PR-preview namespaces are pre-created by
# Kyverno. See stuttgart-things/homerun2-omni-pitcher#132.
manifests = [
    m for m in [
        serviceaccount.serviceAccount
        clusterrole.clusterRole
        clusterrole.clusterRoleBinding
        clusterrole.role
        clusterrole.roleBinding
        configmap.configMap
        secret.secretRedis
        secret.secretToken
        deploy.deployment
    ] if m
]
