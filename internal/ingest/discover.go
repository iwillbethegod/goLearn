// Package ingest orchestrates per-file streaming into the worker pool
// and exposes per-file cancellation by basename.
package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Expand resolves each input path: a file is kept verbatim, a directory
// is read non-recursively and entries whose extension matches one of
// `exts` are appended.
func Expand(paths []string, exts []string) ([]string, error) {
	allow := make(map[string]struct{}, len(exts))
	for _, e := range exts {
		allow[strings.ToLower(e)] = struct{}{}
	}
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", p, err)
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, fmt.Errorf("readdir %s: %w", p, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if _, ok := allow[strings.ToLower(filepath.Ext(e.Name()))]; ok {
				out = append(out, filepath.Join(p, e.Name()))
			}
		}
	}
	return out, nil
}
