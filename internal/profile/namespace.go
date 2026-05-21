package profile

import (
	"os"
	"strings"
)

// SelfNamespaceSentinel is the profile namespace value meaning "the namespace
// this process runs in". Informer/collector specs and secretKeyRefs use it so
// a profile can be namespace-agnostic — useful for per-PR preview environments
// where the namespace name is not known when the profile is authored.
const SelfNamespaceSentinel = "@self"

// serviceAccountNamespaceFile is the in-cluster path kubelet projects the
// pod's namespace into.
const serviceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// SelfNamespace returns the namespace this process runs in. It prefers the
// POD_NAMESPACE environment variable (injected via the downward API), falls
// back to the in-cluster service-account namespace file, and finally
// "default" when neither is available (e.g. running off-cluster).
func SelfNamespace() string {
	if ns := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); ns != "" {
		return ns
	}
	if data, err := os.ReadFile(serviceAccountNamespaceFile); err == nil {
		if ns := strings.TrimSpace(string(data)); ns != "" {
			return ns
		}
	}
	return "default"
}

// ResolveNamespace maps a profile namespace field to a client-go namespace
// argument for a watch or list:
//
//   - "@self"        -> the pod's own namespace (SelfNamespace)
//   - "*" or ""      -> all namespaces (the empty string, metav1.NamespaceAll)
//   - anything else  -> the value unchanged
func ResolveNamespace(specNs string) string {
	switch specNs {
	case SelfNamespaceSentinel:
		return SelfNamespace()
	case "*", "":
		return "" // metav1.NamespaceAll
	default:
		return specNs
	}
}

// ResolveSecretNamespace maps a secretKeyRef namespace to a concrete namespace
// for a secret lookup. Unlike ResolveNamespace, there is no "all namespaces"
// case — a secret Get needs a concrete namespace — so an empty value or the
// "@self" sentinel both resolve to the pod's own namespace.
func ResolveSecretNamespace(ns string) string {
	if ns == "" || ns == SelfNamespaceSentinel {
		return SelfNamespace()
	}
	return ns
}
