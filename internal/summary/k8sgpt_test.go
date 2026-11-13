package summary

import (
	"strings"
	"testing"
)

func result(apiVersion string, errs ...string) map[string]any {
	var es []any
	for _, e := range errs {
		es = append(es, map[string]any{"text": e})
	}
	return map[string]any{
		"apiVersion": apiVersion,
		"kind":       "Result",
		"metadata": map[string]any{
			"name":   "deployment-defaultweb",
			"labels": map[string]any{"k8sgpts.k8sgpt.ai/name": "scan"},
		},
		"spec": map[string]any{
			"kind": "Deployment", "name": "default/web", "parentObject": "Deployment/web",
			"error": es, "details": "",
		},
	}
}

func TestK8sGPTResultText(t *testing.T) {
	got := SummarizeText("Result", result("core.k8sgpt.ai/v1alpha1", "Deployment has 1 replicas but 0 are available"))
	want := "K8sGPT finding: Deployment default/web\n" +
		"Parent: Deployment/web\n" +
		"Errors:\n" +
		"  - Deployment has 1 replicas but 0 are available\n" +
		"Scan: scan"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestK8sGPTResultHeadline(t *testing.T) {
	obj := result("core.k8sgpt.ai/v1alpha1", "one")
	if h, ok := Headline("Result", obj, "add"); !ok || h.Title != "Deployment default/web: 1 finding" || h.Severity != "warning" {
		t.Errorf("add: %+v %v", h, ok)
	}
	obj = result("core.k8sgpt.ai/v1alpha1", "one", "two")
	if h, _ := Headline("Result", obj, "update"); h.Title != "Deployment default/web: 2 findings" {
		t.Errorf("update: %+v", h)
	}
	if h, _ := Headline("Result", obj, "delete"); h.Title != "Deployment default/web: resolved" || h.Severity != "success" {
		t.Errorf("delete: %+v", h)
	}
}

// Other kinds, and Results of other APIs, keep the default title and the
// generic summary.
func TestHeadlineOnlyForK8sGPTResults(t *testing.T) {
	if _, ok := Headline("Pod", map[string]any{"apiVersion": "v1"}, "add"); ok {
		t.Error("Pod got a headline")
	}
	other := result("example.com/v1", "x")
	if _, ok := Headline("Result", other, "add"); ok {
		t.Error("foreign Result got a headline")
	}
	if got := SummarizeText("Result", other); strings.Contains(got, "K8sGPT") {
		t.Errorf("foreign Result summarized as K8sGPT: %q", got)
	}
}

func TestFingerprint(t *testing.T) {
	a := result("core.k8sgpt.ai/v1alpha1", "one", "two")
	b := result("core.k8sgpt.ai/v1alpha1", "two", "one") // order of errors
	fa, ok := Fingerprint("c1", "Result", a)
	fb, _ := Fingerprint("c1", "Result", b)
	if !ok || len(fa) != 16 || fa != fb {
		t.Errorf("fingerprint %q %q ok %v", fa, fb, ok)
	}
	if fc, _ := Fingerprint("c2", "Result", a); fc == fa {
		t.Error("other cluster, same fingerprint")
	}
	if fd, _ := Fingerprint("c1", "Result", result("core.k8sgpt.ai/v1alpha1", "one", "three")); fd == fa {
		t.Error("other error text, same fingerprint")
	}
	if _, ok := Fingerprint("c1", "Pod", map[string]any{"apiVersion": "v1"}); ok {
		t.Error("fingerprint for a Pod")
	}
}

func TestSnapshotHeader(t *testing.T) {
	r := result("core.k8sgpt.ai/v1alpha1", "Deployment has 1 replicas but 0 are available")
	title, text := SnapshotHeader("labda", "results", []map[string]any{r, r})
	if title != "K8sGPT snapshot labda: 2 findings" || !strings.Contains(text, "Deployment default/web: Deployment has 1 replicas") {
		t.Errorf("title %q text %q", title, text)
	}
	if title, _ := SnapshotHeader("labda", "results", []map[string]any{r}); title != "K8sGPT snapshot labda: 1 finding" {
		t.Errorf("singular: %q", title)
	}
	if title, text := SnapshotHeader("labda", "results", nil); title != "K8sGPT snapshot labda: 0 findings" || text != "No open findings." {
		t.Errorf("empty: %q %q", title, text)
	}
	pod := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "p", "namespace": "ns"}}
	if title, text := SnapshotHeader("labda", "pods", []map[string]any{pod}); title != "pods snapshot labda: 1 objects" || text != "ns/p" {
		t.Errorf("generic: %q %q", title, text)
	}
}
