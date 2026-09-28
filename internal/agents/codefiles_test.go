package agents

import (
	"strings"
	"testing"
)

func TestParseCodeFiles(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "single file",
			input: "Here is the code.\n\nFILE: main.go\n```go\npackage main\n```\n",
			want:  []string{"main.go"},
		},
		{
			name:  "several files",
			input: "FILE: a.txt\n```\na\n```\n\nFILE: nested/b.txt\n```\nb\n```\n",
			want:  []string{"a.txt", "nested/b.txt"},
		},
		{
			name:  "no files at all",
			input: "Just an explanation, no code.",
			want:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, files := ParseCodeFiles(tc.input)
			if len(files) != len(tc.want) {
				t.Fatalf("got %d files, want %d: %+v", len(files), len(tc.want), files)
			}
			for i, f := range files {
				if f.Path != tc.want[i] {
					t.Errorf("file %d path = %q, want %q", i, f.Path, tc.want[i])
				}
			}
		})
	}
}

func TestParseCodeFilesKeepsContent(t *testing.T) {
	in := "FILE: main.go\n```go\npackage main\n\nfunc main() {}\n```\n"
	_, files := ParseCodeFiles(in)
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	body := files[0].Content
	for _, want := range []string{"package main", "func main()"} {
		if !strings.Contains(body, want) {
			t.Errorf("content is missing %q; got:\n%s", want, body)
		}
	}
	// The fence and the FILE: header must not end up in the file body.
	if strings.Contains(body, "FILE:") || strings.Contains(body, "```") {
		t.Errorf("content still contains the envelope:\n%s", body)
	}
}

func TestParseCodeFilesReturnsSummary(t *testing.T) {
	in := "A short intro.\n\nFILE: a.txt\n```\nbody\n```\n"
	summary, _ := ParseCodeFiles(in)
	if !strings.Contains(summary, "A short intro.") {
		t.Errorf("summary = %q, want it to contain the intro", summary)
	}
	if strings.Contains(summary, "FILE:") {
		t.Errorf("summary leaked the file envelope: %q", summary)
	}
}

func TestParseCodeFilesRejectsTraversal(t *testing.T) {
	// A model that tries to escape the project directory must be stopped.
	for _, unsafe := range []string{"../outside.go", "/etc/passwd", "../../x.sh", `C:\Windows\evil.go`} {
		_, files := ParseCodeFiles("FILE: " + unsafe + "\n```\nx\n```\n")
		if len(files) != 0 {
			t.Errorf("unsafe path %q was accepted as %q", unsafe, files[0].Path)
		}
	}
}

func TestSafeRelPath(t *testing.T) {
	safe := []string{"main.go", "src/app/main.go", "a/b/c/d.txt", "./x.go"}
	unsafe := []string{"", ".", "..", "../x", "/abs/x", `C:\x`, "a/../../b"}

	for _, p := range safe {
		if !SafeRelPath(p) {
			t.Errorf("SafeRelPath(%q) = false, want true", p)
		}
	}
	for _, p := range unsafe {
		if SafeRelPath(p) {
			t.Errorf("SafeRelPath(%q) = true, want false", p)
		}
	}
}
