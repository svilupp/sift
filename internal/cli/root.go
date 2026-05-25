package cli

import (
	"github.com/spf13/cobra"
)

// NewRootCmd creates the root sift command.
func NewRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:   "sift",
		Short: "Search Index for Finding Things - semantic search tool",
		Long: `SIFT is a local-first semantic search engine for personal knowledge files.
Combines BM25 lexical search, vector embeddings, and reranking for high-quality retrieval.

Quick start:
  sift config init                    # initialize ~/.sift/
  sift collections add vault ~/docs/  # add a folder
  sift refresh                        # index everything

Search examples:
  sift search "database migration"                          # simple keyword search
  sift search "How does the agent handle rate limiting?"    # natural language works too
  sift search "auth flow" --collection vault --since 1w     # filter by collection + recency
  sift search "Returns API proof of concept" --json         # machine-readable output`,
		Version: version,
	}

	root.AddCommand(
		newConfigCmd(),
		newCollectionsCmd(),
		newRefreshCmd(),
		newSearchCmd(),
		newKeywordsCmd(),
		newRefsCmd(),
		newFeedbackCmd(),
		newSqlCmd(),
		newEvalCmd(),
		newLinksCmd(),
		newDaemonCmd(),
		newIndexCmd(),
	)

	return root
}
