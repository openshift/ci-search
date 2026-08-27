package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneEmptyDirs(t *testing.T) {
	base := t.TempDir()
	old := time.Now().Add(-2 * emptyDirGracePeriod)

	mkdir := func(rel string, mtime time.Time) string {
		p := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}
	writeFile := func(rel string) {
		p := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// A populated build directory: must survive along with its ancestors.
	writeFile("logs/job-a/1/junit.failures")
	// A chain of empty, aged-out directories: must collapse entirely.
	emptyChain := mkdir("logs/job-b/2", old)
	// An empty but recently-touched directory: must be left alone (writer race guard).
	freshEmpty := mkdir("logs/job-c/3", time.Now())
	// Make the parents of the aged-out leaf aged-out too so the whole chain is eligible.
	if err := os.Chtimes(filepath.Join(base, "logs", "job-b"), old, old); err != nil {
		t.Fatal(err)
	}

	index := &pathIndex{base: base}
	removed, err := index.PruneEmptyDirs()
	if err != nil {
		t.Fatal(err)
	}
	// Removed: logs/job-b/2 and logs/job-b (two empty dirs collapsed bottom-up).
	if removed != 2 {
		t.Errorf("expected 2 removed directories, got %d", removed)
	}

	if _, err := os.Stat(emptyChain); !os.IsNotExist(err) {
		t.Errorf("expected empty aged-out chain %s to be removed, err=%v", emptyChain, err)
	}
	if _, err := os.Stat(filepath.Join(base, "logs", "job-b")); !os.IsNotExist(err) {
		t.Errorf("expected empty parent job-b to be removed")
	}
	if _, err := os.Stat(freshEmpty); err != nil {
		t.Errorf("expected recently-touched empty dir %s to survive, err=%v", freshEmpty, err)
	}
	if _, err := os.Stat(filepath.Join(base, "logs", "job-a", "1", "junit.failures")); err != nil {
		t.Errorf("expected populated directory to survive, err=%v", err)
	}
	if _, err := os.Stat(base); err != nil {
		t.Errorf("expected base directory to survive, err=%v", err)
	}
}
