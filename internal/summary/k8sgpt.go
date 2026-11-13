package summary

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// k8sgptGroup is the API group of the K8sGPT operator's CRs.
const k8sgptGroup = "core.k8sgpt.ai/"

// Header is the title and severity of an event about an object, for kinds
// where the default "<Kind>/<name> <eventType>" says nothing.
type Header struct {
	Title    string
	Severity string
}

// Headline returns the header of an event about obj, or false for kinds that
// keep the default. A K8sGPT Result is a finding about another object: added
// or updated it is a warning titled after that object, deleted it means the
// operator no longer reports it, so it is resolved.
func Headline(kind string, obj map[string]any, eventType string) (Header, bool) {
	if !isK8sGPTResult(kind, obj) {
		return Header{}, false
	}
	subject := resultSubject(obj)
	if eventType == "delete" {
		return Header{Title: subject + ": resolved", Severity: "success"}, true
	}
	n := len(getSlice(obj, "spec", "error"))
	noun := "findings"
	if n == 1 {
		noun = "finding"
	}
	return Header{Title: fmt.Sprintf("%s: %d %s", subject, n, noun), Severity: "warning"}, true
}

func isK8sGPTResult(kind string, obj map[string]any) bool {
	return kind == "Result" && strings.HasPrefix(getStr(obj, "apiVersion"), k8sgptGroup)
}

// resultSubject is the object a Result is about, e.g. "Pod default/web-0".
// The Result's own name is a normalized form of it (pod-defaultweb0).
func resultSubject(obj map[string]any) string {
	kind := getStr(obj, "spec", "kind")
	name := getStr(obj, "spec", "name")
	if name == "" {
		name = getStr(obj, "metadata", "name")
	}
	if kind == "" {
		return name
	}
	return kind + " " + name
}

// textK8sGPTResult lists what K8sGPT found: the object, its parent and every
// error text. Details is the AI explanation and is empty with ai disabled.
func textK8sGPTResult(obj map[string]any) string {
	var b strings.Builder

	fmt.Fprintf(&b, "K8sGPT finding: %s\n", resultSubject(obj))
	if parent := getStr(obj, "spec", "parentObject"); parent != "" {
		fmt.Fprintf(&b, "Parent: %s\n", parent)
	}
	if errs := getSlice(obj, "spec", "error"); len(errs) > 0 {
		b.WriteString("Errors:\n")
		for _, e := range errs {
			if m, ok := e.(map[string]any); ok {
				fmt.Fprintf(&b, "  - %s\n", anyStr(m["text"]))
			}
		}
	}
	if details := getStr(obj, "spec", "details"); details != "" {
		fmt.Fprintf(&b, "Details: %s\n", details)
	}
	if scan := getStr(obj, "metadata", "labels", "k8sgpts.k8sgpt.ai/name"); scan != "" {
		fmt.Fprintf(&b, "Scan: %s\n", scan)
	}

	return strings.TrimSpace(b.String())
}

// Fingerprint identifies a K8sGPT finding across events, snapshots and
// clusters (#103): a short hash of the cluster, the object the Result is
// about and its sorted error texts. The same problem on the same object keeps
// its fingerprint across scans; a changed error text makes a new one. False
// for anything but a K8sGPT Result.
func Fingerprint(cluster, kind string, obj map[string]any) (string, bool) {
	if !isK8sGPTResult(kind, obj) {
		return "", false
	}
	var texts []string
	for _, e := range getSlice(obj, "spec", "error") {
		if m, ok := e.(map[string]any); ok {
			texts = append(texts, anyStr(m["text"]))
		}
	}
	sort.Strings(texts)
	h := sha256.New()
	for _, p := range append([]string{cluster, getStr(obj, "spec", "kind"), getStr(obj, "spec", "name")}, texts...) {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16], true
}

// IsK8sGPTResult reports whether obj is a K8sGPT Result.
func IsK8sGPTResult(kind string, obj map[string]any) bool { return isK8sGPTResult(kind, obj) }

// SnapshotHeader is title and summary of a snapshot of objects (#103):
// "K8sGPT snapshot platform-sthings: 2 findings" with one line per finding,
// or "<resource> snapshot <cluster>: N objects" for any other kind.
func SnapshotHeader(cluster, resource string, items []map[string]any) (title, text string) {
	k8sgpt := len(items) > 0 && isK8sGPTResult(getStr(items[0], "kind"), items[0])
	if resource == "results" && len(items) == 0 {
		k8sgpt = true
	}
	var b strings.Builder
	const maxLines = 50
	for i, it := range items {
		if i == maxLines {
			fmt.Fprintf(&b, "... and %d more\n", len(items)-maxLines)
			break
		}
		if k8sgpt {
			first := ""
			if errs := getSlice(it, "spec", "error"); len(errs) > 0 {
				if m, ok := errs[0].(map[string]any); ok {
					first = anyStr(m["text"])
				}
			}
			fmt.Fprintf(&b, "%s: %s\n", resultSubject(it), first)
			continue
		}
		name := getStr(it, "metadata", "name")
		if ns := getStr(it, "metadata", "namespace"); ns != "" {
			name = ns + "/" + name
		}
		fmt.Fprintf(&b, "%s\n", name)
	}
	if k8sgpt {
		noun := "findings"
		if len(items) == 1 {
			noun = "finding"
		}
		title = fmt.Sprintf("K8sGPT snapshot %s: %d %s", cluster, len(items), noun)
		if len(items) == 0 {
			text = "No open findings."
		}
	} else {
		title = fmt.Sprintf("%s snapshot %s: %d objects", resource, cluster, len(items))
	}
	if text == "" {
		text = strings.TrimSpace(b.String())
	}
	return title, text
}
