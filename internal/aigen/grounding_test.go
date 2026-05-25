package aigen

import (
	"strings"
	"testing"
)

func TestCheckPurposeGrounding(t *testing.T) {
	t.Run("all proper nouns grounded", func(t *testing.T) {
		fr := FolderResult{Purpose: "Tracks Acme and BetaCo integrations."}
		files := []FileBatch{
			{Path: "acme.md", HeadWords: "acme partner notes"},
			{Path: "betaco.md", HeadWords: "BetaCo webhook details"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		if len(w) != 0 {
			t.Errorf("expected 0 warnings, got %v", w)
		}
	})

	t.Run("Q1 2026 hallucination", func(t *testing.T) {
		fr := FolderResult{Purpose: "Tracks weekly timesheets and a Q1 2026 summary."}
		files := []FileBatch{
			{Path: "week1.md", HeadWords: "hours logged for client"},
			{Path: "week2.md", HeadWords: "more hours and notes"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		// Both "Q1" and "2026" should fire.
		joined := strings.Join(w, "\n")
		if !strings.Contains(joined, "Q1") {
			t.Errorf("expected Q1 warning, got: %v", w)
		}
		if !strings.Contains(joined, "2026") {
			t.Errorf("expected 2026 warning, got: %v", w)
		}
	})

	t.Run("sentence start The not flagged", func(t *testing.T) {
		fr := FolderResult{Purpose: "The folder holds notes. The notes are local."}
		files := []FileBatch{
			{Path: "a.md", HeadWords: "any content"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		if len(w) != 0 {
			t.Errorf("expected 0 warnings (The is sentence-start/common), got %v", w)
		}
	})

	t.Run("no proper nouns no warnings", func(t *testing.T) {
		fr := FolderResult{Purpose: "holds working notes for the project."}
		files := []FileBatch{
			{Path: "a.md", HeadWords: "stuff"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		if len(w) != 0 {
			t.Errorf("expected 0 warnings, got %v", w)
		}
	})

	t.Run("case insensitive match", func(t *testing.T) {
		fr := FolderResult{Purpose: "Tracks Acme operations."}
		files := []FileBatch{
			// File content has lowercase "acme"; purpose has "Acme".
			{Path: "ops.md", HeadWords: "acme partner ops"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		if len(w) != 0 {
			t.Errorf("expected 0 warnings (case-insensitive), got %v", w)
		}
	})

	t.Run("path or frontmatter title counts", func(t *testing.T) {
		fr := FolderResult{Purpose: "Houses LEGO design docs."}
		files := []FileBatch{
			{Path: "lego-set.md", HeadWords: "rough draft"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		if len(w) != 0 {
			t.Errorf("path lego-set.md should ground LEGO; got %v", w)
		}
	})

	t.Run("frontmatter tags ground tokens", func(t *testing.T) {
		fr := FolderResult{Purpose: "Captures Alpha project notes."}
		files := []FileBatch{
			{Path: "a.md", HeadWords: "stuff", FrontmatterTags: []string{"alpha"}},
		}
		w := CheckPurposeGrounding(fr, files, "")
		if len(w) != 0 {
			t.Errorf("frontmatter tag alpha should ground Alpha; got %v", w)
		}
	})

	t.Run("empty purpose no warnings", func(t *testing.T) {
		fr := FolderResult{Purpose: ""}
		w := CheckPurposeGrounding(fr, nil, "")
		if len(w) != 0 {
			t.Errorf("expected 0 warnings, got %v", w)
		}
	})

	t.Run("dedupes repeated tokens", func(t *testing.T) {
		fr := FolderResult{Purpose: "Foobar integration. Foobar uses Foobar config."}
		files := []FileBatch{{Path: "a.md", HeadWords: "no relevant content"}}
		w := CheckPurposeGrounding(fr, files, "")
		// Foobar should appear once even though purpose mentions it twice.
		count := 0
		for _, msg := range w {
			if strings.Contains(msg, "Foobar") {
				count++
			}
		}
		if count != 1 {
			t.Errorf("expected exactly 1 Foobar warning, got %d (%v)", count, w)
		}
	})

	// --- False-positive regressions from prior pilot runs ---

	t.Run("apostrophe-s possessive: Smith's grounded by Smith", func(t *testing.T) {
		fr := FolderResult{Purpose: "Captures Smith's onboarding notes."}
		files := []FileBatch{
			{Path: "a.md", HeadWords: "Smith works on the platform"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		if len(w) != 0 {
			t.Errorf("Smith's should be grounded by Smith in source; got %v", w)
		}
	})

	t.Run("apostrophe-s possessive: Acme's, Platform's", func(t *testing.T) {
		fr := FolderResult{Purpose: "Tracks Acme's and Platform's roadmap."}
		files := []FileBatch{
			{Path: "a.md", HeadWords: "acme engineering and platform vertical"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		if len(w) != 0 {
			t.Errorf("Acme's / Platform's should be grounded; got %v", w)
		}
	})

	t.Run("apostrophe-s curly: Cypress’s", func(t *testing.T) {
		fr := FolderResult{Purpose: "Documents Cypress’s quarterly results."}
		files := []FileBatch{
			{Path: "a.md", HeadWords: "Cypress project notes"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		if len(w) != 0 {
			t.Errorf("curly Cypress’s should be grounded by Cypress; got %v", w)
		}
	})

	t.Run("hyphen compound: AI-generated grounded by parts", func(t *testing.T) {
		fr := FolderResult{Purpose: "Captures AI-generated briefs and Notion-sourced extracts."}
		files := []FileBatch{
			{Path: "a.md", HeadWords: "AI workflow and Notion exports for briefs"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		if len(w) != 0 {
			t.Errorf("AI-generated and Notion-sourced should be grounded by parts; got %v", w)
		}
	})

	t.Run("hyphen compound: DO-based, URL-to-config", func(t *testing.T) {
		fr := FolderResult{Purpose: "DO-based runtime, URL-to-config flow, 12-slide deck."}
		files := []FileBatch{
			{Path: "a.md", HeadWords: "Cloudflare DO runtime, URL config flow, slide deck content"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		if len(w) != 0 {
			t.Errorf("DO-based, URL-to-config, 12-slide should be grounded by parts; got %v", w)
		}
	})

	t.Run("em-dash compound: Platform—analytics", func(t *testing.T) {
		fr := FolderResult{Purpose: "Documents Platform—analytics integration."}
		files := []FileBatch{
			{Path: "a.md", HeadWords: "platform vertical and analytics dashboards"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		if len(w) != 0 {
			t.Errorf("Platform—analytics should be grounded by parts; got %v", w)
		}
	})

	t.Run("en-dash compound: Q3–Q4, 2025–Apr", func(t *testing.T) {
		fr := FolderResult{Purpose: "Covers Q3–Q4 admin and 2025–Apr review."}
		files := []FileBatch{
			{Path: "a.md", HeadWords: "Q3 plans, Q4 retrospective, 2025 budget, Apr review notes"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		if len(w) != 0 {
			t.Errorf("Q3–Q4 and 2025–Apr should be grounded by parts; got %v", w)
		}
	})

	t.Run("slash-joined enum: Invoice/Receipt", func(t *testing.T) {
		fr := FolderResult{Purpose: "Tracks Invoice/Receipt paperwork."}
		files := []FileBatch{
			{Path: "a.md", HeadWords: "invoice records and receipt scans"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		if len(w) != 0 {
			t.Errorf("Invoice/Receipt should be grounded by parts; got %v", w)
		}
	})

	t.Run("slash-joined enum: Developer/Documents/Library", func(t *testing.T) {
		fr := FolderResult{Purpose: "Holds Developer/Documents/Library scratch space."}
		files := []FileBatch{
			{Path: "a.md", HeadWords: "Developer machine, Documents directory, Library cache"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		if len(w) != 0 {
			t.Errorf("Developer/Documents/Library should be grounded by parts; got %v", w)
		}
	})

	t.Run("folder basename grounds 2026", func(t *testing.T) {
		// Real example: docs/design-workshop-2026/ali — purpose
		// mentions 2026, but the year only appears in the folder
		// name. Folder basename should ground it.
		fr := FolderResult{Purpose: "Holds 2026 workshop notes for ali."}
		files := []FileBatch{
			{Path: "draft.md", HeadWords: "rough sketches and ideas"},
		}
		w := CheckPurposeGrounding(fr, files, "/tmp/docs/design-workshop-2026/ali")
		if len(w) != 0 {
			t.Errorf("2026 should be grounded by ancestor folder name; got %v", w)
		}
	})

	t.Run("ancestor folder basename grounds token", func(t *testing.T) {
		fr := FolderResult{Purpose: "Notes on BetaCo integration."}
		files := []FileBatch{
			{Path: "ideas.md", HeadWords: "rough sketches"},
		}
		w := CheckPurposeGrounding(fr, files, "/tmp/projects/betaco-sunsetting/notes")
		if len(w) != 0 {
			t.Errorf("BetaCo should be grounded by ancestor folder; got %v", w)
		}
	})

	t.Run("genuine hallucination still fires", func(t *testing.T) {
		// Pure invention — token does not appear anywhere.
		fr := FolderResult{Purpose: "Uses QuasarFramework and Voyager-2 protocols."}
		files := []FileBatch{
			{Path: "a.md", HeadWords: "general notes about web tech"},
		}
		w := CheckPurposeGrounding(fr, files, "/tmp/docs/notes")
		joined := strings.Join(w, "\n")
		if !strings.Contains(joined, "QuasarFramework") {
			t.Errorf("expected QuasarFramework warning (pure invention); got %v", w)
		}
	})

	t.Run("genuine hallucinated compound part still fires", func(t *testing.T) {
		// Compound where the OTHER part is grounded but one piece is
		// invented — invented piece should still fire.
		fr := FolderResult{Purpose: "Tracks BetaCo-QuasarDB integration."}
		files := []FileBatch{
			{Path: "a.md", HeadWords: "BetaCo notes"},
		}
		w := CheckPurposeGrounding(fr, files, "")
		joined := strings.Join(w, "\n")
		if !strings.Contains(joined, "QuasarDB") {
			t.Errorf("expected QuasarDB warning even though BetaCo is grounded; got %v", w)
		}
		if strings.Contains(joined, "BetaCo") {
			t.Errorf("BetaCo should NOT warn (grounded); got %v", w)
		}
	})
}
