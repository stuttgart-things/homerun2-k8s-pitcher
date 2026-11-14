package inventory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func obj(apiVersion, kind, ns, name string, extra map[string]any) *unstructured.Unstructured {
	o := map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name}}
	if ns != "" {
		o["metadata"].(map[string]any)["namespace"] = ns
	}
	for k, v := range extra {
		o[k] = v
	}
	return &unstructured.Unstructured{Object: o}
}

func pod(ns, name string, labels map[string]any, owner map[string]any, statuses ...map[string]any) *unstructured.Unstructured {
	var st []any
	for _, s := range statuses {
		st = append(st, s)
	}
	p := obj("v1", "Pod", ns, name, map[string]any{"status": map[string]any{"containerStatuses": st}})
	meta := p.Object["metadata"].(map[string]any)
	if labels != nil {
		meta["labels"] = labels
	}
	if owner != nil {
		owner["apiVersion"], owner["controller"], owner["uid"] = "apps/v1", true, "u"
		meta["ownerReferences"] = []any{owner}
	}
	return p
}

func cs(image, id string) map[string]any { return map[string]any{"name": "c", "image": image, "imageID": id} }

var listKinds = map[schema.GroupVersionResource]string{
	gvrNodes: "NodeList", gvrPods: "PodList", gvrHelmReleases: "HelmReleaseList",
	gvrApplications: "ApplicationList", gvrConfigs: "ConfigurationList", gvrProviders: "ProviderList", gvrFunctions: "FunctionList",
}

func fixture() []runtime.Object {
	return []runtime.Object{
		obj("v1", "Node", "", "cp-1", map[string]any{
			"metadata": map[string]any{"name": "cp-1", "labels": map[string]any{"node-role.kubernetes.io/control-plane": "true"}},
			"status": map[string]any{
				"nodeInfo":   map[string]any{"kubeletVersion": "v1.33.2", "osImage": "Ubuntu 24.04", "kernelVersion": "6.8", "containerRuntimeVersion": "containerd://2.0"},
				"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
			}}),
		pod("web", "web-7c9f-abcde", map[string]any{"pod-template-hash": "7c9f"}, map[string]any{"kind": "ReplicaSet", "name": "web-7c9f"},
			cs("ghcr.io/x/web:1.2.3", "ghcr.io/x/web@sha256:aaa")),
		pod("web", "web-7c9f-fghij", map[string]any{"pod-template-hash": "7c9f"}, map[string]any{"kind": "ReplicaSet", "name": "web-7c9f"},
			cs("ghcr.io/x/web:1.2.3", "ghcr.io/x/web@sha256:aaa")),
		pod("tools", "debug", nil, nil, cs("busybox", "docker.io/library/busybox@sha256:bbb")),
		func() *unstructured.Unstructured {
			p := pod("flux-system", "source-controller-5d-x", map[string]any{"pod-template-hash": "5d"}, map[string]any{"kind": "ReplicaSet", "name": "source-controller-5d"},
				cs("sha256:e0bf", "ghcr.io/fluxcd/source-controller@sha256:6517"))
			p.Object["spec"] = map[string]any{"containers": []any{map[string]any{"name": "c", "image": "ghcr.io/fluxcd/source-controller:v1.7.0@sha256:6517"}}}
			return p
		}(),
		pod("db", "db-0", nil, map[string]any{"kind": "StatefulSet", "name": "db"}, cs("postgres:latest", "sha256:ccc")),
		obj("helm.toolkit.fluxcd.io/v2", "HelmRelease", "flux-system", "cert-manager", map[string]any{
			"spec":   map[string]any{"chart": map[string]any{"spec": map[string]any{"chart": "cert-manager", "version": "v1.18.0"}}},
			"status": map[string]any{"lastAttemptedRevision": "v1.18.0", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
		}),
		obj("pkg.crossplane.io/v1", "Provider", "", "provider-kubernetes", map[string]any{
			"spec":   map[string]any{"package": "xpkg.crossplane.io/crossplane-contrib/provider-kubernetes:v0.18.0"},
			"status": map[string]any{"conditions": []any{map[string]any{"type": "Healthy", "status": "True"}}},
		}),
	}
}

func TestGather(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, fixture()...)
	inv, err := Gather(context.Background(), client, "c1", "v1.33.2")
	if err != nil {
		t.Fatal(err)
	}
	if inv.Format != Format || inv.Target != "c1" || len(inv.Nodes) != 1 || inv.Nodes[0].Kubelet != "v1.33.2" || !inv.Nodes[0].Ready || inv.Nodes[0].Roles[0] != "control-plane" {
		t.Errorf("nodes %+v", inv.Nodes)
	}
	byImage := map[string]Image{}
	for _, im := range inv.Images {
		byImage[im.Image] = im
	}
	if f := byImage["ghcr.io/fluxcd/source-controller:v1.7.0@sha256:6517"]; f.Digest != "sha256:6517" || f.Latest || f.Workloads[0] != "Deployment/flux-system/source-controller" {
		t.Errorf("digest-pinned image from the spec: %+v", f)
	}
	if inv.Count != 4 || len(inv.Images) != 4 {
		t.Fatalf("images %+v", inv.Images)
	}
	if w := byImage["ghcr.io/x/web:1.2.3"]; w.Pods != 2 || w.Digest != "sha256:aaa" || w.Latest || strings.Join(w.Workloads, ",") != "Deployment/web/web" {
		t.Errorf("web %+v", w)
	}
	if b := byImage["busybox"]; !b.Latest || b.Workloads[0] != "Pod/tools/debug" {
		t.Errorf("busybox %+v", b)
	}
	if p := byImage["postgres:latest"]; !p.Latest || p.Digest != "sha256:ccc" || p.Workloads[0] != "StatefulSet/db/db" {
		t.Errorf("postgres %+v", p)
	}
	if len(inv.Releases) != 2 {
		t.Fatalf("releases %+v", inv.Releases)
	}
	hr, pr := inv.Releases[0], inv.Releases[1]
	if hr.Kind != "HelmRelease" || hr.Chart != "cert-manager" || hr.Version != "v1.18.0" || hr.Status != "Ready" {
		t.Errorf("helmrelease %+v", hr)
	}
	if pr.Kind != "Provider" || pr.Version != "v0.18.0" || pr.Chart != "xpkg.crossplane.io/crossplane-contrib/provider-kubernetes" || pr.Status != "Healthy" {
		t.Errorf("provider %+v", pr)
	}
	title, text := inv.Summary()
	if !strings.Contains(title, "Inventory c1: v1.33.2, 1 nodes, 4 images, 2 releases") || !strings.Contains(text, "2 without a pinned tag") {
		t.Errorf("summary %q\n%s", title, text)
	}
}

