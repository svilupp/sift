package ref

import (
	"strconv"
	"strings"
)

const (
	defaultTokenLength = 3
	defaultWindow      = 120
	defaultEndSlack    = 40
)

type RefKind string

const (
	RefKindCodeLines RefKind = "code_lines"
	RefKindDocAnchor RefKind = "doc_anchor"
)

type CodeRef struct {
	Raw          string
	SourcePath   string
	SourceLine   int
	RawPath      string
	ResolvedPath string
	StartLine    int
	EndLine      int
	HasEnd       bool
	StartToken   string
	EndToken     string
	Kind         RefKind
}

func (r CodeRef) IsRange() bool {
	return r.HasEnd
}

func (r CodeRef) HasAnyToken() bool {
	return r.StartToken != "" || r.EndToken != ""
}

func (r CodeRef) Canonical() string {
	if r.RawPath == "" {
		return r.Raw
	}

	var b strings.Builder
	b.WriteString(r.RawPath)
	b.WriteByte(':')
	b.WriteString(strconv.Itoa(r.StartLine))
	if r.StartToken != "" {
		b.WriteByte('@')
		b.WriteString(strings.ToLower(r.StartToken))
	}
	if r.HasEnd {
		b.WriteByte('-')
		b.WriteString(strconv.Itoa(r.EndLine))
		if r.EndToken != "" {
			b.WriteByte('@')
			b.WriteString(strings.ToLower(r.EndToken))
		}
	}
	return b.String()
}

type ValidationStatus string

const (
	StatusValidExact     ValidationStatus = "valid_exact"
	StatusValidShifted   ValidationStatus = "valid_shifted"
	StatusValidUnchecked ValidationStatus = "valid_unchecked"
	StatusStale          ValidationStatus = "stale"
	StatusStaleAmbiguous ValidationStatus = "stale_ambiguous"
	StatusMissingFile    ValidationStatus = "missing_file"
	StatusOutOfBounds    ValidationStatus = "out_of_bounds"
	StatusParseError     ValidationStatus = "parse_error"
	StatusUnresolvedPath ValidationStatus = "unresolved_path"
)

type ValidationResult struct {
	Ref            CodeRef
	Status         ValidationStatus
	Message        string
	SuggestedStart int
	SuggestedEnd   int
	SuggestedRaw   string
	Confidence     float64
}

func (r ValidationResult) NeedsRewrite() bool {
	return r.SuggestedRaw != "" && r.SuggestedRaw != r.Ref.Raw
}

func (r ValidationResult) CanFix() bool {
	if !r.NeedsRewrite() {
		return false
	}
	switch r.Status {
	case StatusValidExact, StatusValidShifted, StatusValidUnchecked:
		return true
	default:
		return false
	}
}

func (r ValidationResult) CanStamp() bool {
	if !r.NeedsRewrite() {
		return false
	}
	switch r.Status {
	case StatusValidExact, StatusValidUnchecked:
		return true
	default:
		return false
	}
}

func (r ValidationResult) IsBroken() bool {
	switch r.Status {
	case StatusValidExact, StatusValidShifted, StatusValidUnchecked:
		return false
	default:
		return true
	}
}

type Match struct {
	Ref       CodeRef
	StartByte int
	EndByte   int
}

type DocumentResult struct {
	SourcePath string
	Results    []ValidationResult
	Content    []byte
	Changed    bool
}

type Resolution struct {
	RawPath      string
	ResolvedPath string
	Exists       bool
}

type IndexedFile struct {
	Path      string
	Lines     []string
	TokenSets map[int][]string
}

type Options struct {
	CollectionRoots []string
	TokenLength     int
	Window          int
	EndSlack        int
}

type Engine struct {
	resolver    *Resolver
	tokenLength int
	window      int
	endSlack    int
	files       map[string]*IndexedFile
	sections    map[string]map[string]struct{}
}
