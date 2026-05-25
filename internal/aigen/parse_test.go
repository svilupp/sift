package aigen

import (
	"strings"
	"testing"
)

func TestParseFolderResponseRaw(t *testing.T) {
	raw := `{"purpose":"P","files":[{"path":"a.md","summary":"S"}]}`
	fr, err := ParseFolderResponse(raw)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if fr.Purpose != "P" || len(fr.Files) != 1 || fr.Files[0].Path != "a.md" {
		t.Fatalf("unexpected: %+v", fr)
	}
}

func TestParseFolderResponseFenced(t *testing.T) {
	raw := "Here you go:\n```json\n" +
		`{"purpose":"X","files":[{"path":"q.md","summary":"y"}]}` + "\n```\n"
	fr, err := ParseFolderResponse(raw)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if fr.Purpose != "X" {
		t.Fatalf("got %q", fr.Purpose)
	}
}

func TestParseFolderResponseFirstBrace(t *testing.T) {
	raw := `preamble {"purpose":"P","files":[{"path":"a","summary":"s"}]} trailing junk`
	fr, err := ParseFolderResponse(raw)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if fr.Purpose != "P" {
		t.Fatalf("got %q", fr.Purpose)
	}
}

func TestParseFolderResponseEmptyBody(t *testing.T) {
	_, err := ParseFolderResponse("   ")
	if err == nil || !strings.Contains(err.Error(), "empty body") {
		t.Fatalf("expected empty body error; got %v", err)
	}
}

func TestParseFolderResponseEmptySummary(t *testing.T) {
	raw := `{"purpose":"P","files":[{"path":"a","summary":""}]}`
	if _, err := ParseFolderResponse(raw); err == nil {
		t.Fatal("expected error for empty summary")
	}
}

func TestValidatePathSetOK(t *testing.T) {
	fr := &FolderResult{Files: []FileSummary{{Path: "a", Summary: "s"}, {Path: "b", Summary: "s"}}}
	if err := ValidatePathSet([]string{"a", "b"}, fr); err != nil {
		t.Fatalf("err: %v", err)
	}
}

func TestValidatePathSetMissing(t *testing.T) {
	fr := &FolderResult{Files: []FileSummary{{Path: "a", Summary: "s"}}}
	err := ValidatePathSet([]string{"a", "b"}, fr)
	if err == nil || !strings.Contains(err.Error(), "missing=[b]") {
		t.Fatalf("expected missing=[b]; got %v", err)
	}
}

func TestValidatePathSetUnknown(t *testing.T) {
	fr := &FolderResult{Files: []FileSummary{{Path: "a", Summary: "s"}, {Path: "z", Summary: "s"}}}
	err := ValidatePathSet([]string{"a"}, fr)
	if err == nil || !strings.Contains(err.Error(), "unknown=[z]") {
		t.Fatalf("expected unknown=[z]; got %v", err)
	}
}

func TestValidatePathSetDuplicates(t *testing.T) {
	fr := &FolderResult{Files: []FileSummary{{Path: "a", Summary: "s"}, {Path: "a", Summary: "s"}}}
	err := ValidatePathSet([]string{"a"}, fr)
	if err == nil || !strings.Contains(err.Error(), "duplicates=[a]") {
		t.Fatalf("expected duplicates=[a]; got %v", err)
	}
}

func TestValidateUseWhenAccepts(t *testing.T) {
	fr := &FolderResult{UseWhen: []string{"finding RFCs"}}
	if err := ValidateUseWhen(fr); err != nil {
		t.Fatalf("expected nil; got %v", err)
	}
	fr2 := &FolderResult{UseWhen: []string{"  ", "real cue"}}
	if err := ValidateUseWhen(fr2); err != nil {
		t.Fatalf("expected nil for one non-empty cue; got %v", err)
	}
}

func TestValidateUseWhenRejectsMissing(t *testing.T) {
	fr := &FolderResult{}
	err := ValidateUseWhen(fr)
	if err == nil || !strings.Contains(err.Error(), "use_when") {
		t.Fatalf("expected use_when error; got %v", err)
	}
}

func TestValidateUseWhenRejectsEmptyArray(t *testing.T) {
	fr := &FolderResult{UseWhen: []string{}}
	if err := ValidateUseWhen(fr); err == nil {
		t.Fatal("expected error for empty array")
	}
}

func TestValidateUseWhenRejectsAllWhitespace(t *testing.T) {
	fr := &FolderResult{UseWhen: []string{" ", "\t", ""}}
	if err := ValidateUseWhen(fr); err == nil {
		t.Fatal("expected error for whitespace-only entries")
	}
}

func TestValidateUseWhenNil(t *testing.T) {
	if err := ValidateUseWhen(nil); err == nil {
		t.Fatal("expected error for nil")
	}
}

func TestFolderResponseSchemaShape(t *testing.T) {
	s := FolderResponseSchema()
	if s["additionalProperties"] != false {
		t.Errorf("expected additionalProperties=false")
	}
	req := s["required"].([]string)
	if len(req) != 3 || req[0] != "purpose" || req[1] != "use_when" || req[2] != "files" {
		t.Errorf("unexpected required: %v", req)
	}
	uw := s["properties"].(map[string]any)["use_when"].(map[string]any)
	if uw["minItems"] != 1 {
		t.Errorf("expected use_when.minItems=1; got %v", uw["minItems"])
	}
	rf := FolderResponseFormat()
	if rf["type"] != "json_schema" {
		t.Errorf("expected json_schema; got %v", rf["type"])
	}
}
