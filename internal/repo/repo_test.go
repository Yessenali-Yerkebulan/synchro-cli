package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/synchro/synchro/internal/model"
)

func write(t *testing.T, files []model.CodeFile, msg string) (*Info, error) {
	t.Helper()
	return WriteFiles(t.TempDir(), "proj", files, msg)
}

func TestWriteFilesCommits(t *testing.T) {
	info, err := write(t, []model.CodeFile{
		{Path: "main.go", Content: "package main\n"},
		{Path: "docs/README.md", Content: "# hi\n"},
	}, "add main")
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	if info == nil {
		t.Fatal("expected commit info")
	}
	if len(info.Commit) != 12 {
		t.Errorf("commit = %q, want 12 hex chars", info.Commit)
	}
}

func TestWriteFilesSkipsTraversal(t *testing.T) {
	dir := t.TempDir()
	// An absolute or parent-escaping path must never be written.
	_, err := WriteFiles(dir, "proj", []model.CodeFile{{Path: "../escape.txt", Content: "x"}}, "m")
	if err == nil {
		t.Fatal("expected an error when every path was rejected")
	}
	if !strings.Contains(err.Error(), "outside the repository") {
		t.Errorf("err = %v, want it to mention the rejected path", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "escape.txt")); statErr == nil {
		t.Error("the escaping file was written anyway")
	}
}

func TestWriteFilesRejectsAllUnsafeButSucceedsOnGoodOnes(t *testing.T) {
	dir := t.TempDir()
	info, err := WriteFiles(dir, "proj", []model.CodeFile{
		{Path: "ok.txt", Content: "fine\n"},
		{Path: "../bad.txt", Content: "no\n"},
	}, "m")
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	if info == nil {
		t.Fatal("expected a commit for the safe file")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.txt")); err == nil {
		t.Error("the escaping file was written")
	}
}

func TestWriteFilesNoFilesIsNotAnError(t *testing.T) {
	info, err := write(t, nil, "m")
	if err != nil || info != nil {
		t.Errorf("WriteFiles(nil) = %v/%v, want nil/nil", info, err)
	}
}

func TestWriteFilesIdenticalContentReportsNoCommit(t *testing.T) {
	// The important case: a model that regenerates byte-identical files.
	// This must be distinguishable from an error, because the caller tells
	// the user "nothing to record" rather than "commit it".
	dir := t.TempDir()
	files := []model.CodeFile{{Path: "a.txt", Content: "same\n"}}

	first, err := WriteFiles(dir, "proj", files, "first")
	if err != nil {
		t.Fatalf("first WriteFiles: %v", err)
	}
	if first == nil {
		t.Fatal("first write should commit")
	}

	second, err := WriteFiles(dir, "proj", files, "second")
	if err != nil {
		t.Fatalf("second WriteFiles: %v, want no error for identical content", err)
	}
	if second != nil {
		t.Errorf("second write = %+v, want nil (nothing to commit)", second)
	}
}

func TestWriteFilesChangedContentCommitsAgain(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteFiles(dir, "proj", []model.CodeFile{{Path: "a.txt", Content: "v1\n"}}, "one"); err != nil {
		t.Fatalf("first: %v", err)
	}
	info, err := WriteFiles(dir, "proj", []model.CodeFile{{Path: "a.txt", Content: "v2\n"}}, "two")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if info == nil {
		t.Error("changed content should produce a new commit")
	}
}

func TestWriteFilesNeedsProjectID(t *testing.T) {
	if _, err := WriteFiles(t.TempDir(), "", []model.CodeFile{{Path: "a", Content: "x"}}, "m"); err == nil {
		t.Error("expected an error with no project id")
	}
}

func TestLogListsCommits(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteFiles(dir, "proj", []model.CodeFile{{Path: "a.txt", Content: "1\n"}}, "first commit"); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	if _, err := WriteFiles(dir, "proj", []model.CodeFile{{Path: "a.txt", Content: "2\n"}}, "second commit"); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	lines := Log(dir, "proj", 10)
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "second commit") {
		t.Errorf("newest first expected, got %q", lines[0])
	}
}

func TestLogNoRepo(t *testing.T) {
	if lines := Log(t.TempDir(), "nothing-here", 10); lines != nil {
		t.Errorf("Log = %v, want nil for a project with no repo", lines)
	}
}
