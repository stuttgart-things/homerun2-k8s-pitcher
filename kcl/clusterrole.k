"""
ClusterRole and ClusterRoleBinding for homerun2-k8s-pitcher
Grants read access to watched K8s resources
"""

import labels

config = labels.config

clusterRole = {
    apiVersion: "rbac.authorization.k8s.io/v1"
    kind: "ClusterRole"
    metadata: {
        name: config.name
        labels: labels.commonLabels
    }
    rules: [
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
}

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
}
