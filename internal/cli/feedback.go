package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"sift/internal/cache"
	"sift/internal/config"
)

func newFeedbackCmd() *cobra.Command {
	var (
		positive string
		negative string
	)

	cmd := &cobra.Command{
		Use:   "feedback <search_id>",
		Short: "Record feedback for search results",
		Long: `Record positive or negative feedback to improve future search rankings.
The search_id comes from the search output header (id:abc123).
Result labels (a, b, c, ...) map to results in the order they were returned.

Feedback boosts/penalizes specific chunks via Bayesian scoring and
invalidates the search cache so the next search reflects the change.

Example workflow:
  sift search "auth flow"                                   # returns id:a1b2c3
  sift feedback a1b2c3 --positive a,b --negative d          # a,b were good, d was bad`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			searchID := args[0]

			if positive == "" && negative == "" {
				return fmt.Errorf("at least one of --positive or --negative must be provided")
			}

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			// Look up search session.
			session, err := database.GetSearchSession(searchID)
			if err != nil {
				return fmt.Errorf("lookup search session: %w", err)
			}
			if session == nil {
				return fmt.Errorf("search session %q not found", searchID)
			}

			// Parse stored results to resolve indices.
			var results []struct {
				Index   string `json:"index"`
				ChunkID int64  `json:"chunk_id"`
				File    string `json:"file"`
			}
			if err := json.Unmarshal([]byte(session.ResultsJSON), &results); err != nil {
				return fmt.Errorf("parse search results: %w", err)
			}

			var positiveCount, negativeCount int

			// Process positive signals.
			if positive != "" {
				indices, err := parseLetterIndices(positive)
				if err != nil {
					return fmt.Errorf("invalid --positive: %w", err)
				}
				for _, idx := range indices {
					if idx < 0 || idx >= len(results) {
						return fmt.Errorf("result index %q (position %d) is out of range (0-%d)", indexLabel(idx), idx, len(results)-1)
					}
					r := results[idx]
					if err := database.InsertFeedback(searchID, idx, r.File, r.ChunkID, "positive", session.Query); err != nil {
						return fmt.Errorf("insert positive feedback: %w", err)
					}
					positiveCount++
				}
			}

			// Process negative signals.
			if negative != "" {
				indices, err := parseLetterIndices(negative)
				if err != nil {
					return fmt.Errorf("invalid --negative: %w", err)
				}
				for _, idx := range indices {
					if idx < 0 || idx >= len(results) {
						return fmt.Errorf("result index %q (position %d) is out of range (0-%d)", indexLabel(idx), idx, len(results)-1)
					}
					r := results[idx]
					if err := database.InsertFeedback(searchID, idx, r.File, r.ChunkID, "negative", session.Query); err != nil {
						return fmt.Errorf("insert negative feedback: %w", err)
					}
					negativeCount++
				}
			}

			// Invalidate search cache after feedback (scores may change).
			cfg, cfgErr := config.Load()
			if cfgErr == nil && cfg.Cache.Enabled {
				cache.New(database, cfg.Cache.TTLSeconds, cfg.Cache.MaxEntries).Clear()
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Recorded %d positive and %d negative signals for search %s\n", positiveCount, negativeCount, searchID)
			return nil
		},
	}

	cmd.Flags().StringVar(&positive, "positive", "", "Comma-separated positive result indices (e.g. a,b)")
	cmd.Flags().StringVar(&negative, "negative", "", "Comma-separated negative result indices (e.g. c,d)")

	return cmd
}

// parseLetterIndices converts comma-separated letter indices (a,b,c) to numeric indices (0,1,2).
func parseLetterIndices(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	var indices []int
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		idx, err := letterToIndex(p)
		if err != nil {
			return nil, err
		}
		indices = append(indices, idx)
	}
	if len(indices) == 0 {
		return nil, fmt.Errorf("no valid indices provided")
	}
	return indices, nil
}

// letterToIndex converts a letter label to a 0-based index.
// "a" -> 0, "b" -> 1, ..., "z" -> 25, "aa" -> 26, etc.
func letterToIndex(label string) (int, error) {
	if len(label) == 0 {
		return 0, fmt.Errorf("empty label")
	}
	for _, ch := range label {
		if ch < 'a' || ch > 'z' {
			return 0, fmt.Errorf("invalid label %q: must be lowercase letters", label)
		}
	}
	if len(label) == 1 {
		return int(label[0] - 'a'), nil
	}
	if len(label) == 2 {
		return int(label[0]-'a'+1)*26 + int(label[1]-'a'), nil
	}
	return 0, fmt.Errorf("label %q too long (max 2 characters)", label)
}
