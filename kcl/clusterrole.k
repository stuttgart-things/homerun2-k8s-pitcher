"""
RBAC for homerun2-k8s-pitcher — grants read access to watched K8s resources.

When config.namespaceScoped is false (default), a cluster-scoped ClusterRole +
ClusterRoleBinding are emitted. When true, a namespaced Role + RoleBinding are
emitted instead — used by per-PR preview environments.
"""

import labels

config = labels.config

# Shared rule set for both the cluster-scoped and namespaced variants.
_rules = [
    {
        apiGroups: config.watchedApiGroups
        resources: config.watchedResources
        verbs: ["get", "list", "watch"]
    }
    {
        apiGroups: [""]
        resources: ["secrets"]
        verbs: ["get"]
    }
] + [
    {
        apiGroups: rule.apiGroups
        resources: rule.resources
        verbs: rule.verbs or ["get", "list", "watch"]
    }
    for rule in config.extraRbacRules
]

# Cluster-scoped variant (default).
clusterRole = {
    apiVersion: "rbac.authorization.k8s.io/v1"
    kind: "ClusterRole"
    metadata: {
        name: config.name
        labels: labels.commonLabels
    }
    rules: _rules
} if not config.namespaceScoped else None

clusterRoleBinding = {
    apiVersion: "rbac.authorization.k8s.io/v1"
    kind: "ClusterRoleBinding"
    metadata: {
        name: config.name
        labels: labels.commonLabels
    }
    roleRef: {
        apiGroup: "rbac.authorization.k8s.io"
        kind: "ClusterRole"
        name: config.name
    }
    subjects: [
        {
            kind: "ServiceAccount"
            name: config.name
            namespace: config.namespace
        }
    ]
} if not config.namespaceScoped else None

# Namespace-scoped variant — k8s-pitcher watches only its own namespace.
role = {
    apiVersion: "rbac.authorization.k8s.io/v1"
    kind: "Role"
    metadata: {
        name: config.name
        namespace: config.namespace
        labels: labels.commonLabels
    }
    rules: _rules
} if config.namespaceScoped else None

roleBinding = {
    apiVersion: "rbac.authorization.k8s.io/v1"
    kind: "RoleBinding"
    metadata: {
        name: config.name
        namespace: config.namespace
        labels: labels.commonLabels
    }
    roleRef: {
        apiGroup: "rbac.authorization.k8s.io"
        kind: "Role"
        name: config.name
    }
    subjects: [
        {
            kind: "ServiceAccount"
            name: config.name
            namespace: config.namespace
        }
    ]
} if config.namespaceScoped else None
