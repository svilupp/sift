package chunk

import "fmt"

// FormatProvenance returns an XML source tag with file metadata.
func FormatProvenance(relativePath, title, collection string) string {
	return fmt.Sprintf(`<source file="%s" title="%s" collection="%s" />`, relativePath, title, collection)
}

// PrependProvenance prefixes content with a provenance tag.
func PrependProvenance(content, relativePath, title, collection string) string {
	return FormatProvenance(relativePath, title, collection) + "\n" + content
}
