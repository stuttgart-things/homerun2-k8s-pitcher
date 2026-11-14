// Package inventory gathers what runs on a cluster (#108): the Kubernetes
// version, the nodes, every image in use and the releases of Flux, Argo CD
// and Crossplane. One snapshot per run, payload format inventory/v1
// (stuttgart-things/stuttgart-things#4556). It reports, it does not judge:
// outdated or :latest images are for the analysis to point out.
package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Format of the payload.
const Format = "inventory/v1"

// MaxWorkloadsPerImage bounds the workloads listed per image.
const MaxWorkloadsPerImage = 10

// Inventory is the payload of one snapshot.
type Inventory struct {
	Format   string    `json:"format"`
	Source   string    `json:"source"`
	Target   string    `json:"target"`
	Version  string    `json:"version,omitempty"` // server gitVersion
	Nodes    []Node    `json:"nodes"`
	Images   []Image   `json:"images"`
	Releases []Release `json:"releases"`
	// Count is the number of images before trimming.
	Count     int  `json:"count"`
	Truncated int  `json:"truncated,omitempty"` // images cut to fit
	Trimmed   bool `json:"trimmed,omitempty"`   // workload lists cut to fit
}

// Node is one node's versions.
type Node struct {
	Name             string   `json:"name"`
	Roles            []string `json:"roles,omitempty"`
	Kubelet          string   `json:"kubelet"`
	OSImage          string   `json:"osImage"`
	Kernel           string   `json:"kernel"`
	ContainerRuntime string   `json:"containerRuntime"`
	Ready            bool     `json:"ready"`
}

// Image is one image reference and digest in use.
type Image struct {
	Image      string   `json:"image"`
	Digest     string   `json:"digest,omitempty"`
	Pods       int      `json:"pods"`
	Namespaces []string `json:"namespaces"`
	Workloads  []string `json:"workloads"` // Kind/namespace/name
	Latest     bool     `json:"latest,omitempty"`
}

// Release is a HelmRelease, an Argo CD Application or a Crossplane package.
type Release struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Chart     string `json:"chart,omitempty"`   // chart, repo URL or package
	Version   string `json:"version,omitempty"` // desired: chart version, targetRevision, package tag
	Revision  string `json:"revision,omitempty"`
	Status    string `json:"status,omitempty"` // Ready, Synced/Healthy, Healthy
}

var (
	gvrNodes        = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	gvrPods         = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	gvrHelmReleases = schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	gvrApplications = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	gvrConfigs      = schema.GroupVersionResource{Group: "pkg.crossplane.io", Version: "v1", Resource: "configurations"}
	gvrProviders    = schema.GroupVersionResource{Group: "pkg.crossplane.io", Version: "v1", Resource: "providers"}
	gvrFunctions    = schema.GroupVersionResource{Group: "pkg.crossplane.io", Version: "v1", Resource: "functions"}
)

// Gather lists nodes and pods (required) and the release kinds (each only
// if served). version may be empty.
func Gather(ctx context.Context, client dynamic.Interface, target, version string) (*Inventory, error) {
	inv := &Inventory{Format: Format, Source: "inventory", Target: target, Version: version}

	nodes, err := client.Resource(gvrNodes).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	for _, n := range nodes.Items {
		inv.Nodes = append(inv.Nodes, node(n))
	}
	sort.Slice(inv.Nodes, func(a, b int) bool { return inv.Nodes[a].Name < inv.Nodes[b].Name })

	pods, err := client.Resource(gvrPods).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	inv.Images = images(pods.Items)
	inv.Count = len(inv.Images)

	for _, r := range []struct {
		gvr  schema.GroupVersionResource
		kind string
		fn   func(unstructured.Unstructured) Release
	}{
		{gvrHelmReleases, "HelmRelease", helmRelease},
		{gvrApplications, "Application", application},
		{gvrConfigs, "Configuration", crossplanePackage},
		{gvrProviders, "Provider", crossplanePackage},
		{gvrFunctions, "Function", crossplanePackage},
	} {
		list, err := client.Resource(r.gvr).Namespace("").List(ctx, metav1.ListOptions{})
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			continue // not served here, or not granted: the inventory is still useful
		}
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", r.gvr.Resource, err)
		}
		for _, it := range list.Items {
			rel := r.fn(it)
			rel.Kind = r.kind
			inv.Releases = append(inv.Releases, rel)
		}
	}
	sort.SliceStable(inv.Releases, func(a, b int) bool {
		ra, rb := inv.Releases[a], inv.Releases[b]
		return ra.Kind+"/"+ra.Namespace+"/"+ra.Name < rb.Kind+"/"+rb.Namespace+"/"+rb.Name
	})
	return inv, nil
}

