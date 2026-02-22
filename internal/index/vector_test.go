package index

import (
	"math"
	"testing"

	"sift/internal/db"
)

func TestEncodeDecodeRoundtrip(t *testing.T) {
	tests := []struct {
		name string
		vec  []float32
	}{
		{"simple", []float32{1.0, 2.0, 3.0}},
		{"negative", []float32{-1.5, 0.0, 1.5}},
		{"small values", []float32{0.001, 0.002, 0.003}},
		{"single", []float32{42.0}},
		{"empty", []float32{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeVector(tt.vec)
			decoded := DecodeVector(encoded)

			if len(decoded) != len(tt.vec) {
				t.Fatalf("length mismatch: got %d, want %d", len(decoded), len(tt.vec))
			}
			for i := range tt.vec {
				if decoded[i] != tt.vec[i] {
					t.Errorf("index %d: got %f, want %f", i, decoded[i], tt.vec[i])
				}
			}
		})
	}
}

func TestBinaryEncodeDecodeRoundtrip(t *testing.T) {
	tests := []struct {
		name string
		vec  []float32
	}{
		{"positive values", []float32{42.0, 15.0, 100.0, 1.0}},
		{"negative values", []float32{-15.0, -128.0, 127.0, 0.0}},
		{"mixed", []float32{-1.0, 42.0, -100.0, 55.0}},
		{"single", []float32{42.0}},
		{"empty", []float32{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeBinaryVector(tt.vec)
			decoded := DecodeBinaryVector(encoded)

			if len(decoded) != len(tt.vec) {
				t.Fatalf("length mismatch: got %d, want %d", len(decoded), len(tt.vec))
			}
			for i := range tt.vec {
				if decoded[i] != tt.vec[i] {
					t.Errorf("index %d: got %f, want %f", i, decoded[i], tt.vec[i])
				}
			}
		})
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name    string
		a, b    []float32
		want    float32
		epsilon float32
	}{
		{
			name:    "identical vectors",
			a:       []float32{1, 2, 3},
			b:       []float32{1, 2, 3},
			want:    1.0,
			epsilon: 1e-6,
		},
		{
			name:    "orthogonal vectors",
			a:       []float32{1, 0, 0},
			b:       []float32{0, 1, 0},
			want:    0.0,
			epsilon: 1e-6,
		},
		{
			name:    "opposite vectors",
			a:       []float32{1, 0, 0},
			b:       []float32{-1, 0, 0},
			want:    -1.0,
			epsilon: 1e-6,
		},
		{
			name:    "zero vector a",
			a:       []float32{0, 0, 0},
			b:       []float32{1, 2, 3},
			want:    0.0,
			epsilon: 1e-6,
		},
		{
			name:    "zero vector b",
			a:       []float32{1, 2, 3},
			b:       []float32{0, 0, 0},
			want:    0.0,
			epsilon: 1e-6,
		},
		{
			name:    "both zero",
			a:       []float32{0, 0, 0},
			b:       []float32{0, 0, 0},
			want:    0.0,
			epsilon: 1e-6,
		},
		{
			name:    "known value",
			a:       []float32{1, 2, 3},
			b:       []float32{4, 5, 6},
			want:    0.9746318, // precomputed
			epsilon: 1e-5,
		},
		{
			name:    "different lengths",
			a:       []float32{1, 2},
			b:       []float32{1, 2, 3},
			want:    0.0,
			epsilon: 1e-6,
		},
		{
			name:    "empty vectors",
			a:       []float32{},
			b:       []float32{},
			want:    0.0,
			epsilon: 1e-6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CosineSimilarity(tt.a, tt.b)
			if math.Abs(float64(got-tt.want)) > float64(tt.epsilon) {
				t.Errorf("got %f, want %f (epsilon %f)", got, tt.want, tt.epsilon)
			}
		})
	}
}

