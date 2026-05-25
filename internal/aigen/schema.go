package aigen

// FolderResponseSchema is the JSON-Schema enforced by DeepInfra
// `response_format=json_schema` (strict mode). Mirrors LT-A's verified
// shape from PROPOSAL2.md exactly.
//
// Returned as map[string]any so json.Marshal preserves the document
// shape DeepInfra expects without us hand-rolling raw bytes.
func FolderResponseSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"purpose", "use_when", "files"},
		"properties": map[string]any{
			"purpose": map[string]any{
				"type": "string",
			},
			"use_when": map[string]any{
				"type":     "array",
				"items":    map[string]any{"type": "string"},
				"minItems": 1,
			},
			"files": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"path", "summary"},
					"properties": map[string]any{
						"path":    map[string]any{"type": "string"},
						"summary": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
}

// FolderResponseFormat assembles the strict-mode `response_format`
// payload sent to DeepInfra. Wrapping happens here so callers can swap
// the schema document without re-implementing the envelope.
func FolderResponseFormat() map[string]any {
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "folder_summary",
			"strict": true,
			"schema": FolderResponseSchema(),
		},
	}
}
