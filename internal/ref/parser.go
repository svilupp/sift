package ref

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	colonParseRe  = regexp.MustCompile(`^(?P<path>(?:/)?[A-Za-z0-9_./~\-]+\.[A-Za-z0-9_+\-]+):(?P<start>\d+)(?:@(?P<starttok>[A-Za-z0-9]{2,8}))?(?:-(?P<end>\d+)(?:@(?P<endtok>[A-Za-z0-9]{2,8}))?)?$`)
	githubParseRe = regexp.MustCompile(`^(?P<path>(?:/)?[A-Za-z0-9_./~\-]+\.[A-Za-z0-9_+\-]+)#L(?P<start>\d+)(?:@(?P<starttok>[A-Za-z0-9]{2,8}))?(?:-L(?P<end>\d+)(?:@(?P<endtok>[A-Za-z0-9]{2,8}))?)?$`)

	colonExtractRe  = regexp.MustCompile(`(?:/)?[A-Za-z0-9_./~\-]+\.[A-Za-z0-9_+\-]+:\d+(?:@[A-Za-z0-9]{2,8})?(?:-\d+(?:@[A-Za-z0-9]{2,8})?)?`)
	githubExtractRe = regexp.MustCompile(`(?:/)?[A-Za-z0-9_./~\-]+\.[A-Za-z0-9_+\-]+#L\d+(?:@[A-Za-z0-9]{2,8})?(?:-L\d+(?:@[A-Za-z0-9]{2,8})?)?`)
)

func ParseCodeRef(raw string) (CodeRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CodeRef{}, fmt.Errorf("empty ref")
	}

	if ref, ok, err := parseWithPattern(raw, colonParseRe); ok || err != nil {
		return ref, err
	}
	if ref, ok, err := parseWithPattern(raw, githubParseRe); ok || err != nil {
		return ref, err
	}
	return CodeRef{}, fmt.Errorf("invalid code ref: %q", raw)
}

func parseWithPattern(raw string, re *regexp.Regexp) (CodeRef, bool, error) {
	matches := re.FindStringSubmatch(raw)
	if matches == nil {
		return CodeRef{}, false, nil
	}

	group := func(name string) string {
		idx := re.SubexpIndex(name)
		if idx < 0 || idx >= len(matches) {
			return ""
		}
		return matches[idx]
	}

	startLine, err := strconv.Atoi(group("start"))
	if err != nil {
		return CodeRef{}, true, fmt.Errorf("parse start line: %w", err)
	}
	if startLine < 1 {
		return CodeRef{}, true, fmt.Errorf("start line must be positive")
	}

	ref := CodeRef{
		Raw:        raw,
		RawPath:    group("path"),
		StartLine:  startLine,
		EndLine:    startLine,
		StartToken: strings.ToLower(group("starttok")),
		Kind:       RefKindCodeLines,
	}

	if endRaw := group("end"); endRaw != "" {
		endLine, convErr := strconv.Atoi(endRaw)
		if convErr != nil {
			return CodeRef{}, true, fmt.Errorf("parse end line: %w", convErr)
		}
		if endLine < startLine {
			return CodeRef{}, true, fmt.Errorf("end line %d is before start line %d", endLine, startLine)
		}
		ref.HasEnd = true
		ref.EndLine = endLine
		ref.EndToken = strings.ToLower(group("endtok"))
	}

	return ref, true, nil
}

func Extract(sourcePath string, content []byte) []Match {
	text := string(content)

	type rawMatch struct {
		start int
		end   int
		raw   string
	}

	var all []rawMatch
	for _, re := range []*regexp.Regexp{colonExtractRe, githubExtractRe} {
		for _, loc := range re.FindAllStringIndex(text, -1) {
			all = append(all, rawMatch{
				start: loc[0],
				end:   loc[1],
				raw:   text[loc[0]:loc[1]],
			})
		}
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].start == all[j].start {
			return all[i].end < all[j].end
		}
		return all[i].start < all[j].start
	})

	matches := make([]Match, 0, len(all))
	lastEnd := -1
	for _, found := range all {
		if found.start < lastEnd {
			continue
		}

		ref, err := ParseCodeRef(found.raw)
		if err != nil {
			continue
		}
		ref.SourcePath = sourcePath
		ref.SourceLine = 1 + strings.Count(text[:found.start], "\n")

		matches = append(matches, Match{
			Ref:       ref,
			StartByte: found.start,
			EndByte:   found.end,
		})
		lastEnd = found.end
	}

	return matches
}
