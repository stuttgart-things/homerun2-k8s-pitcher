"""
Service and ServiceMonitor for the /metrics endpoint of the endpoint probes
(#109). Emitted only with config.probesEnabled; the ServiceMonitor also needs
config.serviceMonitor (Prometheus Operator CRDs).
"""

import labels

config = labels.config

service = {
    apiVersion: "v1"
    kind: "Service"
    metadata: {
        name: "{}-metrics".format(config.name)
        namespace: config.namespace
        labels: labels.commonLabels
    }
    spec: {
        selector: labels.selectorLabels
        ports: [
            {
                name: "metrics"
                port: config.metricsPort
                targetPort: "metrics"
                protocol: "TCP"
            }
        ]
    }
} if config.probesEnabled else None

serviceMonitor = {
    apiVersion: "monitoring.coreos.com/v1"
    kind: "ServiceMonitor"
    metadata: {
        name: config.name
        namespace: config.namespace
        labels: {
            **labels.commonLabels
            **config.serviceMonitorLabels
        }
    }
    spec: {
        selector: {
            matchLabels: labels.selectorLabels
        }
        endpoints: [
            {
                port: "metrics"
                path: "/metrics"
                interval: config.serviceMonitorInterval
            }
        ]
    }
} if config.probesEnabled and config.serviceMonitor else None