func node(n unstructured.Unstructured) Node {
	info, _, _ := unstructured.NestedStringMap(n.Object, "status", "nodeInfo")
	out := Node{Name: n.GetName(), Kubelet: info["kubeletVersion"], OSImage: info["osImage"],
		Kernel: info["kernelVersion"], ContainerRuntime: info["containerRuntimeVersion"]}
	for l := range n.GetLabels() {
		if role, ok := strings.CutPrefix(l, "node-role.kubernetes.io/"); ok && role != "" {
			out.Roles = append(out.Roles, role)
		}
	}
	sort.Strings(out.Roles)
	conds, _, _ := unstructured.NestedSlice(n.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] == "Ready" {
			out.Ready = m["status"] == "True"
		}
	}
	return out
}

// images groups the containers of all pods by image and digest.
func images(pods []unstructured.Unstructured) []Image {
	type acc struct {
		img        Image
		pods       map[string]bool
		namespaces map[string]bool
		workloads  map[string]bool
	}
	byKey := map[string]*acc{}
	for _, p := range pods {
		w := workload(p)
		specImage := map[string]string{} // container name -> image as written
		for _, field := range []string{"initContainers", "containers", "ephemeralContainers"} {
			cs, _, _ := unstructured.NestedSlice(p.Object, "spec", field)
			for _, c := range cs {
				m, _ := c.(map[string]any)
				name, _ := m["name"].(string)
				specImage[name], _ = m["image"].(string)
			}
		}
		for _, field := range []string{"initContainerStatuses", "containerStatuses", "ephemeralContainerStatuses"} {
			sts, _, _ := unstructured.NestedSlice(p.Object, "status", field)
			for _, s := range sts {
				m, _ := s.(map[string]any)
				image, _ := m["image"].(string)
				// The runtime reports a bare image ID for references pinned
				// by digest; the spec has the reference as written.
				if name, _ := m["name"].(string); specImage[name] != "" && (image == "" || strings.HasPrefix(image, "sha256:")) {
					image = specImage[name]
				}
				if image == "" {
					continue
				}
				digest := digestOf(m["imageID"])
				k := image + "@" + digest
				a := byKey[k]
				if a == nil {
					a = &acc{img: Image{Image: image, Digest: digest, Latest: isLatest(image)},
						pods: map[string]bool{}, namespaces: map[string]bool{}, workloads: map[string]bool{}}
					byKey[k] = a
				}
				a.pods[p.GetNamespace()+"/"+p.GetName()] = true
				a.namespaces[p.GetNamespace()] = true
				a.workloads[w] = true
			}
		}
	}
	out := make([]Image, 0, len(byKey))
	for _, a := range byKey {
		a.img.Pods = len(a.pods)
		a.img.Namespaces = keys(a.namespaces)
		a.img.Workloads = keys(a.workloads)
		if len(a.img.Workloads) > MaxWorkloadsPerImage {
			a.img.Workloads = append(a.img.Workloads[:MaxWorkloadsPerImage], fmt.Sprintf("… %d more", len(a.workloads)-MaxWorkloadsPerImage))
		}
		out = append(out, a.img)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Image != out[b].Image {
			return out[a].Image < out[b].Image
		}
		return out[a].Digest < out[b].Digest
	})
	return out
}

