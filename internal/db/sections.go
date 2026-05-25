package db

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// SectionSummary represents an aggregated section across one or more chunks.
type SectionSummary struct {
	FileID          int64
	FilePath        string
	CollectionID    int64
	SectionID       string
	Heading         string
	HeadingLevel    int
	ParentSectionID string
	CharCount       int
	SubsectionCount int
	StartLine       int
	FirstOrder      int
}

type sectionChunkRow struct {
	ChunkRecord
	FilePath     string
	CollectionID int64
}

// GetSectionsByFile returns all sections for a file in document order.
func (db *DB) GetSectionsByFile(fileID int64) ([]SectionSummary, error) {
	rows, err := db.conn.Query(`
		SELECT c.id, c.file_id, c.chunk_order, c.start_line, c.end_line, c.char_count, c.created_at,
		       COALESCE(c.section_id,''), COALESCE(c.heading,''), COALESCE(c.heading_level,0),
		       COALESCE(c.chunk_hash,''), c.parent_chunk_id, f.path, f.collection_id
		FROM chunks c
		JOIN files f ON f.id = c.file_id
		WHERE c.file_id = ?
		ORDER BY c.chunk_order
	`, fileID)
	if err != nil {
		return nil, fmt.Errorf("query sections by file: %w", err)
	}
	defer rows.Close()

	chunks, err := scanSectionChunkRows(rows)
	if err != nil {
		return nil, err
	}
	return summarizeSections(chunks), nil
}

// GetSectionsByHeading returns sections with a matching heading across a collection.
func (db *DB) GetSectionsByHeading(collectionID int64, heading string) ([]SectionSummary, error) {
	heading = strings.TrimSpace(heading)
	if heading == "" {
		return nil, nil
	}

	var (
		rows *sql.Rows
		err  error
	)
	if collectionID > 0 {
		rows, err = db.conn.Query(`
			SELECT c.id, c.file_id, c.chunk_order, c.start_line, c.end_line, c.char_count, c.created_at,
			       COALESCE(c.section_id,''), COALESCE(c.heading,''), COALESCE(c.heading_level,0),
			       COALESCE(c.chunk_hash,''), c.parent_chunk_id, f.path, f.collection_id
			FROM chunks c
			JOIN files f ON f.id = c.file_id
			WHERE EXISTS (
				SELECT 1 FROM file_collections fc
				WHERE fc.file_id = f.id AND fc.collection_id = ?
			) AND LOWER(TRIM(c.heading)) = LOWER(TRIM(?))
			ORDER BY f.path, c.chunk_order
		`, collectionID, heading)
	} else {
		rows, err = db.conn.Query(`
			SELECT c.id, c.file_id, c.chunk_order, c.start_line, c.end_line, c.char_count, c.created_at,
			       COALESCE(c.section_id,''), COALESCE(c.heading,''), COALESCE(c.heading_level,0),
			       COALESCE(c.chunk_hash,''), c.parent_chunk_id, f.path, f.collection_id
			FROM chunks c
			JOIN files f ON f.id = c.file_id
			WHERE LOWER(TRIM(c.heading)) = LOWER(TRIM(?))
			ORDER BY f.path, c.chunk_order
		`, heading)
	}
	if err != nil {
		return nil, fmt.Errorf("query sections by heading: %w", err)
	}
	defer rows.Close()

	chunks, err := scanSectionChunkRows(rows)
	if err != nil {
		return nil, err
	}
	return summarizeSections(chunks), nil
}

func scanSectionChunkRows(rows *sql.Rows) ([]sectionChunkRow, error) {
	var chunks []sectionChunkRow
	for rows.Next() {
		var row sectionChunkRow
		var parentID sql.NullInt64
		if err := rows.Scan(
			&row.ID,
			&row.FileID,
			&row.Order,
			&row.StartLine,
			&row.EndLine,
			&row.CharCount,
			&row.CreatedAt,
			&row.SectionID,
			&row.Heading,
			&row.HeadingLevel,
			&row.ChunkHash,
			&parentID,
			&row.FilePath,
			&row.CollectionID,
		); err != nil {
			return nil, fmt.Errorf("scan section chunk row: %w", err)
		}
		if parentID.Valid {
			v := parentID.Int64
			row.ParentChunkID = &v
		}
		chunks = append(chunks, row)
	}
	return chunks, rows.Err()
}

func summarizeSections(chunks []sectionChunkRow) []SectionSummary {
	type key struct {
		fileID    int64
		sectionID string
	}

	sectionByChunkID := make(map[int64]key, len(chunks))
	summaries := make(map[key]*SectionSummary)
	childSets := make(map[key]map[key]struct{})

	for _, row := range chunks {
		k := key{
			fileID:    row.FileID,
			sectionID: canonicalSectionID(row.SectionID),
		}
		sectionByChunkID[row.ID] = k

		summary := summaries[k]
		if summary == nil {
			summary = &SectionSummary{
				FileID:       row.FileID,
				FilePath:     row.FilePath,
				CollectionID: row.CollectionID,
				SectionID:    k.sectionID,
				Heading:      row.Heading,
				HeadingLevel: row.HeadingLevel,
				CharCount:    row.CharCount,
				StartLine:    row.StartLine,
				FirstOrder:   row.Order,
			}
			summaries[k] = summary
		} else {
			summary.CharCount += row.CharCount
			if row.StartLine < summary.StartLine {
				summary.StartLine = row.StartLine
			}
			if row.Order < summary.FirstOrder {
				summary.FirstOrder = row.Order
			}
			if summary.Heading == "" && row.Heading != "" {
				summary.Heading = row.Heading
				summary.HeadingLevel = row.HeadingLevel
			}
		}
	}

	for _, row := range chunks {
		childKey := sectionByChunkID[row.ID]
		if row.ParentChunkID == nil {
			continue
		}
		parentKey, ok := sectionByChunkID[*row.ParentChunkID]
		if !ok || parentKey == childKey {
			continue
		}
		if summaries[childKey].ParentSectionID == "" {
			summaries[childKey].ParentSectionID = parentKey.sectionID
		}
		if childSets[parentKey] == nil {
			childSets[parentKey] = make(map[key]struct{})
		}
		childSets[parentKey][childKey] = struct{}{}
	}

	result := make([]SectionSummary, 0, len(summaries))
	for k, summary := range summaries {
		summary.SubsectionCount = len(childSets[k])
		result = append(result, *summary)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].FilePath != result[j].FilePath {
			return result[i].FilePath < result[j].FilePath
		}
		if result[i].FirstOrder != result[j].FirstOrder {
			return result[i].FirstOrder < result[j].FirstOrder
		}
		return result[i].SectionID < result[j].SectionID
	})

	return result
}

func canonicalSectionID(sectionID string) string {
	if idx := strings.Index(sectionID, "::"); idx >= 0 {
		return sectionID[:idx]
	}
	return sectionID
}
