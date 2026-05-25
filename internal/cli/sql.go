package cli

import (
	"database/sql"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sift/internal/config"
	siftlog "sift/internal/log"

	_ "modernc.org/sqlite"
)

func newSqlCmd() *cobra.Command {
	var (
		limit  int
		schema bool
	)

	cmd := &cobra.Command{
		Use:   "sql <query>",
		Short: "Run a read-only SQL query against the SIFT database",
		Long: `Run a read-only SQL query against the SIFT SQLite database.

WARNING: Internal debugging tool. The schema may change between versions.
Prefer "sift search", "sift config stats", and "sift config health" for
normal operations. Only use this if you know what you're looking for.

Output is tab-aligned with column headers.

Tables:
  collections    (id, name, path, tags, created_at)
  files          (id, path, collection_id, file_hash, mtime, size_bytes, last_indexed, chunk_count, title)
  file_collections (file_id, collection_id)
  chunks         (id, file_id, chunk_order, start_line, end_line, char_count, created_at)
  embeddings     (chunk_id, vector, model, dimensions)
  feedback       (id, search_id, result_index, doc_path, chunk_id, signal, query, created_at)
  dead_letters   (id, operation, file_path, chunk_info, error_message, attempts, resolved_at)
  api_usage      (id, timestamp, operation, request_count, token_count, latency_ms)
  search_sessions (search_id, query, collection_filter, results_json, created_at)

Examples:
  sift sql "SELECT name, path FROM collections"
  sift sql "SELECT COUNT(*) FROM chunks"
  sift sql "SELECT path, chunk_count FROM files ORDER BY mtime DESC LIMIT 10"
  sift sql "SELECT operation, SUM(token_count) FROM api_usage GROUP BY operation"
  sift sql "SELECT signal, COUNT(*) FROM feedback GROUP BY signal"
  sift sql --schema`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !schema && len(args) == 0 {
				return cmd.Help()
			}

			dbPath, err := config.DBPath()
			if err != nil {
				return err
			}

			// Open in read-only mode.
			dsn := "file:" + dbPath + "?mode=ro&_pragma=foreign_keys(ON)"
			conn, err := sql.Open("sqlite", dsn)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer conn.Close()

			if err := conn.Ping(); err != nil {
				return fmt.Errorf("connect to db: %w", err)
			}

			if schema {
				rows, err := conn.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
				if err != nil {
					return fmt.Errorf("list tables: %w", err)
				}
				var tables []string
				for rows.Next() {
					var name string
					if err := rows.Scan(&name); err != nil {
						return fmt.Errorf("scan table: %w", err)
					}
					tables = append(tables, name)
				}
				rows.Close()

				w := cmd.OutOrStdout()
				for _, table := range tables {
					fmt.Fprintf(w, "%s\n", table)
					infoRows, err := conn.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
					if err != nil {
						fmt.Fprintf(w, "  (error: %v)\n", err)
						continue
					}
					for infoRows.Next() {
						var cid int
						var name, colType string
						var notNull, pk int
						var dflt sql.NullString
						if err := infoRows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
							continue
						}
						pkTag := ""
						if pk > 0 {
							pkTag = " PK"
						}
						fmt.Fprintf(w, "  %-20s %-10s%s\n", name, colType, pkTag)
					}
					infoRows.Close()
					fmt.Fprintln(w)
				}
				return nil
			}

			query := args[0]

			// Log the query.
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			siftDir, err := config.Dir()
			if err != nil {
				return fmt.Errorf("get sift dir: %w", err)
			}
			logDir := siftDir + "/logs"
			logger := siftlog.NewLogger(logDir, cfg.Logs.MaxWeeks)

			logEntry := map[string]any{
				"query":     query,
				"limit":     limit,
				"timestamp": time.Now().UTC().Format(time.RFC3339),
			}
			if logErr := logger.Log("sql", logEntry); logErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to log query: %v\n", logErr)
			}

			// Execute the query.
			rows, err := conn.Query(query)
			if err != nil {
				return fmt.Errorf("execute query: %w", err)
			}
			defer rows.Close()

			cols, err := rows.Columns()
			if err != nil {
				return fmt.Errorf("get columns: %w", err)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)

			// Print header.
			fmt.Fprintln(w, strings.Join(cols, "\t"))

			// Print separator.
			seps := make([]string, len(cols))
			for i, c := range cols {
				seps[i] = strings.Repeat("-", len(c))
			}
			fmt.Fprintln(w, strings.Join(seps, "\t"))

			// Print rows.
			count := 0
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}

			for rows.Next() {
				if count >= limit {
					break
				}
				if err := rows.Scan(ptrs...); err != nil {
					return fmt.Errorf("scan row: %w", err)
				}

				parts := make([]string, len(cols))
				for i, v := range vals {
					parts[i] = formatValue(v)
				}
				fmt.Fprintln(w, strings.Join(parts, "\t"))
				count++
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterate rows: %w", err)
			}

			if err := w.Flush(); err != nil {
				return fmt.Errorf("flush output: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "\n(%d rows)\n", count)
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum number of rows to display")
	cmd.Flags().BoolVar(&schema, "schema", false, "Show database table schemas")

	return cmd
}

// formatValue converts a database value to a display string.
func formatValue(v any) string {
	if v == nil {
		return "NULL"
	}
	switch val := v.(type) {
	case []byte:
		return string(val)
	default:
		return fmt.Sprintf("%v", val)
	}
}
