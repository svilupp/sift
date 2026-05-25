package index

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// HeaderComment is prepended to every emitted `sift.toml` so a casual
// reader knows where the file came from and that human edits survive.
const HeaderComment = "# SIFT folder index. Maintained by SIFT. Human edits preserved.\n"

// multilineThreshold is the length above which a string is emitted as a
// multiline triple-quoted TOML string instead of an inline string.
const multilineThreshold = 80

// Marshal returns the canonical TOML representation of idx. Two calls on
// the same logical FolderIndex produce byte-identical output, and a
// Load(Marshal(x)) round-trip yields the same in-memory value.
func Marshal(idx *FolderIndex) ([]byte, error) {
	if idx == nil {
		return nil, fmt.Errorf("marshal: nil FolderIndex")
	}
	var buf bytes.Buffer
	buf.WriteString(HeaderComment)
	buf.WriteString("\n")

	schemaVersion := idx.SchemaVersion
	if schemaVersion <= 0 {
		schemaVersion = SchemaVersion
	}
	fmt.Fprintf(&buf, "schema_version = %d\n", schemaVersion)
	fmt.Fprintf(&buf, "ignore = %s\n", boolStr(idx.Ignore))

	if idx.Purpose != "" {
		buf.WriteString("\n")
		buf.WriteString("purpose = ")
		buf.WriteString(emitString(idx.Purpose))
		buf.WriteString("\n")
	}

	if len(idx.UseWhen) > 0 {
		buf.WriteString("\n")
		buf.WriteString("use_when = [\n")
		for _, item := range idx.UseWhen {
			buf.WriteString("  ")
			buf.WriteString(emitInlineString(item))
			buf.WriteString(",\n")
		}
		buf.WriteString("]\n")
	}

	if idx.Refresh != (RefreshStats{}) {
		buf.WriteString("\n[refresh]\n")
		fmt.Fprintf(&buf, "file_count = %d\n", idx.Refresh.FileCount)
		fmt.Fprintf(&buf, "word_count = %d\n", idx.Refresh.WordCount)
	}

	if len(idx.Files) > 0 {
		names := make([]string, 0, len(idx.Files))
		for name := range idx.Files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			entry := idx.Files[name]
			buf.WriteString("\n[files.")
			buf.WriteString(emitInlineString(name))
			buf.WriteString("]\n")
			fmt.Fprintf(&buf, "ignore = %s\n", boolStr(entry.Ignore))
			fmt.Fprintf(&buf, "content_hash = %s\n", emitInlineString(entry.ContentHash))
			fmt.Fprintf(&buf, "head_hash = %s\n", emitInlineString(entry.HeadHash))
			fmt.Fprintf(&buf, "tail_hash = %s\n", emitInlineString(entry.TailHash))
			fmt.Fprintf(&buf, "words = %d\n", entry.Words)
			if entry.Summary != "" {
				buf.WriteString("summary = ")
				buf.WriteString(emitString(entry.Summary))
				buf.WriteString("\n")
			}
		}
	}

	if len(idx.Folders) > 0 {
		names := make([]string, 0, len(idx.Folders))
		for name := range idx.Folders {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			entry := idx.Folders[name]
			buf.WriteString("\n[folders.")
			buf.WriteString(emitInlineString(name))
			buf.WriteString("]\n")
			fmt.Fprintf(&buf, "ignore = %s\n", boolStr(entry.Ignore))
		}
	}

	return buf.Bytes(), nil
}

// Save writes idx to path atomically: the bytes go to a sibling tmp file
// first, are fsync'd, and then renamed into place. Callers get either the
// old file or the new one — never a half-written file — even on crash.
func Save(path string, idx *FolderIndex) error {
	data, err := Marshal(idx)
	if err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	tmp, err := tempName(path)
	if err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("save %s: open tmp: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("save %s: write tmp: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("save %s: fsync tmp: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("save %s: close tmp: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("save %s: rename: %w", path, err)
	}
	// Best-effort directory fsync so the rename hits the disk too. We
	// ignore the error: not all filesystems / OSes support it.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func tempName(path string) (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return path + ".tmp." + hex.EncodeToString(buf[:]), nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// emitString picks between inline ("...") and multiline ("""...""")
// representations and escapes accordingly.
func emitString(s string) string {
	if shouldMultiline(s) {
		return emitMultilineString(s)
	}
	return emitInlineString(s)
}

func shouldMultiline(s string) bool {
	if strings.ContainsAny(s, "\n\r") {
		return true
	}
	if len(s) > multilineThreshold {
		return true
	}
	return false
}

// emitInlineString writes a TOML basic string ("...") with standard escapes.
func emitInlineString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// emitMultilineString writes a TOML multi-line basic string ("""...""")
// with a leading newline (canonical pretty form). Only `"""` and backslash
// runs that would terminate the literal are escaped; ordinary newlines and
// tabs are preserved verbatim.
func emitMultilineString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	b.WriteString(`"""`)
	b.WriteByte('\n')
	// Per TOML 1.0, only " sequences of 3+ in a row inside """...""" need
	// escaping. We handle them by escaping the third quote.
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			// If this would form """ inside the literal, escape it.
			if i+2 < len(s) && s[i+1] == '"' && s[i+2] == '"' {
				b.WriteString(`\"\"\"`)
				i += 2
			} else {
				b.WriteByte('"')
			}
		default:
			b.WriteByte(c)
		}
	}
	// Ensure there is a trailing newline before closing """ for clean diff.
	if !strings.HasSuffix(s, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(`"""`)
	return b.String()
}
