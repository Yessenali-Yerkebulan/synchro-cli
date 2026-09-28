package agents

import (
	"path"
	"regexp"
	"strings"
)

// filePattern matches one "FILE: path" + fenced-block pair.
// Ported from synchro/app/utils/code_files.py:FILE_PATTERN.
var filePattern = regexp.MustCompile("(?s)FILE:\\s*(\\S+)\\s*\\n```[a-zA-Z0-9_+-]*\\n(.*?)```")

// CodeFile is one file recovered from an agent's answer.
type CodeFile struct {
	Path    string
	Content string
}

// ParseCodeFiles splits a Developer agent's response into a summary and the
// files it produced.
//
// If the model did not follow the FILE: convention the text is returned
// unchanged with no files, so the caller can show the raw answer rather than
// reporting a spurious failure.
func ParseCodeFiles(text string) (string, []CodeFile) {
	matches := filePattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil
	}

	summary := strings.TrimSpace(text[:matches[0][0]])

	var files []CodeFile
	for _, m := range matches {
		p := strings.TrimSpace(text[m[2]:m[3]])
		content := strings.TrimRight(text[m[4]:m[5]], "\n")
		if !SafeRelPath(p) {
			// A model occasionally emits an absolute path or a parent
			// traversal. Skip it rather than writing outside the repo.
			continue
		}
		files = append(files, CodeFile{Path: path.Clean(strings.ReplaceAll(p, "\\", "/")), Content: content})
	}
	return summary, files
}

// SafeRelPath reports whether p is a relative path that stays inside the repo.
func SafeRelPath(p string) bool {
	if p == "" {
		return false
	}
	// Reject Windows drive letters and UNC paths, not just leading slashes.
	if strings.Contains(p, ":") {
		return false
	}
	cleaned := path.Clean(strings.ReplaceAll(p, "\\", "/"))
	if cleaned == "." || cleaned == ".." {
		return false
	}
	if strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return false
	}
	return true
}
