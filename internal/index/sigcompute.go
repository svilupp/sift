package index

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cespare/xxhash/v2"
)

// HeadTailWindow is the number of words used for the head_hash and
// tail_hash signatures. Bounded so monster files stay cheap.
const HeadTailWindow = 500

// FileSig is the deterministic signature of a file's content used by the
// freshness tuple to decide whether a stored summary is still valid.
type FileSig struct {
	ContentHash string
	HeadHash    string
	TailHash    string
	Words       int
}

// AsEntry copies the signature fields into a FileEntry, leaving Summary
// and Ignore untouched. Useful when persisting freshly computed sigs.
func (s FileSig) AsEntry(prev FileEntry) FileEntry {
	prev.ContentHash = s.ContentHash
	prev.HeadHash = s.HeadHash
	prev.TailHash = s.TailHash
	prev.Words = s.Words
	return prev
}

// SigOf reads the file at path and returns its signature. Files larger
// than 1 MB are read in a streaming fashion so we don't pin huge bytes
// just to hash them.
func SigOf(path string) (FileSig, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileSig{}, fmt.Errorf("sigcompute %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return FileSig{}, fmt.Errorf("sigcompute %s: stat: %w", path, err)
	}

	const streamThreshold = 1 << 20 // 1 MB
	if info.Size() <= streamThreshold {
		data, err := io.ReadAll(f)
		if err != nil {
			return FileSig{}, fmt.Errorf("sigcompute %s: read: %w", path, err)
		}
		return ComputeSig(data), nil
	}
	return computeSigStream(f)
}

// ComputeSig returns the signature of an in-memory byte slice. Identical
// content always yields the identical FileSig.
func ComputeSig(data []byte) FileSig {
	contentHash := hashBytes(data)
	words := strings.Fields(string(data))
	return FileSig{
		ContentHash: contentHash,
		HeadHash:    hashWordWindow(words, true),
		TailHash:    hashWordWindow(words, false),
		Words:       len(words),
	}
}

// SplitWords returns the canonical word splitting used by every freshness
// helper. It is `strings.Fields` semantics — split on any unicode
// whitespace, no empty tokens.
func SplitWords(s string) []string {
	return strings.Fields(s)
}

func hashBytes(b []byte) string {
	return fmt.Sprintf("%016x", xxhash.Sum64(b))
}

// hashWordWindow joins the first or last HeadTailWindow words with a
// single space and hashes the result. Files shorter than the window
// hash the entire word list (so head and tail collapse to the same
// signature, which is fine for freshness — content_hash distinguishes
// them anyway).
func hashWordWindow(words []string, head bool) string {
	if len(words) == 0 {
		return hashBytes(nil)
	}
	if len(words) <= HeadTailWindow {
		return hashBytes([]byte(strings.Join(words, " ")))
	}
	var window []string
	if head {
		window = words[:HeadTailWindow]
	} else {
		window = words[len(words)-HeadTailWindow:]
	}
	return hashBytes([]byte(strings.Join(window, " ")))
}

// computeSigStream computes a signature without materializing the entire
// file in memory. It does two passes via bufio: a single byte stream
// fueling both the xxhash digest and a word-aware ring buffer that keeps
// the head and tail windows.
func computeSigStream(r io.ReadSeeker) (FileSig, error) {
	hasher := xxhash.New()

	// First pass would hash the bytes, but we only want one pass.
	// Use a TeeReader semantically — feed a scanner that emits words and
	// a hasher in lock-step.
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return FileSig{}, fmt.Errorf("sigcompute stream: seek: %w", err)
	}

	// We need both content-hash and word stream. Read in chunks; for each
	// chunk update hasher AND scan words from a residual buffer.
	br := bufio.NewReaderSize(r, 1<<16)
	headBuf := make([]string, 0, HeadTailWindow)
	tailRing := make([]string, HeadTailWindow)
	tailLen := 0
	tailHead := 0
	totalWords := 0

	var partial strings.Builder
	chunk := make([]byte, 32*1024)
	for {
		n, err := br.Read(chunk)
		if n > 0 {
			data := chunk[:n]
			_, _ = hasher.Write(data)
			// Find boundary: append to partial, then split. Last token
			// might be incomplete unless the chunk ends in whitespace.
			partial.Write(data)
			s := partial.String()
			endsInSpace := isSpaceByte(data[n-1])
			tokens := strings.Fields(s)
			var keep string
			if !endsInSpace && len(tokens) > 0 {
				keep = tokens[len(tokens)-1]
				tokens = tokens[:len(tokens)-1]
			}
			for _, w := range tokens {
				totalWords++
				if len(headBuf) < HeadTailWindow {
					headBuf = append(headBuf, w)
				}
				if tailLen < HeadTailWindow {
					tailRing[tailLen] = w
					tailLen++
				} else {
					tailRing[tailHead] = w
					tailHead = (tailHead + 1) % HeadTailWindow
				}
			}
			partial.Reset()
			partial.WriteString(keep)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return FileSig{}, fmt.Errorf("sigcompute stream: read: %w", err)
		}
	}
	// Flush trailing partial token.
	if rest := strings.Fields(partial.String()); len(rest) > 0 {
		for _, w := range rest {
			totalWords++
			if len(headBuf) < HeadTailWindow {
				headBuf = append(headBuf, w)
			}
			if tailLen < HeadTailWindow {
				tailRing[tailLen] = w
				tailLen++
			} else {
				tailRing[tailHead] = w
				tailHead = (tailHead + 1) % HeadTailWindow
			}
		}
	}

	tailWords := make([]string, tailLen)
	for i := 0; i < tailLen; i++ {
		tailWords[i] = tailRing[(tailHead+i)%HeadTailWindow]
	}

	contentHash := fmt.Sprintf("%016x", hasher.Sum64())

	headHash := hashBytes(nil)
	tailHash := hashBytes(nil)
	if len(headBuf) > 0 {
		headHash = hashBytes([]byte(strings.Join(headBuf, " ")))
	}
	if len(tailWords) > 0 {
		tailHash = hashBytes([]byte(strings.Join(tailWords, " ")))
	}
	// If file has fewer than HeadTailWindow words, head and tail collapse,
	// matching ComputeSig's in-memory behavior.
	if totalWords <= HeadTailWindow {
		// Recompute both from headBuf for byte parity with ComputeSig.
		joined := strings.Join(headBuf, " ")
		headHash = hashBytes([]byte(joined))
		tailHash = headHash
	}

	return FileSig{
		ContentHash: contentHash,
		HeadHash:    headHash,
		TailHash:    tailHash,
		Words:       totalWords,
	}, nil
}

func isSpaceByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	}
	return false
}
