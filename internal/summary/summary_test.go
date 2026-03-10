package summary

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSummarizeTextNode(t *testing.T) {
	raw := `{
		"apiVersion": "v1",
		"kind": "Node",
		"metadata": {
			"name": "node-1",
			"labels": {
				"kubernetes.io/os": "linux",
				"node-role.kubernetes.io/control-plane": "true",
				"beta.kubernetes.io/os": "linux"
			},
			"creationTimestamp": "2026-01-01T00:00:00Z",
			"managedFields": [{"manager": "k3s"}]
		},
		"spec": {"podCIDR": "10.42.0.0/24", "providerID": "k3s://node-1"},
		"status": {
			"conditions": [{"type": "Ready", "status": "True", "reason": "KubeletReady"}],
			"addresses": [{"type": "InternalIP", "address": "10.0.0.1"}],
			"capacity": {"cpu": "8", "memory": "8Gi", "pods": "110"},
			"allocatable": {"cpu": "8", "memory": "7Gi", "pods": "110"},
			"nodeInfo": {
				"kubeletVersion": "v1.35.0",
				"containerRuntimeVersion": "containerd://2.0",
				"osImage": "Ubuntu 24.04",
				"architecture": "amd64",
				"machineID": "abc123",
				"bootID": "xyz789"
			},
			"images": [{"names": ["nginx:latest"], "sizeBytes": 100000}]
		}
	}`

	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatal(err)
	}

	text := SummarizeText("Node", obj)

	for _, want := range []string{
		"Name: node-1",
		"InternalIP=10.0.0.1",
		"Roles: control-plane",
		"Capacity: cpu=8",
		"Kubelet: v1.35.0",
		"OS: Ubuntu 24.04",
		"PodCIDR: 10.42.0.0/24",
		"Ready=True (KubeletReady)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}

	// Should NOT contain raw JSON artifacts
	for _, bad := range []string{`"managedFields"`, `"images"`, `"machineID"`, `"bootID"`} {
		if strings.Contains(text, bad) {
			t.Errorf("should not contain %s in:\n%s", bad, text)
		}
	}
}

func TestSummarizeTextPod(t *testing.T) {
	raw := `{
		"apiVersion": "v1",
		"kind": "Pod",
		"metadata": {"name": "nginx-abc", "namespace": "default"},
		"spec": {"nodeName": "node-1"},
		"status": {
			"phase": "Running",
			"podIP": "10.0.0.5",
			"containerStatuses": [
				{"name": "nginx", "ready": true, "restartCount": 0, "image": "nginx:latest", "state": {"running": {}}}
			],
			"conditions": [{"type": "Ready", "status": "True", "reason": "PodReady"}]
		}
	}`

	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatal(err)
	}

	text := SummarizeText("Pod", obj)

	for _, want := range []string{
		"Name: nginx-abc",
		"Namespace: default",
		"Phase: Running",
		"PodIP: 10.0.0.5",
		"Node: node-1",
		"nginx [ready] restarts=0 image=nginx:latest",
		"Ready=True (PodReady)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestSummarizeTextEvent(t *testing.T) {
	raw := `{
		"apiVersion": "v1",
		"kind": "Event",
		"metadata": {"name": "pod.abc123", "namespace": "default"},
		"type": "Normal",
		"reason": "Scheduled",
		"message": "Successfully assigned default/nginx to node-1",
		"involvedObject": {"kind": "Pod", "name": "nginx", "namespace": "default"},
		"count": 3,
		"source": {"component": "default-scheduler"}
	}`

	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatal(err)
	}

	text := SummarizeText("Event", obj)

	for _, want := range []string{
		"Type: Normal",
		"Reason: Scheduled",
		"Object: Pod/nginx in default",
		"Count: 3",
		"Source: default-scheduler",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestSummarizeTextDeployment(t *testing.T) {
	raw := `{
		"apiVersion": "apps/v1",
		"kind": "Deployment",
		"metadata": {"name": "web", "namespace": "production"},
		"spec": {"replicas": 3},
		"status": {
			"replicas": 3,
			"readyReplicas": 3,
			"availableReplicas": 3,
			"updatedReplicas": 3,
			"conditions": [{"type": "Available", "status": "True", "reason": "MinimumReplicasAvailable"}]
		}
	}`

	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatal(err)
	}

	text := SummarizeText("Deployment", obj)

	for _, want := range []string{
		"Name: web",
		"Namespace: production",
		"3 desired, 3 ready, 3 available, 3 updated",
		"Available=True (MinimumReplicasAvailable)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestSummarizeTextGeneric(t *testing.T) {
	raw := `{
		"apiVersion": "apps/v1",
		"kind": "StatefulSet",
		"metadata": {"name": "redis", "namespace": "db"}
	}`

	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatal(err)
	}

	text := SummarizeText("StatefulSet", obj)

	if !strings.Contains(text, "Kind: StatefulSet") {
		t.Errorf("missing kind in:\n%s", text)
	}
	if !strings.Contains(text, "Name: redis") {
		t.Errorf("missing name in:\n%s", text)
	}
}
