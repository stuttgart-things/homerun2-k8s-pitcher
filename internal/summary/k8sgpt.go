package summary

import (
	"fmt"
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