func TestDotProduct(t *testing.T) {
	tests := []struct {
		name    string
		a, b    []float32
		want    float32
		epsilon float32
	}{
		{
			name:    "unit vectors identical",
			a:       []float32{1, 0, 0},
			b:       []float32{1, 0, 0},
			want:    1.0,
			epsilon: 1e-6,
		},
		{
			name:    "unit vectors orthogonal",
			a:       []float32{1, 0, 0},
			b:       []float32{0, 1, 0},
			want:    0.0,
			epsilon: 1e-6,
		},
		{
			name:    "unit vectors opposite",
			a:       []float32{1, 0, 0},
			b:       []float32{-1, 0, 0},
			want:    -1.0,
			epsilon: 1e-6,
		},
		{
			name:    "non-unit vectors",
			a:       []float32{1, 2, 3},
			b:       []float32{4, 5, 6},
			want:    32.0, // 1*4 + 2*5 + 3*6
			epsilon: 1e-6,
		},
		{
			name:    "empty vectors",
			a:       []float32{},
			b:       []float32{},
			want:    0.0,
			epsilon: 1e-6,
		},
		{
			name:    "different lengths",
			a:       []float32{1, 2},
			b:       []float32{1, 2, 3},
			want:    0.0,
			epsilon: 1e-6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DotProduct(tt.a, tt.b)
			if math.Abs(float64(got-tt.want)) > float64(tt.epsilon) {
				t.Errorf("got %f, want %f (epsilon %f)", got, tt.want, tt.epsilon)
			}
		})
	}
}

func TestHammingSimilarity(t *testing.T) {
	tests := []struct {
		name    string
		a, b    []byte
		want    float32
		epsilon float32
	}{
		{
			name:    "identical bytes",
			a:       []byte{0xFF, 0x00, 0xAA},
			b:       []byte{0xFF, 0x00, 0xAA},
			want:    1.0,
			epsilon: 1e-6,
		},
		{
			name:    "completely different",
			a:       []byte{0xFF, 0xFF},
			b:       []byte{0x00, 0x00},
			want:    0.0,
			epsilon: 1e-6,
		},
		{
			name:    "one bit different",
			a:       []byte{0xFF}, // 11111111
			b:       []byte{0xFE}, // 11111110
			want:    7.0 / 8.0,    // 7 matching bits out of 8
			epsilon: 1e-6,
		},
		{
			name:    "half different",
			a:       []byte{0xF0}, // 11110000
			b:       []byte{0x0F}, // 00001111
			want:    0.0,          // all 8 bits differ
			epsilon: 1e-6,
		},
		{
			name:    "empty",
			a:       []byte{},
			b:       []byte{},
			want:    0.0,
			epsilon: 1e-6,
		},
		{
			name:    "different lengths",
			a:       []byte{0xFF},
			b:       []byte{0xFF, 0x00},
			want:    0.0,
			epsilon: 1e-6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HammingSimilarity(tt.a, tt.b)
			if math.Abs(float64(got-tt.want)) > float64(tt.epsilon) {
				t.Errorf("got %f, want %f (epsilon %f)", got, tt.want, tt.epsilon)
			}
		})
	}
}

func TestIsBinaryDtype(t *testing.T) {
	tests := []struct {
		dtype string
		want  bool
	}{
		{"binary", true},
		{"ubinary", true},
		{"float", false},
		{"int8", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.dtype, func(t *testing.T) {
			got := IsBinaryDtype(tt.dtype)
			if got != tt.want {
				t.Errorf("IsBinaryDtype(%q) = %v, want %v", tt.dtype, got, tt.want)
			}
		})
	}
}

