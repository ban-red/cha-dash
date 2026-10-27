package runtime

import (
	"encoding/json"
	"slices"
	"strings"

	"dash.cha.sh/internal/integration"
)

// Secrets live in the vault; an instance's stored config holds a
// ${secret:<vault name>} reference in each secret field (the manifest's
// write-only fields) that's set. References resolve only in those fields,
// so a non-secret field can never pull a secret out of the vault.
//
// Vault names are instance/<instance id>/<field>, the convention the
// Homepage importer uses too. A new value overwrites the entry in place; if
// saving the config then fails, the previous value is put back. A reference
// to any other name still resolves; when the instance stops referencing such
// a name, it's deleted unless another instance still refers to it.

// secretName is the vault name of an instance's secret field.
func secretName(instanceID, field string) string {
	return "instance/" + instanceID + "/" + field
}

// refsOf returns the vault name referenced by each secret field of a stored
// config. With man unknown (nil schema), every top-level reference counts.
func refsOf(stored map[string]any, man integration.Manifest) map[string]string {
	out := map[string]string{}
	for k, v := range stored {
		s, ok := v.(string)
		if !ok {
			continue
		}
		name, ok := integration.ParseSecretRef(s)
		if !ok {
			continue
		}
		if man.ConfigSchema == nil || man.IsSecret(k) {
			out[k] = name
		}
	}
	return out
}

// nonSecret is a stored config without its secret fields (and, for an
// unknown integration, without any reference).
func nonSecret(stored map[string]any, man integration.Manifest) map[string]any {
	out := map[string]any{}
	refs := refsOf(stored, man)
	for k, v := range stored {
		if _, isRef := refs[k]; isRef || man.IsSecret(k) {
			continue
		}
		out[k] = cloneJSON(v)
	}
	return out
}

// clean drops null and empty-string members from objects, recursively, so a
// form that sends "" for an untouched optional field means "not set".
func clean(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			if x == nil || x == "" {
				continue
			}
			out[k] = clean(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = clean(x)
		}
		return out
	}
	return v
}

// mentionsRef reports whether any string in v contains a secret reference.
func mentionsRef(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.Contains(t, "${secret:")
	case map[string]any:
		for _, x := range t {
			if mentionsRef(x) {
				return true
			}
		}
	case []any:
		if slices.ContainsFunc(t, mentionsRef) {
			return true
		}
	}
	return false
}

// cloneJSON deep-copies decoded JSON.
func cloneJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = cloneJSON(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = cloneJSON(x)
		}
		return out
	}
	return v
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return cloneJSON(m).(map[string]any)
}

// sameJSON compares two decoded JSON values.
func sameJSON(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}