// workload names the pod's controller: Deployment for a ReplicaSet with a
// pod-template-hash, else the first owner, else the pod.
func workload(p unstructured.Unstructured) string {
	ns := p.GetNamespace()
	for _, o := range p.GetOwnerReferences() {
		if o.Controller == nil || !*o.Controller {
			continue
		}
		if o.Kind == "ReplicaSet" {
			if h := p.GetLabels()["pod-template-hash"]; h != "" {
				if d, ok := strings.CutSuffix(o.Name, "-"+h); ok {
					return "Deployment/" + ns + "/" + d
				}
			}
		}
		if o.Kind == "Job" {
			// A CronJob's Job is <cronjob>-<schedule time>.
			if i := strings.LastIndex(o.Name, "-"); i > 0 && p.GetLabels()["batch.kubernetes.io/controller-uid"] != "" && isDigits(o.Name[i+1:]) {
				return "CronJob/" + ns + "/" + o.Name[:i]
			}
		}
		return o.Kind + "/" + ns + "/" + o.Name
	}
	return "Pod/" + ns + "/" + p.GetName()
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// digestOf returns the sha256:… part of a container status imageID.
func digestOf(v any) string {
	s, _ := v.(string)
	if i := strings.LastIndex(s, "@"); i >= 0 {
		return s[i+1:]
	}
	if strings.HasPrefix(s, "sha256:") {
		return s
	}
	return ""
}

// isLatest: tag latest, or no tag and no digest in the reference.
func isLatest(ref string) bool {
	if strings.Contains(ref, "@") {
		return false
	}
	last := ref[strings.LastIndex(ref, "/")+1:]
	i := strings.LastIndex(last, ":")
	return i < 0 || last[i+1:] == "latest"
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func str(o map[string]any, path ...string) string {
	s, _, _ := unstructured.NestedString(o, path...)
	return s
}

func condition(o map[string]any, typ string) string {
	conds, _, _ := unstructured.NestedSlice(o, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] == typ {
			s, _ := m["status"].(string)
			return s
		}
	}
	return ""
}

func helmRelease(u unstructured.Unstructured) Release {
	o := u.Object
	r := Release{Namespace: u.GetNamespace(), Name: u.GetName(),
		Chart:    str(o, "spec", "chart", "spec", "chart"),
		Version:  str(o, "spec", "chart", "spec", "version"),
		Revision: str(o, "status", "lastAttemptedRevision"),
		Status:   readyStatus(condition(o, "Ready"))}
	if r.Chart == "" {
		if ref := str(o, "spec", "chartRef", "name"); ref != "" {
			r.Chart = str(o, "spec", "chartRef", "kind") + "/" + ref
		}
	}
	return r
}

func application(u unstructured.Unstructured) Release {
	o := u.Object
	src, _, _ := unstructured.NestedMap(o, "spec", "source")
	if src == nil {
		if srcs, _, _ := unstructured.NestedSlice(o, "spec", "sources"); len(srcs) > 0 {
			src, _ = srcs[0].(map[string]any)
		}
	}
	chart := str(src, "chart")
	if chart == "" {
		chart = str(src, "repoURL")
		if p := str(src, "path"); p != "" {
			chart += "//" + p
		}
	}
	return Release{Namespace: u.GetNamespace(), Name: u.GetName(), Chart: chart,
		Version:  str(src, "targetRevision"),
		Revision: str(o, "status", "sync", "revision"),
		Status:   str(o, "status", "sync", "status") + "/" + str(o, "status", "health", "status")}
}

func crossplanePackage(u unstructured.Unstructured) Release {
	o := u.Object
	pkg := str(o, "spec", "package")
	r := Release{Name: u.GetName(), Chart: pkg, Revision: str(o, "status", "currentRevision")}
	if i := strings.LastIndex(pkg, "@"); i >= 0 {
		r.Chart, r.Version = pkg[:i], pkg[i+1:]
	} else if i := strings.LastIndex(pkg, ":"); i > strings.LastIndex(pkg, "/") {
		r.Chart, r.Version = pkg[:i], pkg[i+1:]
	}
	if condition(o, "Healthy") == "True" {
		r.Status = "Healthy"
	} else if s := condition(o, "Healthy"); s != "" {
		r.Status = "Unhealthy"
	}
	return r
}

func readyStatus(s string) string {
	switch s {
	case "True":
		return "Ready"
	case "False":
		return "NotReady"
	default:
		return s
	}
}

// Fit keeps the payload under limit bytes: first the workload lists shrink to
// one entry, then images are cut from the end. Count stays the total.
func (inv *Inventory) Fit(limit int) {
	if size(inv) <= limit {
		return
	}
	for i := range inv.Images {
		if len(inv.Images[i].Workloads) > 1 {
			inv.Images[i].Workloads = inv.Images[i].Workloads[:1]
			inv.Trimmed = true
		}
	}
	for size(inv) > limit && len(inv.Images) > 0 {
		cut := max(len(inv.Images)/10, 1)
		inv.Images = inv.Images[:len(inv.Images)-cut]
		inv.Truncated += cut
	}
}

func size(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// Summary is the message title and text of a snapshot.
func (inv *Inventory) Summary() (title, text string) {
	latest := 0
	for _, im := range inv.Images {
		if im.Latest {
			latest++
		}
	}
	ready := 0
	kubelets := map[string]bool{}
	for _, n := range inv.Nodes {
		if n.Ready {
			ready++
		}
		kubelets[n.Kubelet] = true
	}
	title = fmt.Sprintf("Inventory %s: %s, %d nodes, %d images, %d releases", inv.Target, orUnknown(inv.Version), len(inv.Nodes), inv.Count, len(inv.Releases))
	var b strings.Builder
	fmt.Fprintf(&b, "Kubernetes %s; nodes %d (%d ready), kubelet %s\n", orUnknown(inv.Version), len(inv.Nodes), ready, strings.Join(keys(kubelets), ", "))
	fmt.Fprintf(&b, "Images: %d distinct, %d without a pinned tag (latest or untagged)\n", inv.Count, latest)
	byKind := map[string]int{}
	for _, r := range inv.Releases {
		byKind[r.Kind]++
	}
	var parts []string
	for _, k := range keys(boolKeys(byKind)) {
		parts = append(parts, fmt.Sprintf("%d %s", byKind[k], k))
	}
	if len(parts) > 0 {
		fmt.Fprintf(&b, "Releases: %s\n", strings.Join(parts, ", "))
	}
	if inv.Truncated > 0 {
		fmt.Fprintf(&b, "(%d images not in the payload: size limit)\n", inv.Truncated)
	}
	return title, b.String()
}

func boolKeys(m map[string]int) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func orUnknown(s string) string {
	if s == "" {
		return "version unknown"
	}
	return s
}
