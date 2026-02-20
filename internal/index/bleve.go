package index

import (
	"errors"
	"fmt"
	"os"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
)

// BleveIndex wraps a Bleve full-text search index.
type BleveIndex struct {
	index bleve.Index
	path  string
}

// BleveDoc is the document structure indexed by Bleve.
type BleveDoc struct {
	Content string `json:"content"`
	Path    string `json:"path"`
}

// BleveResult represents a single BM25 search result.
type BleveResult struct {
	ChunkID    string   // The document ID (chunk ID as string)
	Score      float64  // BM25 relevance score
	Path       string   // File path from stored field
	Highlights []string // Fragment strings from Bleve highlighting
}

// OpenBleve opens an existing Bleve index or creates a new one.
func OpenBleve(path, analyzer string) (*BleveIndex, error) {
	idx, err := bleve.Open(path)
	if err != nil {
		if !errors.Is(err, bleve.ErrorIndexPathDoesNotExist) {
			return nil, fmt.Errorf("open bleve index: %w", err)
		}
		// Create new index.
		idx, err = bleve.New(path, buildMapping(analyzer))
		if err != nil {
			return nil, fmt.Errorf("create bleve index: %w", err)
		}
	}
	return &BleveIndex{index: idx, path: path}, nil
}

// buildMapping creates the index mapping for chunk documents.
func buildMapping(analyzer string) mapping.IndexMapping {
	contentField := bleve.NewTextFieldMapping()
	contentField.Analyzer = analyzer
	contentField.Store = true

	pathField := bleve.NewTextFieldMapping()
	pathField.Analyzer = "keyword"

	docMapping := bleve.NewDocumentMapping()
	docMapping.AddFieldMappingsAt("content", contentField)
	docMapping.AddFieldMappingsAt("path", pathField)
	docMapping.Dynamic = false

	indexMapping := bleve.NewIndexMapping()
	indexMapping.DefaultMapping = docMapping
	indexMapping.DefaultAnalyzer = analyzer

	return indexMapping
}

// Index adds or updates a document in the Bleve index.
func (b *BleveIndex) Index(chunkID string, content, path string) error {
	doc := BleveDoc{
		Content: content,
		Path:    path,
	}
	return b.index.Index(chunkID, doc)
}

// IndexBatch adds or updates multiple documents in a single Bleve batch.
func (b *BleveIndex) IndexBatch(docs map[string]BleveDoc) error {
	if len(docs) == 0 {
		return nil
	}
	batch := b.index.NewBatch()
	for id, doc := range docs {
		if err := batch.Index(id, doc); err != nil {
			return fmt.Errorf("batch index %s: %w", id, err)
		}
	}
	return b.index.Batch(batch)
}

// Delete removes a document from the Bleve index.
func (b *BleveIndex) Delete(chunkID string) error {
	return b.index.Delete(chunkID)
}

// Search performs a BM25 search and returns ranked results with highlighting.
func (b *BleveIndex) Search(queryStr string, size int) ([]BleveResult, error) {
	query := bleve.NewMatchQuery(queryStr)
	req := bleve.NewSearchRequest(query)
	req.Size = size
	req.Fields = []string{"path"}
	req.Highlight = bleve.NewHighlight()

	res, err := b.index.Search(req)
	if err != nil {
		return nil, fmt.Errorf("bleve search: %w", err)
	}

	results := make([]BleveResult, 0, len(res.Hits))
	for _, hit := range res.Hits {
		r := BleveResult{
			ChunkID: hit.ID,
			Score:   hit.Score,
		}
		if p, ok := hit.Fields["path"].(string); ok {
			r.Path = p
		}
		if frags, ok := hit.Fragments["content"]; ok {
			r.Highlights = frags
		}
		results = append(results, r)
	}
	return results, nil
}

// Close closes the Bleve index.
func (b *BleveIndex) Close() error {
	return b.index.Close()
}

// Destroy closes and removes the Bleve index directory.
func (b *BleveIndex) Destroy() error {
	if err := b.index.Close(); err != nil {
		return err
	}
	return os.RemoveAll(b.path)
}

// DocCount returns the number of documents in the index.
func (b *BleveIndex) DocCount() (uint64, error) {
	return b.index.DocCount()
}
