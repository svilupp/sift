package bm25

import (
	"encoding/binary"
	"math"
	"math/bits"
	"sort"

	"sift/internal/db"
)

// VectorResult represents a single vector similarity search result.
type VectorResult struct {
	ChunkID    int64
	Similarity float32
}

// IsBinaryDtype returns true if the dtype represents binary packed embeddings.
func IsBinaryDtype(dtype string) bool {
	return dtype == "binary" || dtype == "ubinary"
}

// EncodeVector converts a float32 slice to little-endian bytes.
func EncodeVector(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// DecodeVector converts little-endian bytes back to a float32 slice.
func DecodeVector(b []byte) []float32 {
	n := len(b) / 4
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// EncodeBinaryVector converts a Voyage binary embedding response ([]float32 of
// int8 values) to packed bytes. Each float value is a signed int8 representing
// 8 packed dimension bits.
func EncodeBinaryVector(v []float32) []byte {
	buf := make([]byte, len(v))
	for i, f := range v {
		buf[i] = byte(int8(f))
	}
	return buf
}

// DecodeBinaryVector converts packed bytes back to a float32 slice of int8 values.
func DecodeBinaryVector(b []byte) []float32 {
	v := make([]float32, len(b))
	for i, bv := range b {
		v[i] = float32(int8(bv))
	}
	return v
}

// CosineSimilarity computes the cosine similarity between two vectors.
// Returns 0.0 for zero-length vectors.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0.0
	}

	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0.0
	}

	return dot / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}

// DotProduct computes the dot product of two vectors.
// For unit-normalized vectors (as returned by Voyage), this equals cosine similarity
// but is faster since it skips the norm computation.
func DotProduct(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0.0
	}

	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}

// HammingSimilarity computes similarity based on Hamming distance between binary
// vectors. Each byte represents 8 packed dimension bits. Returns a value in [0, 1]
// where 1 means identical.
func HammingSimilarity(a, b []byte) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0.0
	}
	totalBits := len(a) * 8
	var diffBits int
	for i := range a {
		diffBits += bits.OnesCount8(a[i] ^ b[i])
	}
	return 1.0 - float32(diffBits)/float32(totalBits)
}

// VectorSearch performs a brute-force dot product search on float32 vectors.
// Voyage embeddings are unit-normalized, so dot product equals cosine similarity.
// It returns the top K results sorted by similarity descending.
func VectorSearch(query []float32, embeddings []db.EmbeddingRecord, topK int) []VectorResult {
	if len(embeddings) == 0 {
		return nil
	}

	results := make([]VectorResult, 0, len(embeddings))
	for _, emb := range embeddings {
		vec := DecodeVector(emb.Vector)
		sim := DotProduct(query, vec)
		results = append(results, VectorResult{
			ChunkID:    emb.ChunkID,
			Similarity: sim,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})

	if topK > 0 && topK < len(results) {
		results = results[:topK]
	}

	return results
}

// BinaryVectorSearch performs a brute-force Hamming distance search on binary vectors.
// The query and stored embeddings are raw packed bytes (no float decode needed).
// It returns the top K results sorted by similarity descending.
func BinaryVectorSearch(query []byte, embeddings []db.EmbeddingRecord, topK int) []VectorResult {
	if len(embeddings) == 0 {
		return nil
	}

	results := make([]VectorResult, 0, len(embeddings))
	for _, emb := range embeddings {
		sim := HammingSimilarity(query, emb.Vector)
		results = append(results, VectorResult{
			ChunkID:    emb.ChunkID,
			Similarity: sim,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})

	if topK > 0 && topK < len(results) {
		results = results[:topK]
	}

	return results
}