// Release kinds that are not served are skipped, nodes and pods are required.
func TestGatherWithoutReleaseCRDs(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	for _, gvr := range []schema.GroupVersionResource{gvrHelmReleases, gvrApplications, gvrConfigs, gvrProviders} {
		client.PrependReactor("list", gvr.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewNotFound(gvr.GroupResource(), "")
		})
	}
	client.PrependReactor("list", gvrFunctions.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(gvrFunctions.GroupResource(), "", nil)
	})
	inv, err := Gather(context.Background(), client, "c1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Releases) != 0 {
		t.Errorf("releases %+v", inv.Releases)
	}
	if title, _ := inv.Summary(); !strings.Contains(title, "version unknown") {
		t.Errorf("title %q", title)
	}
}

func TestIsLatest(t *testing.T) {
	for ref, want := range map[string]bool{
		"nginx": true, "nginx:latest": true, "registry:5000/nginx": true, "registry:5000/nginx:1.27": false,
		"ghcr.io/x/y@sha256:abc": false, "ghcr.io/x/y:v1": false,
	} {
		if got := isLatest(ref); got != want {
			t.Errorf("%s: %v", ref, got)
		}
	}
}

func TestFit(t *testing.T) {
	inv := &Inventory{Format: Format}
	for i := 0; i < 2000; i++ {
		inv.Images = append(inv.Images, Image{Image: strings.Repeat("x", 100), Workloads: []string{"Deployment/a/b", "Deployment/a/c"}})
	}
	inv.Count = len(inv.Images)
	inv.Fit(64 * 1024)
	b, _ := json.Marshal(inv)
	if len(b) > 64*1024 || !inv.Trimmed || inv.Truncated == 0 || inv.Count != 2000 || inv.Truncated+len(inv.Images) != 2000 {
		t.Errorf("size %d trimmed %v truncated %d kept %d", len(b), inv.Trimmed, inv.Truncated, len(inv.Images))
	}
}
