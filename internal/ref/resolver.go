package ref

import (
	"os"
	"path/filepath"
)

type Resolver struct {
	collectionRoots []string
}

func NewResolver(collectionRoots []string) *Resolver {
	roots := make([]string, 0, len(collectionRoots))
	seen := make(map[string]struct{}, len(collectionRoots))
	for _, root := range collectionRoots {
		if root == "" {
			continue
		}
		cleaned := root
		if !filepath.IsAbs(cleaned) {
			if abs, err := filepath.Abs(cleaned); err == nil {
				cleaned = abs
			}
		}
		cleaned = filepath.Clean(cleaned)
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		roots = append(roots, cleaned)
	}
	return &Resolver{collectionRoots: roots}
}

func (r *Resolver) Resolve(sourcePath, rawPath string) Resolution {
	res := Resolution{RawPath: rawPath}
	if rawPath == "" {
		return res
	}

	if filepath.IsAbs(rawPath) {
		res.ResolvedPath = filepath.Clean(rawPath)
		res.Exists = fileExists(res.ResolvedPath)
		return res
	}

	if sourcePath != "" && !filepath.IsAbs(sourcePath) {
		if abs, err := filepath.Abs(sourcePath); err == nil {
			sourcePath = abs
		}
	}

	if filepath.IsAbs(sourcePath) {
		res.ResolvedPath = filepath.Clean(filepath.Join(filepath.Dir(sourcePath), rawPath))
		if fileExists(res.ResolvedPath) {
			res.Exists = true
			return res
		}
	}

	for _, root := range r.collectionRoots {
		candidate := filepath.Clean(filepath.Join(root, rawPath))
		if fileExists(candidate) {
			res.ResolvedPath = candidate
			res.Exists = true
			return res
		}
	}

	return res
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
