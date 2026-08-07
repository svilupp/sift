package cli

import (
	"github.com/spf13/cobra"
)

// NewRootCmd creates the root sift command.
func NewRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:   "sift",
		Short: "Local-first search and semantic navigation for knowledge files",
		Long: `SIFT indexes and navigates personal knowledge files. It works fully locally
with BM25 search and extractive folder orientation. When an API key is configured,
vector embeddings and reranking add semantic recall and ranking quality.

Quick start:
  sift config init                    # initialize ~/.sift/
  sift collections add vault ~/docs/  # add a folder
  sift refresh                        # index everything

Search examples:
  sift search "database migration"                          # simple keyword search
  sift search "How does the agent handle rate limiting?"    # natural language works too
  sift search "auth flow" --collection vault --since 1w     # filter by collection + recency
  sift search "Returns API proof of concept" --json         # machine-readable output

Agent navigation:
  sift collections --json                    # discover knowledge collections
  sift index -c vault --orient               # semantic root + one level
  sift index docs -c vault --orient           # drill into a relevant branch
  sift read docs/guide.md -c vault --section setup --json`,
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
		newReadCmd(),
	)

	return root
}
