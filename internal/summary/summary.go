package summary

import (
	"fmt"
	"strings"
)

// SummarizeText produces a human-readable text summary of a K8s object.
func SummarizeText(kind string, obj map[string]any) string {
	switch kind {
	case "Node":
		return textNode(obj)
	case "Pod":
		return textPod(obj)
	case "Event":
		return textEvent(obj)
	case "Deployment":
		return textDeployment(obj)
	default:
		return textGeneric(kind, obj)
	}
}

func textNode(obj map[string]any) string {
	var b strings.Builder

	name := getStr(obj, "metadata", "name")
	created := getStr(obj, "metadata", "creationTimestamp")
	fmt.Fprintf(&b, "Name: %s\nCreated: %s\n", name, created)

	// Addresses
	if addrs := getSlice(obj, "status", "addresses"); len(addrs) > 0 {
		parts := make([]string, 0, len(addrs))
		for _, a := range addrs {
			if m, ok := a.(map[string]any); ok {
				parts = append(parts, fmt.Sprintf("%s=%s", m["type"], m["address"]))
			}
		}
		fmt.Fprintf(&b, "Addresses: %s\n", strings.Join(parts, ", "))
	}

	// Roles from labels
	if labels := getMap(obj, "metadata", "labels"); labels != nil {
		roles := []string{}
		for k, v := range labels {
			if strings.HasPrefix(k, "node-role.kubernetes.io/") {
				if v == "true" || v == "" {
					roles = append(roles, strings.TrimPrefix(k, "node-role.kubernetes.io/"))
				}
			}
		}
		if len(roles) > 0 {
			fmt.Fprintf(&b, "Roles: %s\n", strings.Join(roles, ", "))
		}
	}

	// Capacity / Allocatable
	if cap := getMap(obj, "status", "capacity"); cap != nil {
		fmt.Fprintf(&b, "Capacity: cpu=%s, memory=%s, pods=%s\n",
			strVal(cap, "cpu"), strVal(cap, "memory"), strVal(cap, "pods"))
	}
	if alloc := getMap(obj, "status", "allocatable"); alloc != nil {
		fmt.Fprintf(&b, "Allocatable: cpu=%s, memory=%s, pods=%s\n",
			strVal(alloc, "cpu"), strVal(alloc, "memory"), strVal(alloc, "pods"))
	}

	// NodeInfo
	if ni := getMap(obj, "status", "nodeInfo"); ni != nil {
		fmt.Fprintf(&b, "OS: %s, Arch: %s\nKubelet: %s, Runtime: %s\n",
			strVal(ni, "osImage"), strVal(ni, "architecture"),
			strVal(ni, "kubeletVersion"), strVal(ni, "containerRuntimeVersion"))
	}

	// PodCIDR
	if cidr := getStr(obj, "spec", "podCIDR"); cidr != "" {
		fmt.Fprintf(&b, "PodCIDR: %s\n", cidr)
	}

	// Conditions
	writeConditions(&b, obj)

	return strings.TrimSpace(b.String())
}

func textPod(obj map[string]any) string {
	var b strings.Builder

	name := getStr(obj, "metadata", "name")
	ns := getStr(obj, "metadata", "namespace")
	phase := getStr(obj, "status", "phase")
	podIP := getStr(obj, "status", "podIP")
	nodeName := getStr(obj, "spec", "nodeName")

	fmt.Fprintf(&b, "Name: %s\nNamespace: %s\nPhase: %s\n", name, ns, phase)
	if podIP != "" {
		fmt.Fprintf(&b, "PodIP: %s\n", podIP)
	}
	if nodeName != "" {
		fmt.Fprintf(&b, "Node: %s\n", nodeName)
	}

	// Containers
	if cs := getSlice(obj, "status", "containerStatuses"); len(cs) > 0 {
		b.WriteString("Containers:\n")
		for _, c := range cs {
			if cm, ok := c.(map[string]any); ok {
				ready := "not ready"
				if r, ok := cm["ready"].(bool); ok && r {
					ready = "ready"
				}
				restarts := "0"
				if rc, ok := cm["restartCount"].(float64); ok {
					restarts = fmt.Sprintf("%.0f", rc)
				}
				fmt.Fprintf(&b, "  - %s [%s] restarts=%s image=%s\n",
					cm["name"], ready, restarts, cm["image"])
			}
		}
	}

	// Conditions
	writeConditions(&b, obj)

	return strings.TrimSpace(b.String())
}

