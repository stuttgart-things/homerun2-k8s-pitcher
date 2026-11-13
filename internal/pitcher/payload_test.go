package pitcher

import "testing"

func TestPayloadOf(t *testing.T) {
	obj := map[string]any{
		"kind": "Pod",
		"metadata": map[string]any{
			"name":          "p",
			"managedFields": []any{map[string]any{"manager": "kubectl"}},
			"annotations": map[string]any{
				LastAppliedAnnotation: "{...}",
				"keep":      "me",
			},
		},
	}
	got := PayloadOf(obj)

	meta := got["metadata"].(map[string]any)
	if _, ok := meta["managedFields"]; ok {
		t.Error("managedFields not stripped")
	}
	ann := meta["annotations"].(map[string]any)
	if _, ok := ann[LastAppliedAnnotation]; ok || ann["keep"] != "me" {
		t.Errorf("annotations = %v", ann)
	}
	if meta["name"] != "p" || got["kind"] != "Pod" {
		t.Errorf("payload lost fields: %v", got)
	}

	// The informer's object (and its cache) must stay untouched.
	orig := obj["metadata"].(map[string]any)
	if _, ok := orig["managedFields"]; !ok {
		t.Error("payloadOf modified the object's metadata")
	}
	if _, ok := orig["annotations"].(map[string]any)[LastAppliedAnnotation]; !ok {
		t.Error("payloadOf modified the object's annotations")
	}

	// Only the last-applied annotation: no empty annotations map remains.
	only := PayloadOf(map[string]any{"metadata": map[string]any{"annotations": map[string]any{LastAppliedAnnotation: "x"}}})
	if _, ok := only["metadata"].(map[string]any)["annotations"]; ok {
		t.Error("empty annotations kept")
	}
}
