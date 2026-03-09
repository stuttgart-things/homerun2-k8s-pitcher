"""
Main entry point for homerun2-k8s-pitcher Kubernetes deployment
Imports all resources and exports manifests
"""

import namespace
import serviceaccount
import clusterrole
import configmap
import secret
import deploy

# Export all manifests (filter out None values)
manifests = [
    m for m in [
        namespace.namespace
        serviceaccount.serviceAccount
        clusterrole.clusterRole
        clusterrole.clusterRoleBinding
        configmap.configMap
        secret.secretRedis
        secret.secretToken
        deploy.deployment
    ] if m
]
