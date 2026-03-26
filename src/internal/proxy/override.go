package proxy

// DeepMerge merges src into dst in-place.
// Rules:
//   - If a key exists in src but not dst: add it.
//   - If a key exists in both and both values are map[string]any: recurse.
//   - Otherwise: src value overwrites dst value.
func DeepMerge(dst, src map[string]any) {
	for k, srcVal := range src {
		dstVal, exists := dst[k]
		if exists {
			srcMap, srcIsMap := srcVal.(map[string]any)
			dstMap, dstIsMap := dstVal.(map[string]any)
			if srcIsMap && dstIsMap {
				DeepMerge(dstMap, srcMap)
				continue
			}
		}
		dst[k] = srcVal
	}
}

// ApplyOverride applies node.Override onto the request body map.
// If node.Override is empty, returns body unchanged.
// The original map is modified in-place; caller should pass a clone if needed.
func ApplyOverride(body map[string]any, override map[string]any) map[string]any {
	if len(override) == 0 {
		return body
	}
	DeepMerge(body, override)
	return body
}
