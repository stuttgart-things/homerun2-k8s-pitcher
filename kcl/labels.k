"""
Common labels for homerun2-k8s-pitcher resources
"""

import schema

# Get command-line options
_image = option("config.image")
_namespace = option("config.namespace")
_profileYaml = option("config.profileYaml")
_pitcherAddr = option("config.pitcherAddr")
_pitcherInsecure = option("config.pitcherInsecure")
_redisPassword = option("config.redisPassword")
_authToken = option("config.authToken")
_watchedResources = option("config.watchedResources") or []
_watchedApiGroups = option("config.watchedApiGroups") or []
_trustBundleConfigMap = option("config.trustBundleConfigMap")
_extraRbacRules = option("config.extraRbacRules") or []
_extraEnvVars = option("config.extraEnvVars") or {}

# Config instance with command-line overrides
config: schema.K8sPitcher = schema.K8sPitcher {
    if _image:
        image = _image
    if _namespace:
        namespace = _namespace
    if _profileYaml:
        profileYaml = _profileYaml
    if _pitcherAddr:
        pitcherAddr = _pitcherAddr
    if _pitcherInsecure:
        pitcherInsecure = _pitcherInsecure == "true" or _pitcherInsecure == "True"
    if _redisPassword:
        redisPassword = _redisPassword
    if _authToken:
        authToken = _authToken
    if _watchedResources:
        watchedResources = _watchedResources
    if _watchedApiGroups:
        watchedApiGroups = _watchedApiGroups
    if _trustBundleConfigMap:
        trustBundleConfigMap = _trustBundleConfigMap
    if _extraRbacRules:
        extraRbacRules = _extraRbacRules
    if _extraEnvVars:
        extraEnvVars = _extraEnvVars
}

# Common labels applied to all resources
commonLabels = {
    "app.kubernetes.io/name": config.name
    "app.kubernetes.io/instance": config.name
    "app.kubernetes.io/component": "watcher"
    "app.kubernetes.io/part-of": "homerun2"
    **config.labels
}

# Selector labels for pods
selectorLabels = {
    "app.kubernetes.io/name": config.name
    "app.kubernetes.io/instance": config.name
}
