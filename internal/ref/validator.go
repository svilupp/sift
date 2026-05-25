package ref

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func NewEngine(opts Options) *Engine {
	tokenLength := sanitizeTokenLength(opts.TokenLength)
	window := opts.Window
	if window <= 0 {
		window = defaultWindow
	}
	endSlack := opts.EndSlack
	if endSlack <= 0 {
		endSlack = defaultEndSlack
	}

	return &Engine{
		resolver:    NewResolver(opts.CollectionRoots),
		tokenLength: tokenLength,
		window:      window,
		endSlack:    endSlack,
		files:       make(map[string]*IndexedFile),
	}
}

func (e *Engine) Validate(ref CodeRef) ValidationResult {
	ref.Kind = RefKindCodeLines
	if ref.EndLine == 0 {
		ref.EndLine = ref.StartLine
	}

	result := ValidationResult{Ref: ref}

	resolution := e.resolver.Resolve(ref.SourcePath, ref.RawPath)
	result.Ref.ResolvedPath = resolution.ResolvedPath
	if resolution.ResolvedPath == "" {
		result.Status = StatusUnresolvedPath
		result.Message = "could not resolve target path"
		return result
	}
	if !resolution.Exists {
		result.Status = StatusMissingFile
		result.Message = "target file does not exist"
		return result
	}

	indexed, err := e.indexedFile(resolution.ResolvedPath)
	if err != nil {
		result.Status = StatusMissingFile
		result.Message = fmt.Sprintf("read target file: %v", err)
		return result
	}

	if ref.StartLine < 1 || ref.EndLine < ref.StartLine || ref.StartLine > len(indexed.Lines) || ref.EndLine > len(indexed.Lines) {
		result.Status = StatusOutOfBounds
		result.Message = fmt.Sprintf("line range %d-%d exceeds file length %d", ref.StartLine, ref.EndLine, len(indexed.Lines))
		return result
	}

	if exact, ok := e.validateExact(indexed, ref); ok {
		return exact
	}

	if ref.HasEnd {
		return e.validateRange(indexed, ref)
	}
	return e.validateSingle(indexed, ref)
}

func (e *Engine) validateExact(indexed *IndexedFile, ref CodeRef) (ValidationResult, bool) {
	hasStartToken := ref.StartToken != ""
	hasEndToken := ref.HasEnd && ref.EndToken != ""

	startOK := !hasStartToken || e.lineHasToken(indexed, ref.StartLine, ref.StartToken)
	endOK := !hasEndToken || e.lineHasToken(indexed, ref.EndLine, ref.EndToken)
	if !startOK || !endOK {
		return ValidationResult{}, false
	}

	suggested := e.suggestedRef(indexed, ref, ref.StartLine, ref.EndLine)
	result := ValidationResult{
		Ref:            ref,
		SuggestedStart: suggested.StartLine,
		SuggestedEnd:   suggested.EndLine,
		SuggestedRaw:   suggested.Canonical(),
	}

	if hasStartToken && (!ref.HasEnd || hasEndToken) {
		result.Status = StatusValidExact
		result.Message = "tokens match at stored lines"
		result.Confidence = 1.0
		return result, true
	}

	result.Status = StatusValidUnchecked
	result.Message = "line range exists but is not fully token-validated"
	result.Confidence = 0.6
	return result, true
}

func (e *Engine) validateSingle(indexed *IndexedFile, ref CodeRef) ValidationResult {
	result := ValidationResult{Ref: ref}
	if ref.StartToken == "" {
		result.Status = StatusValidUnchecked
		result.Message = "single-line ref has no validation token"
		result.SuggestedRaw = e.suggestedRef(indexed, ref, ref.StartLine, ref.StartLine).Canonical()
		result.SuggestedStart = ref.StartLine
		result.SuggestedEnd = ref.StartLine
		result.Confidence = 0.6
		return result
	}

	candidates := e.findTokenNear(indexed, ref.StartToken, ref.StartLine, e.window)
	switch len(candidates) {
	case 0:
		result.Status = StatusStale
		result.Message = "token no longer matches nearby lines"
	case 1:
		suggested := e.suggestedRef(indexed, ref, candidates[0], candidates[0])
		result.Status = StatusValidShifted
		result.Message = "unique nearby line match found"
		result.SuggestedStart = candidates[0]
		result.SuggestedEnd = candidates[0]
		result.SuggestedRaw = suggested.Canonical()
		result.Confidence = 0.9
	default:
		result.Status = StatusStaleAmbiguous
		result.Message = "multiple nearby line matches found"
	}
	return result
}

