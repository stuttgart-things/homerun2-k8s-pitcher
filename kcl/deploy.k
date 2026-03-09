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
                        ]
                        envFrom: if config.extraEnvVars then [
                            {
                                configMapRef: {
                                    name: "{}-env".format(config.name)
                                }
                            }
                        ] else []
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
                        livenessProbe: {
                            exec: {
                                command: ["cat", "/tmp/healthy"]
                            }
                            initialDelaySeconds: 15
                            periodSeconds: 30
                            timeoutSeconds: 5
                            failureThreshold: 3
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
                        ]
                    }
                ]
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
                ]
            }
        }
    }
}
