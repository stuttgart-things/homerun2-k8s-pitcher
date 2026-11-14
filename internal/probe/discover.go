package probe

import (
	"context"
	"log/slog"
	"net/url"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
)

// Annotations on an Ingress or HTTPRoute.
const (
	// AnnotationProbe "false" leaves the object out.
	AnnotationProbe = "homerun2.sthings.io/probe"
	// AnnotationScheme (http or https) overrides the scheme.
	AnnotationScheme = "homerun2.sthings.io/probe-scheme"
	// AnnotationPath overrides the probed path.
	AnnotationPath = "homerun2.sthings.io/probe-path"
)

// GVRs of the discovered resources.
var (
	IngressGVR   = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
	HTTPRouteGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
)

// Discoverer lists the probe targets: the explicit ones, then the hosts of
// Ingress and HTTPRoute objects.
type Discoverer struct {
	client dynamic.Interface
	spec   profile.ProbesSpec
	// last keeps the targets of the last successful list per resource, so a
	// failing list (API hiccup, RBAC) does not drop them and their findings.
	last     map[string][]Target
	unserved map[string]bool
}

// NewDiscoverer creates a Discoverer for spec.
func NewDiscoverer(client dynamic.Interface, spec profile.ProbesSpec) *Discoverer {
	return &Discoverer{client: client, spec: spec, last: map[string][]Target{}, unserved: map[string]bool{}}
}

// Targets returns all targets, each URL once (the first source wins).
func (d *Discoverer) Targets(ctx context.Context) []Target {
	var all []Target
	for _, t := range d.spec.Targets {
		u, err := url.Parse(t.URL)
		if err != nil {
			continue
		}
		name := t.Name
		if name == "" {
			name = u.Hostname()
		}
		all = append(all, Target{
			URL: t.URL, Host: u.Hostname(), Source: SourceTarget, Name: name,
			ExpectedStatus: t.ExpectedStatus, InsecureSkipVerify: t.InsecureSkipVerify,
		})
	}
	if d.spec.Discover.Ingress && d.client != nil {
		all = append(all, d.list(ctx, IngressGVR, ingressTargets)...)
	}
	if d.spec.Discover.HTTPRoute && d.client != nil {
		scheme := d.spec.Discover.HTTPRouteScheme
		all = append(all, d.list(ctx, HTTPRouteGVR, func(obj map[string]any) []Target {
			return httpRouteTargets(obj, scheme)
		})...)
	}
	seen := map[string]bool{}
	out := all[:0]
	for _, t := range all {
		if seen[t.URL] {
			continue
		}
		seen[t.URL] = true
		out = append(out, t)
	}
	return out
}

// list returns the targets of all objects of gvr. A resource that is not
// served (no Gateway API installed) is skipped; it is picked up once it is.
func (d *Discoverer) list(ctx context.Context, gvr schema.GroupVersionResource, targets func(map[string]any) []Target) []Target {
	ns := profile.ResolveNamespace(d.spec.Discover.Namespace)
	list, err := d.client.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		if !d.unserved[gvr.Resource] {
			slog.Info("probe discovery: resource not served, skipped", "group", gvr.Group, "resource", gvr.Resource)
			d.unserved[gvr.Resource] = true
		}
		d.last[gvr.Resource] = nil
		return nil
	}
	if err != nil {
		slog.Warn("probe discovery failed, keeping the last targets", "resource", gvr.Resource, "error", err)
		return d.last[gvr.Resource]
	}
	if d.unserved[gvr.Resource] {
		slog.Info("probe discovery: resource now served", "resource", gvr.Resource)
		d.unserved[gvr.Resource] = false
	}
	items := slices.Clone(list.Items)
	slices.SortFunc(items, func(a, b unstructured.Unstructured) int {
		return strings.Compare(a.GetNamespace()+"/"+a.GetName(), b.GetNamespace()+"/"+b.GetName())
	})
	var out []Target
	for _, item := range items {
		if strings.EqualFold(item.GetAnnotations()[AnnotationProbe], "false") {
			continue
		}
		out = append(out, targets(item.Object)...)
	}
	d.last[gvr.Resource] = out
	return out
}

// ingressTargets returns one target per host of an Ingress: https if the
// host is in spec.tls, the path of the rule's first path.
func ingressTargets(obj map[string]any) []Target {
	u := unstructured.Unstructured{Object: obj}
	ann := u.GetAnnotations()
	tlsHosts := map[string]bool{}
	tlsList, _, _ := unstructured.NestedSlice(obj, "spec", "tls")
	for _, e := range tlsList {
		m, _ := e.(map[string]any)
		hosts, _, _ := unstructured.NestedStringSlice(m, "hosts")
		for _, h := range hosts {
			tlsHosts[h] = true
		}
	}
	rules, _, _ := unstructured.NestedSlice(obj, "spec", "rules")
	var out []Target
	for _, r := range rules {
		rule, _ := r.(map[string]any)
		host, _ := rule["host"].(string)
		if host == "" || strings.Contains(host, "*") {
			continue
		}
		path := ""
		if paths, _, _ := unstructured.NestedSlice(rule, "http", "paths"); len(paths) > 0 {
			p, _ := paths[0].(map[string]any)
			path, _ = p["path"].(string)
		}
		scheme := "http"
		if tlsHosts[host] {
			scheme = "https"
		}
		out = append(out, target(SourceIngress, u.GetNamespace(), u.GetName(), host, scheme, path, ann))
	}
	return out
}

// httpRouteTargets returns one target per hostname of an HTTPRoute with the
// path of the first rule's first match.
func httpRouteTargets(obj map[string]any, scheme string) []Target {
	u := unstructured.Unstructured{Object: obj}
	hosts, _, _ := unstructured.NestedStringSlice(obj, "spec", "hostnames")
	path := ""
	if rules, _, _ := unstructured.NestedSlice(obj, "spec", "rules"); len(rules) > 0 {
		rule, _ := rules[0].(map[string]any)
		if matches, _, _ := unstructured.NestedSlice(rule, "matches"); len(matches) > 0 {
			m, _ := matches[0].(map[string]any)
			if typ, _, _ := unstructured.NestedString(m, "path", "type"); typ == "" || typ == "PathPrefix" || typ == "Exact" {
				path, _, _ = unstructured.NestedString(m, "path", "value")
			}
		}
	}
	var out []Target
	for _, host := range hosts {
		if host == "" || strings.Contains(host, "*") {
			continue
		}
		out = append(out, target(SourceHTTPRoute, u.GetNamespace(), u.GetName(), host, scheme, path, u.GetAnnotations()))
	}
	return out
}

// target builds a discovered target; the annotations override scheme and
// path. A path that looks like a regular expression is replaced by /.
func target(source, ns, name, host, scheme, path string, ann map[string]string) Target {
	if s := ann[AnnotationScheme]; s == "http" || s == "https" {
		scheme = s
	}
	if p, ok := ann[AnnotationPath]; ok {
		path = p
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "*()[]$^|?+{}\\") {
		path = "/"
	}
	u := url.URL{Scheme: scheme, Host: host, Path: path}
	return Target{URL: u.String(), Host: host, Source: source, Namespace: ns, Name: name}
}
