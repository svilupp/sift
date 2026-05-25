package ref

import (
	"bytes"
	"sort"
)

type replacement struct {
	start int
	end   int
	text  string
}

func (e *Engine) StampDocument(sourcePath string, content []byte) (DocumentResult, error) {
	matches := Extract(sourcePath, content)
	results := make([]ValidationResult, 0, len(matches))
	replacements := make([]replacement, 0, len(matches))

	for _, match := range matches {
		result := e.Validate(match.Ref)
		results = append(results, result)
		if result.CanStamp() {
			replacements = append(replacements, replacement{
				start: match.StartByte,
				end:   match.EndByte,
				text:  result.SuggestedRaw,
			})
		}
	}

	updated := applyReplacements(content, replacements)
	return DocumentResult{
		SourcePath: sourcePath,
		Results:    results,
		Content:    updated,
		Changed:    !bytes.Equal(updated, content),
	}, nil
}

func (e *Engine) ValidateDocument(sourcePath string, content []byte, fix bool) (DocumentResult, error) {
	matches := Extract(sourcePath, content)
	results := make([]ValidationResult, 0, len(matches))
	replacements := make([]replacement, 0, len(matches))

	for _, match := range matches {
		result := e.Validate(match.Ref)
		results = append(results, result)
		if fix && result.CanFix() {
			replacements = append(replacements, replacement{
				start: match.StartByte,
				end:   match.EndByte,
				text:  result.SuggestedRaw,
			})
		}
	}

	updated := content
	if fix {
		updated = applyReplacements(content, replacements)
	}

	return DocumentResult{
		SourcePath: sourcePath,
		Results:    results,
		Content:    updated,
		Changed:    fix && !bytes.Equal(updated, content),
	}, nil
}

func applyReplacements(content []byte, replacements []replacement) []byte {
	if len(replacements) == 0 {
		out := make([]byte, len(content))
		copy(out, content)
		return out
	}

	sort.Slice(replacements, func(i, j int) bool {
		return replacements[i].start < replacements[j].start
	})

	var out bytes.Buffer
	last := 0
	for _, repl := range replacements {
		if repl.start < last {
			continue
		}
		out.Write(content[last:repl.start])
		out.WriteString(repl.text)
		last = repl.end
	}
	out.Write(content[last:])
	return out.Bytes()
}
