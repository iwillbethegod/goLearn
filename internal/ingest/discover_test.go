package ingest_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ashishsinghbhadoria/goLearn/internal/ingest"
)

func TestExpand_FilesPassedThrough(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.csv")
	b := filepath.Join(dir, "b.csv")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("id,name,email\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ingest.Expand([]string{a, b}, []string{".csv"})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
}

func TestExpand_DirIsScannedNonRecursively(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"x.csv", "y.csv", "skip.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// nested dir should NOT be descended.
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "deep.csv"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ingest.Expand([]string{dir}, []string{".csv"})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	sort.Strings(got)
	if len(got) != 2 {
		t.Fatalf("got %d files, want 2 (recursive picked up): %v", len(got), got)
	}
	for _, p := range got {
		if filepath.Ext(p) != ".csv" {
			t.Errorf("non-csv leaked: %s", p)
		}
	}
}

func TestExpand_ExtensionMatchIsCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.CSV", "b.Csv", "c.csv"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ingest.Expand([]string{dir}, []string{".csv"})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d, want 3 (mixed-case ext): %v", len(got), got)
	}
}

func TestExpand_MissingPathErrors(t *testing.T) {
	if _, err := ingest.Expand([]string{"/nonexistent/path"}, []string{".csv"}); err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestExpand_NoMatchingFiles(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "x.json"), []byte("data"), 0o644)
	got, err := ingest.Expand([]string{dir}, []string{".csv"})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d, want 0 (no .csv files)", len(got))
	}
}

func TestExpand_EmptyExtList(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.csv"), []byte("data"), 0o644)
	got, err := ingest.Expand([]string{dir}, nil)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("nil ext list should match nothing inside dir, got %v", got)
	}
}