func TestVectorSearch(t *testing.T) {
	// Create test embeddings. The query is [1, 0, 0] (unit-normalized).
	query := []float32{1, 0, 0}

	embeddings := []db.EmbeddingRecord{
		{ChunkID: 1, Vector: EncodeVector([]float32{0, 1, 0})},     // orthogonal
		{ChunkID: 2, Vector: EncodeVector([]float32{1, 0, 0})},     // identical
		{ChunkID: 3, Vector: EncodeVector([]float32{0.9, 0.1, 0})}, // very similar
		{ChunkID: 4, Vector: EncodeVector([]float32{0, 0, 1})},     // orthogonal
		{ChunkID: 5, Vector: EncodeVector([]float32{0.5, 0.5, 0})}, // moderate
	}

	t.Run("top 3 ordering", func(t *testing.T) {
		results := VectorSearch(query, embeddings, 3)
		if len(results) != 3 {
			t.Fatalf("expected 3 results, got %d", len(results))
		}

		// Chunk 2 (identical) should be first.
		if results[0].ChunkID != 2 {
			t.Errorf("expected first result to be chunk 2, got %d", results[0].ChunkID)
		}
		// Chunk 3 (very similar) should be second.
		if results[1].ChunkID != 3 {
			t.Errorf("expected second result to be chunk 3, got %d", results[1].ChunkID)
		}

		// Scores should be descending.
		for i := 1; i < len(results); i++ {
			if results[i].Similarity > results[i-1].Similarity {
				t.Errorf("results not in descending order at index %d: %f > %f",
					i, results[i].Similarity, results[i-1].Similarity)
			}
		}
	})

	t.Run("top K larger than results", func(t *testing.T) {
		results := VectorSearch(query, embeddings, 100)
		if len(results) != 5 {
			t.Errorf("expected 5 results, got %d", len(results))
		}
	})

	t.Run("empty embeddings", func(t *testing.T) {
		results := VectorSearch(query, nil, 10)
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})

	t.Run("single embedding", func(t *testing.T) {
		single := []db.EmbeddingRecord{
			{ChunkID: 42, Vector: EncodeVector([]float32{0.5, 0.5, 0})},
		}
		results := VectorSearch(query, single, 10)
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].ChunkID != 42 {
			t.Errorf("expected chunk 42, got %d", results[0].ChunkID)
		}
	})
}

func TestBinaryVectorSearch(t *testing.T) {
	// Binary vectors: each byte is 8 packed bits.
	query := []byte{0xFF, 0x00} // 11111111 00000000

	embeddings := []db.EmbeddingRecord{
		{ChunkID: 1, Vector: []byte{0xFF, 0x00}}, // identical
		{ChunkID: 2, Vector: []byte{0xFF, 0xFF}}, // half different
		{ChunkID: 3, Vector: []byte{0x00, 0xFF}}, // all different
		{ChunkID: 4, Vector: []byte{0xFE, 0x00}}, // 1 bit different
	}

	t.Run("ordering", func(t *testing.T) {
		results := BinaryVectorSearch(query, embeddings, 4)
		if len(results) != 4 {
			t.Fatalf("expected 4 results, got %d", len(results))
		}

		// Chunk 1 (identical) should be first.
		if results[0].ChunkID != 1 {
			t.Errorf("expected first result to be chunk 1, got %d", results[0].ChunkID)
		}
		if results[0].Similarity != 1.0 {
			t.Errorf("expected similarity 1.0, got %f", results[0].Similarity)
		}

		// Chunk 4 (1 bit different) should be second.
		if results[1].ChunkID != 4 {
			t.Errorf("expected second result to be chunk 4, got %d", results[1].ChunkID)
		}

		// Chunk 3 (all different) should be last.
		if results[3].ChunkID != 3 {
			t.Errorf("expected last result to be chunk 3, got %d", results[3].ChunkID)
		}
		if results[3].Similarity != 0.0 {
			t.Errorf("expected similarity 0.0, got %f", results[3].Similarity)
		}
	})

	t.Run("top K", func(t *testing.T) {
		results := BinaryVectorSearch(query, embeddings, 2)
		if len(results) != 2 {
			t.Fatalf("expected 2 results, got %d", len(results))
		}
	})

	t.Run("empty", func(t *testing.T) {
		results := BinaryVectorSearch(query, nil, 10)
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})
}
