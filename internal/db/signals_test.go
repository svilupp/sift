package db

import "testing"

func TestRefreshBacklinkCounts(t *testing.T) {
	d := openTestDB(t)

	col, err := d.AddCollection("links", "/links", nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}
	sourceID, err := d.UpsertFile("/links/source.md", col.ID, "h1", 1, 100, "")
	if err != nil {
		t.Fatalf("UpsertFile source: %v", err)
	}
	if _, err := d.UpsertFile("/links/target-a.md", col.ID, "h2", 1, 100, ""); err != nil {
		t.Fatalf("UpsertFile target-a: %v", err)
	}
	if _, err := d.UpsertFile("/links/target-b.md", col.ID, "h3", 1, 100, ""); err != nil {
		t.Fatalf("UpsertFile target-b: %v", err)
	}

	if err := d.InsertLink(sourceID, "intro", 1, "/links/target-a.md", "", "markdown", "[a](target-a.md)"); err != nil {
		t.Fatalf("InsertLink 1: %v", err)
	}
	if err := d.InsertLink(sourceID, "intro", 2, "/links/target-a.md", "details", "markdown", "[a](target-a.md#details)"); err != nil {
		t.Fatalf("InsertLink 2: %v", err)
	}
	if err := d.InsertLink(sourceID, "intro", 3, "/links/target-b.md", "", "markdown", "[b](target-b.md)"); err != nil {
		t.Fatalf("InsertLink 3: %v", err)
	}
	if err := d.InsertLink(sourceID, "intro", 4, "/links/source.md", "intro", "markdown", "[self](#intro)"); err != nil {
		t.Fatalf("InsertLink self: %v", err)
	}

	updated, err := d.RefreshBacklinkCounts()
	if err != nil {
		t.Fatalf("RefreshBacklinkCounts: %v", err)
	}
	if updated != 2 {
		t.Fatalf("RefreshBacklinkCounts updated %d rows, want 2", updated)
	}

	counts, err := d.GetBacklinkCounts()
	if err != nil {
		t.Fatalf("GetBacklinkCounts: %v", err)
	}
	if counts["/links/target-a.md"] != 2 {
		t.Fatalf("target-a backlinks = %d, want 2", counts["/links/target-a.md"])
	}
	if counts["/links/target-b.md"] != 1 {
		t.Fatalf("target-b backlinks = %d, want 1", counts["/links/target-b.md"])
	}
	if counts["/links/source.md"] != 0 {
		t.Fatalf("source self backlinks = %d, want 0", counts["/links/source.md"])
	}
}

func TestReplaceReadCounts(t *testing.T) {
	d := openTestDB(t)

	err := d.ReplaceReadCounts([]ReadCountRecord{
		{DocPath: "/vault/a.md", TotalReads: 12, UniqueDays: 5, LastRead: "2026-03-17"},
		{DocPath: "/vault/b.md", TotalReads: 3, UniqueDays: 2, LastRead: "2026-03-16"},
	})
	if err != nil {
		t.Fatalf("ReplaceReadCounts: %v", err)
	}

	counts, err := d.GetReadCounts()
	if err != nil {
		t.Fatalf("GetReadCounts: %v", err)
	}
	if len(counts) != 2 {
		t.Fatalf("GetReadCounts returned %d records, want 2", len(counts))
	}
	if counts["/vault/a.md"].TotalReads != 12 {
		t.Fatalf("a.md total reads = %d, want 12", counts["/vault/a.md"].TotalReads)
	}

	err = d.ReplaceReadCounts([]ReadCountRecord{
		{DocPath: "/vault/b.md", TotalReads: 9, UniqueDays: 4, LastRead: "2026-03-17"},
	})
	if err != nil {
		t.Fatalf("second ReplaceReadCounts: %v", err)
	}

	counts, err = d.GetReadCounts()
	if err != nil {
		t.Fatalf("GetReadCounts after replace: %v", err)
	}
	if len(counts) != 1 {
		t.Fatalf("expected replacement to leave 1 record, got %d", len(counts))
	}
	if counts["/vault/b.md"].TotalReads != 9 {
		t.Fatalf("b.md total reads = %d, want 9", counts["/vault/b.md"].TotalReads)
	}
}

func TestGetSectionsByFileAndHeading(t *testing.T) {
	d := openTestDB(t)

	col, err := d.AddCollection("vault", "/vault", nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}

	fileA, err := d.UpsertFile("/vault/a.md", col.ID, "ha", 1, 100, "")
	if err != nil {
		t.Fatalf("UpsertFile a: %v", err)
	}
	parentID, err := d.InsertChunkV2(fileA, 0, 1, 10, 100, "overview", "Overview", 2, "hash-a1", nil)
	if err != nil {
		t.Fatalf("InsertChunkV2 overview: %v", err)
	}
	if _, err := d.InsertChunkV2(fileA, 1, 11, 20, 80, "overview::2", "Overview", 2, "hash-a2", nil); err != nil {
		t.Fatalf("InsertChunkV2 overview part 2: %v", err)
	}
	if _, err := d.InsertChunkV2(fileA, 2, 21, 30, 60, "details", "Details", 3, "hash-a3", &parentID); err != nil {
		t.Fatalf("InsertChunkV2 details: %v", err)
	}

	fileB, err := d.UpsertFile("/vault/b.md", col.ID, "hb", 1, 100, "")
	if err != nil {
		t.Fatalf("UpsertFile b: %v", err)
	}
	if _, err := d.InsertChunkV2(fileB, 0, 1, 8, 90, "overview", "Overview", 2, "hash-b1", nil); err != nil {
		t.Fatalf("InsertChunkV2 b overview: %v", err)
	}

	sections, err := d.GetSectionsByFile(fileA)
	if err != nil {
		t.Fatalf("GetSectionsByFile: %v", err)
	}
	if len(sections) != 2 {
		t.Fatalf("GetSectionsByFile returned %d sections, want 2", len(sections))
	}
	if sections[0].SectionID != "overview" {
		t.Fatalf("first section = %q, want overview", sections[0].SectionID)
	}
	if sections[0].CharCount != 180 {
		t.Fatalf("overview char count = %d, want 180", sections[0].CharCount)
	}
	if sections[0].SubsectionCount != 1 {
		t.Fatalf("overview subsection count = %d, want 1", sections[0].SubsectionCount)
	}
	if sections[1].ParentSectionID != "overview" {
		t.Fatalf("details parent section = %q, want overview", sections[1].ParentSectionID)
	}

	related, err := d.GetSectionsByHeading(col.ID, "Overview")
	if err != nil {
		t.Fatalf("GetSectionsByHeading: %v", err)
	}
	if len(related) != 2 {
		t.Fatalf("GetSectionsByHeading returned %d sections, want 2", len(related))
	}
}