func (e *Engine) validateRange(indexed *IndexedFile, ref CodeRef) ValidationResult {
	result := ValidationResult{Ref: ref}
	offset := ref.EndLine - ref.StartLine

	type pair struct {
		start int
		end   int
	}

	pairs := make([]pair, 0)
	seen := make(map[string]struct{})

	addPair := func(start, end int) {
		if start < 1 || end < start || end > len(indexed.Lines) {
			return
		}
		key := fmt.Sprintf("%d:%d", start, end)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		pairs = append(pairs, pair{start: start, end: end})
	}

	if ref.StartToken != "" {
		startCandidates := e.findTokenNear(indexed, ref.StartToken, ref.StartLine, e.window)
		for _, start := range startCandidates {
			if ref.EndToken != "" {
				expectedEnd := start + offset
				for _, end := range e.findTokenNear(indexed, ref.EndToken, expectedEnd, e.endSlack) {
					addPair(start, end)
				}
			} else {
				addPair(start, start+offset)
			}
		}
	} else if ref.EndToken != "" {
		endCandidates := e.findTokenNear(indexed, ref.EndToken, ref.EndLine, e.window)
		for _, end := range endCandidates {
			addPair(end-offset, end)
		}
	}

	switch len(pairs) {
	case 0:
		result.Status = StatusStale
		result.Message = "range tokens no longer match nearby lines"
		return result
	case 1:
		suggested := e.suggestedRef(indexed, ref, pairs[0].start, pairs[0].end)
		result.Status = StatusValidShifted
		result.Message = "unique nearby range match found"
		result.SuggestedStart = pairs[0].start
		result.SuggestedEnd = pairs[0].end
		result.SuggestedRaw = suggested.Canonical()
		if ref.StartToken != "" && ref.EndToken != "" {
			result.Confidence = 0.9
		} else {
			result.Confidence = 0.75
		}
		return result
	default:
		result.Status = StatusStaleAmbiguous
		result.Message = "multiple nearby range matches found"
		return result
	}
}

func (e *Engine) indexedFile(path string) (*IndexedFile, error) {
	path = filepath.Clean(path)
	if indexed, ok := e.files[path]; ok {
		return indexed, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	indexed := &IndexedFile{
		Path:      path,
		Lines:     strings.Split(string(data), "\n"),
		TokenSets: make(map[int][]string),
	}
	e.files[path] = indexed
	return indexed, nil
}

func (e *Engine) lineHasToken(indexed *IndexedFile, line int, token string) bool {
	if line < 1 || line > len(indexed.Lines) || token == "" {
		return false
	}
	length := len(token)
	return e.tokenAt(indexed, line, length) == strings.ToLower(token)
}

func (e *Engine) tokenAt(indexed *IndexedFile, line, length int) string {
	if line < 1 || line > len(indexed.Lines) {
		return ""
	}
	length = sanitizeTokenLength(length)
	if tokens, ok := indexed.TokenSets[length]; ok {
		return tokens[line-1]
	}

	tokens := make([]string, len(indexed.Lines))
	for i, text := range indexed.Lines {
		tokens[i] = LineToken(text, length)
	}
	indexed.TokenSets[length] = tokens
	return tokens[line-1]
}

func (e *Engine) findTokenNear(indexed *IndexedFile, token string, around, window int) []int {
	if token == "" || len(indexed.Lines) == 0 {
		return nil
	}

	if window < 0 {
		window = 0
	}
	start := around - window
	if start < 1 {
		start = 1
	}
	end := around + window
	if end > len(indexed.Lines) {
		end = len(indexed.Lines)
	}

	length := len(token)
	matches := make([]int, 0, 1)
	for line := start; line <= end; line++ {
		if e.tokenAt(indexed, line, length) == strings.ToLower(token) {
			matches = append(matches, line)
		}
	}
	return matches
}

func (e *Engine) suggestedRef(indexed *IndexedFile, ref CodeRef, start, end int) CodeRef {
	out := ref
	out.StartLine = start
	out.EndLine = end

	length := e.preferredTokenLength(ref)
	if out.StartToken == "" {
		out.StartToken = e.tokenAt(indexed, start, length)
	} else {
		out.StartToken = strings.ToLower(out.StartToken)
	}
	if out.HasEnd {
		if out.EndToken == "" {
			out.EndToken = e.tokenAt(indexed, end, length)
		} else {
			out.EndToken = strings.ToLower(out.EndToken)
		}
	}
	return out
}

func (e *Engine) preferredTokenLength(ref CodeRef) int {
	switch {
	case len(ref.StartToken) > 0:
		return len(ref.StartToken)
	case len(ref.EndToken) > 0:
		return len(ref.EndToken)
	default:
		return e.tokenLength
	}
}
