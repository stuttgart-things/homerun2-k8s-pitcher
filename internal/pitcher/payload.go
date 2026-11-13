package pitcher

// PayloadOf returns obj without the fields that only bloat a payload: the
// server-side apply bookkeeping in metadata.managedFields and the client-side
// copy in the last-applied-configuration annotation. obj is not modified.
func PayloadOf(obj map[string]any) map[string]any {
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		out[k] = v
	}
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		return out
	}
	m := make(map[string]any, len(meta))
	for k, v := range meta {
		if k != "managedFields" {
			m[k] = v
		}
	}
	if ann, ok := meta["annotations"].(map[string]any); ok {
		if _, has := ann[LastAppliedAnnotation]; has {
			a := make(map[string]any, len(ann))
			for k, v := range ann {
				if k != LastAppliedAnnotation {
					a[k] = v
				}
			}
			if len(a) > 0 {
				m["annotations"] = a
			} else {
				delete(m, "annotations")
			}
		}
	}
	out["metadata"] = m
	return out
}

// LastAppliedAnnotation is kubectl's client-side copy of an applied object.
const LastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"
