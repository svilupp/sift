package db

import (
	"fmt"
	"time"
)

// BacklinkCountRecord stores backlink statistics for a document.
type BacklinkCountRecord struct {
	DocPath       string
	BacklinkCount int
}

// ReadCountRecord stores read-frequency data for a document.
type ReadCountRecord struct {
	DocPath    string
	TotalReads int
	UniqueDays int
	LastRead   string
}

// RefreshBacklinkCounts rebuilds backlink counts from the links table.
func (db *DB) RefreshBacklinkCounts() (int, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin backlink refresh tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("DELETE FROM backlink_counts"); err != nil {
		return 0, fmt.Errorf("clear backlink counts: %w", err)
	}

	now := time.Now().Unix()
	res, err := tx.Exec(`
		INSERT INTO backlink_counts (doc_path, backlink_count, updated_at)
		SELECT l.target_path, COUNT(*), ?
		FROM links l
		JOIN files f ON f.id = l.source_file_id
		WHERE l.target_path <> ''
		  AND l.target_path <> f.path
		GROUP BY l.target_path
	`, now)
	if err != nil {
		return 0, fmt.Errorf("rebuild backlink counts: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit backlink refresh: %w", err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return int(rowsAffected), nil
}

// GetBacklinkCounts returns backlink counts keyed by absolute document path.
func (db *DB) GetBacklinkCounts() (map[string]int, error) {
	rows, err := db.conn.Query(`
		SELECT doc_path, backlink_count
		FROM backlink_counts
	`)
	if err != nil {
		return nil, fmt.Errorf("query backlink counts: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var docPath string
		var count int
		if err := rows.Scan(&docPath, &count); err != nil {
			return nil, fmt.Errorf("scan backlink count: %w", err)
		}
		counts[docPath] = count
	}
	return counts, rows.Err()
}

// ReplaceReadCounts replaces all stored read counts with the provided data.
func (db *DB) ReplaceReadCounts(counts []ReadCountRecord) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin read count tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("DELETE FROM read_counts"); err != nil {
		return fmt.Errorf("clear read counts: %w", err)
	}
	if err := upsertReadCounts(tx, counts); err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertReadCounts inserts or updates read counts without clearing the table first.
func (db *DB) UpsertReadCounts(counts []ReadCountRecord) error {
	return upsertReadCounts(db.conn, counts)
}

func upsertReadCounts(ex execer, counts []ReadCountRecord) error {
	if len(counts) == 0 {
		return nil
	}

	now := time.Now().Unix()
	for _, count := range counts {
		if _, err := ex.Exec(`
			INSERT INTO read_counts (doc_path, total_reads, unique_days, last_read, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(doc_path) DO UPDATE SET
				total_reads = excluded.total_reads,
				unique_days = excluded.unique_days,
				last_read = excluded.last_read,
				updated_at = excluded.updated_at
		`, count.DocPath, count.TotalReads, count.UniqueDays, count.LastRead, now); err != nil {
			return fmt.Errorf("upsert read count for %s: %w", count.DocPath, err)
		}
	}
	return nil
}

// GetReadCounts returns read counts keyed by absolute document path.
func (db *DB) GetReadCounts() (map[string]ReadCountRecord, error) {
	rows, err := db.conn.Query(`
		SELECT doc_path, total_reads, unique_days, last_read
		FROM read_counts
	`)
	if err != nil {
		return nil, fmt.Errorf("query read counts: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]ReadCountRecord)
	for rows.Next() {
		var record ReadCountRecord
		if err := rows.Scan(&record.DocPath, &record.TotalReads, &record.UniqueDays, &record.LastRead); err != nil {
			return nil, fmt.Errorf("scan read count: %w", err)
		}
		counts[record.DocPath] = record
	}
	return counts, rows.Err()
}