func textEvent(obj map[string]any) string {
	var b strings.Builder

	reason := anyStr(obj["reason"])
	msg := anyStr(obj["message"])
	evType := anyStr(obj["type"])
	ns := getStr(obj, "metadata", "namespace")

	fmt.Fprintf(&b, "Type: %s\nReason: %s\nNamespace: %s\nMessage: %s\n", evType, reason, ns, msg)

	if inv, ok := obj["involvedObject"].(map[string]any); ok {
		fmt.Fprintf(&b, "Object: %s/%s", anyStr(inv["kind"]), anyStr(inv["name"]))
		if invNs := anyStr(inv["namespace"]); invNs != "" {
			fmt.Fprintf(&b, " in %s", invNs)
		}
		b.WriteString("\n")
	}

	if count, ok := obj["count"].(float64); ok && count > 1 {
		fmt.Fprintf(&b, "Count: %.0f\n", count)
	}

	if src, ok := obj["source"].(map[string]any); ok {
		fmt.Fprintf(&b, "Source: %s\n", anyStr(src["component"]))
	}

	return strings.TrimSpace(b.String())
}

func textDeployment(obj map[string]any) string {
	var b strings.Builder

	name := getStr(obj, "metadata", "name")
	ns := getStr(obj, "metadata", "namespace")

	fmt.Fprintf(&b, "Name: %s\nNamespace: %s\n", name, ns)

	if status, ok := obj["status"].(map[string]any); ok {
		desired := "?"
		if spec, ok := obj["spec"].(map[string]any); ok {
			if r, ok := spec["replicas"].(float64); ok {
				desired = fmt.Sprintf("%.0f", r)
			}
		}
		ready := numStr(status["readyReplicas"])
		available := numStr(status["availableReplicas"])
		updated := numStr(status["updatedReplicas"])

		fmt.Fprintf(&b, "Replicas: %s desired, %s ready, %s available, %s updated\n",
			desired, ready, available, updated)
	}

	// Conditions
	writeConditions(&b, obj)

	return strings.TrimSpace(b.String())
}

func textGeneric(kind string, obj map[string]any) string {
	var b strings.Builder

	name := getStr(obj, "metadata", "name")
	ns := getStr(obj, "metadata", "namespace")

	fmt.Fprintf(&b, "Kind: %s\nName: %s\n", kind, name)
	if ns != "" {
		fmt.Fprintf(&b, "Namespace: %s\n", ns)
	}

	return strings.TrimSpace(b.String())
}

// writeConditions appends a compact conditions list.
func writeConditions(b *strings.Builder, obj map[string]any) {
	conditions := getSlice(obj, "status", "conditions")
	if len(conditions) == 0 {
		return
	}
	b.WriteString("Conditions:\n")
	for _, c := range conditions {
		if cm, ok := c.(map[string]any); ok {
			fmt.Fprintf(b, "  - %s=%s (%s)\n",
				anyStr(cm["type"]), anyStr(cm["status"]), anyStr(cm["reason"]))
		}
	}
}

// --- helpers ---

func getStr(obj map[string]any, keys ...string) string {
	current := obj
	for i, key := range keys {
		if i == len(keys)-1 {
			return anyStr(current[key])
		}
		if next, ok := current[key].(map[string]any); ok {
			current = next
		} else {
			return ""
		}
	}
	return ""
}

func getMap(obj map[string]any, keys ...string) map[string]any {
	current := obj
	for i, key := range keys {
		if i == len(keys)-1 {
			if v, ok := current[key].(map[string]any); ok {
				return v
			}
			return nil
		}
		if next, ok := current[key].(map[string]any); ok {
			current = next
		} else {
			return nil
		}
	}
	return nil
}

func getSlice(obj map[string]any, keys ...string) []any {
	current := obj
	for i, key := range keys {
		if i == len(keys)-1 {
			if v, ok := current[key].([]any); ok {
				return v
			}
			return nil
		}
		if next, ok := current[key].(map[string]any); ok {
			current = next
		} else {
			return nil
		}
	}
	return nil
}

func anyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func strVal(m map[string]any, key string) string {
	return anyStr(m[key])
}

func numStr(v any) string {
	if f, ok := v.(float64); ok {
		return fmt.Sprintf("%.0f", f)
	}
	return "0"
}
