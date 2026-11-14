package probe

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
)

func obj(apiVersion, kind, ns, name string, ann map[string]any, spec map[string]any) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "namespace": ns}
	if ann != nil {
		meta["annotations"] = ann
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion, "kind": kind, "metadata": meta, "spec": spec,
	}}
}

func fakeClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		IngressGVR:   "IngressList",
		HTTPRouteGVR: "HTTPRouteList",
	}, objs...)
}

func TestDiscover(t *testing.T) {
	ing := obj("networking.k8s.io/v1", "Ingress", "web", "shop", nil, map[string]any{
		"tls": []any{map[string]any{"hosts": []any{"shop.example.com"}}},
		"rules": []any{
			map[string]any{"host": "shop.example.com", "http": map[string]any{"paths": []any{
				map[string]any{"path": "/api", "pathType": "Prefix"},
			}}},
			map[string]any{"host": "plain.example.com"},
			map[string]any{"host": "*.wild.example.com"},
			map[string]any{"host": "regex.example.com", "http": map[string]any{"paths": []any{
				map[string]any{"path": "/(.*)", "pathType": "ImplementationSpecific"},
			}}},
		},
	})
	optOut := obj("networking.k8s.io/v1", "Ingress", "web", "hidden", map[string]any{AnnotationProbe: "false"}, map[string]any{
		"rules": []any{map[string]any{"host": "hidden.example.com"}},
	})
	route := obj("gateway.networking.k8s.io/v1", "HTTPRoute", "argocd", "argocd-server", nil, map[string]any{
		"hostnames": []any{"argo.example.com", "shop.example.com"},
		"rules": []any{map[string]any{"matches": []any{
			map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/ui"}},
		}}},
	})
	plainRoute := obj("gateway.networking.k8s.io/v1", "HTTPRoute", "dev", "app",
		map[string]any{AnnotationScheme: "http", AnnotationPath: "/healthz"},
		map[string]any{"hostnames": []any{"dev.example.com"}})

	spec := profile.ProbesSpec{
		Discover: profile.ProbeDiscover{Ingress: true, HTTPRoute: true, HTTPRouteScheme: "https"},
		Targets:  []profile.ProbeTarget{{URL: "https://ext.example.org/health", ExpectedStatus: []int{200}, InsecureSkipVerify: true}},
	}
	d := NewDiscoverer(fakeClient(ing, optOut, route, plainRoute), spec)
	got := d.Targets(context.Background())

	want := []Target{
		{URL: "https://ext.example.org/health", Host: "ext.example.org", Source: SourceTarget, Name: "ext.example.org", ExpectedStatus: []int{200}, InsecureSkipVerify: true},
		{URL: "https://shop.example.com/api", Host: "shop.example.com", Source: SourceIngress, Namespace: "web", Name: "shop"},
		{URL: "http://plain.example.com/", Host: "plain.example.com", Source: SourceIngress, Namespace: "web", Name: "shop"},
		{URL: "http://regex.example.com/", Host: "regex.example.com", Source: SourceIngress, Namespace: "web", Name: "shop"},
		{URL: "https://argo.example.com/ui", Host: "argo.example.com", Source: SourceHTTPRoute, Namespace: "argocd", Name: "argocd-server"},
		{URL: "https://shop.example.com/ui", Host: "shop.example.com", Source: SourceHTTPRoute, Namespace: "argocd", Name: "argocd-server"},
		{URL: "http://dev.example.com/healthz", Host: "dev.example.com", Source: SourceHTTPRoute, Namespace: "dev", Name: "app"},
	}
	if len(got) != len(want) {
		t.Fatalf("targets = %+v", got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.URL != w.URL || g.Host != w.Host || g.Source != w.Source || g.Namespace != w.Namespace || g.Name != w.Name ||
			g.InsecureSkipVerify != w.InsecureSkipVerify || len(g.ExpectedStatus) != len(w.ExpectedStatus) {
			t.Errorf("target %d = %+v, want %+v", i, g, w)
		}
	}
	if got[4].Parent() != "HTTPRoute/argocd/argocd-server" || got[0].Parent() != "" {
		t.Errorf("parents %q %q", got[4].Parent(), got[0].Parent())
	}
}

func TestDiscoverUnservedAndErrors(t *testing.T) {
	ing := obj("networking.k8s.io/v1", "Ingress", "web", "shop", nil, map[string]any{
		"rules": []any{map[string]any{"host": "shop.example.com"}},
	})
	c := fakeClient(ing)
	routeErr := apierrors.NewNotFound(HTTPRouteGVR.GroupResource(), "")
	c.PrependReactor("list", "httproutes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, routeErr
	})
	var ingErr error
	c.PrependReactor("list", "ingresses", func(k8stesting.Action) (bool, runtime.Object, error) {
		if ingErr != nil {
			return true, nil, ingErr
		}
		return false, nil, nil
	})
	d := NewDiscoverer(c, profile.ProbesSpec{Discover: profile.ProbeDiscover{Ingress: true, HTTPRoute: true, HTTPRouteScheme: "https"}})
	if got := d.Targets(context.Background()); len(got) != 1 || got[0].URL != "http://shop.example.com/" {
		t.Fatalf("unserved HTTPRoute: %+v", got)
	}
	// A failing list keeps the last targets.
	ingErr = errors.New("connection refused")
	if got := d.Targets(context.Background()); len(got) != 1 {
		t.Errorf("failing list: %+v", got)
	}
}
