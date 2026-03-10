"""
Deployment for homerun2-k8s-pitcher
"""

import labels

config = labels.config

deployment = {
    apiVersion: "apps/v1"
    kind: "Deployment"
    metadata: {
        name: config.name
        namespace: config.namespace
        labels: labels.commonLabels
    }
    spec: {
        replicas: config.replicas
        selector: {
            matchLabels: labels.selectorLabels
        }
        strategy: {
            type: "RollingUpdate"
            rollingUpdate: {
                maxSurge: 1
                maxUnavailable: 0
            }
        }
        template: {
            metadata: {
                labels: labels.commonLabels
            }
            spec: {
                serviceAccountName: config.name
                containers: [
                    {
                        name: config.name
                        image: config.image
                        imagePullPolicy: config.imagePullPolicy
                        args: [
                            "--profile"
                            "/etc/k8s-pitcher/profile.yaml"
                        ]
                        env: [
                            {
                                name: "LOG_LEVEL"
                                value: "info"
                            }
                        ] + ([
                            {
                                name: "SSL_CERT_DIR"
                                value: "/etc/ssl/custom"
                            }
                        ] if config.trustBundleConfigMap else [])
                        resources: {
                            requests: {
                                cpu: config.cpuRequest
                                memory: config.memoryRequest
                            }
                            limits: {
                                cpu: config.cpuLimit
                                memory: config.memoryLimit
                            }
                        }
                        securityContext: {
                            readOnlyRootFilesystem: True
                            runAsNonRoot: True
                            allowPrivilegeEscalation: False
                            capabilities: {
                                drop: ["ALL"]
                            }
                        }
                        volumeMounts: [
                            {
                                name: "profile"
                                mountPath: "/etc/k8s-pitcher"
                                readOnly: True
                            }
                            {
                                name: "tmp"
                                mountPath: "/tmp"
                            }
                        ] + ([
                            {
                                name: "trust-bundle"
                                mountPath: "/etc/ssl/custom/trust-bundle.pem"
                                subPath: "trust-bundle.pem"
                                readOnly: True
                            }
                        ] if config.trustBundleConfigMap else [])
                    }
                ]
                affinity: {
                    podAntiAffinity: {
                        preferredDuringSchedulingIgnoredDuringExecution: [
                            {
                                weight: 100
                                podAffinityTerm: {
                                    labelSelector: {
                                        matchLabels: labels.selectorLabels
                                    }
                                    topologyKey: "kubernetes.io/hostname"
                                }
                            }
                        ]
                    }
                }
                securityContext: {
                    runAsNonRoot: True
                    runAsUser: 65532
                    runAsGroup: 65532
                    fsGroup: 65532
                    seccompProfile: {
                        type: "RuntimeDefault"
                    }
                }
                volumes: [
                    {
                        name: "profile"
                        configMap: {
                            name: "{}-profile".format(config.name)
                        }
                    }
                    {
                        name: "tmp"
                        emptyDir: {}
                    }
                ] + ([
                    {
                        name: "trust-bundle"
                        configMap: {
                            name: config.trustBundleConfigMap
                            optional: True
                        }
                    }
                ] if config.trustBundleConfigMap else [])
            }
        }
    }
}
