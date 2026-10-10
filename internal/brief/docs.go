package brief

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// docLinesPerFile caps the lines one design doc contributes.
	docLinesPerFile = 15
	// docFilesMax caps how many docs the design section lists.
	docFilesMax = 8
	// docMaxBytes skips files too large to be a design doc.
	docMaxBytes = 2 << 20
)

// docPathRef finds docs/…md paths written in issue fields and comments.
var docPathRef = regexp.MustCompile("docs/[^\\s`'\"<>()（）\\[\\]，、；：]+?\\.md")

// docHit is one doc that mentions the issue (self) or only its parent.
type docHit struct {
	path    string // slash path relative to root
	self    bool   // mentions the issue id itself
	missing bool   // named by the issue but not on disk
	lines   []string
}

// designDocs scans root/docs/**/*.md for the issue id (or its parent id) and adds
// the docs the issue text points at. Each listed doc shows the section headings of
// its hits with the first lines of each section.
func designDocs(root, id, parent string, refs []string) []docHit {
	if root == "" {
		return nil
	}
	var hits []docHit
	listed := map[string]bool{}
	docsDir := filepath.Join(root, "docs")
	_ = filepath.WalkDir(docsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > docMaxBytes {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(data)
		self := mentions(text, id)
		if !self && !mentions(text, parent) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		want := id
		if !self {
			want = parent
		}
		listed[rel] = true
		hits = append(hits, docHit{path: rel, self: self, lines: docSections(text, want)})
		return nil
	})
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].self != hits[j].self {
			return hits[i].self
		}
		// Dated design docs: newest first.
		return hits[i].path > hits[j].path
	})
	// Docs the issue text names but that do not mention the id: list them with their
	// outline so the link is not lost.
	var named []docHit
	for _, ref := range refs {
		for _, p := range docPathRef.FindAllString(ref, -1) {
			if listed[p] {
				continue
			}
			listed[p] = true
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
			if err != nil {
				named = append(named, docHit{path: p, self: true, missing: true, lines: []string{"  （文件不存在）"}})
				continue
			}
			named = append(named, docHit{path: p, self: true, lines: docOutline(string(data))})
		}
	}
	hits = append(named, hits...)
	if len(hits) > docFilesMax {
		hits = hits[:docFilesMax]
	}
	return hits
}

func headingText(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	text := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
	return text, text != ""
}

// docSections renders, for every section holding a line that mentions want, the
// section heading and its first lines (making sure the hit line itself shows), within
// docLinesPerFile.
func docSections(text, want string) []string {
	lines := strings.Split(text, "\n")
	inFence := false
	heading := make([]int, len(lines)) // index of the heading owning each line (-1 none)
	cur := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		if !inFence {
			if _, ok := headingText(line); ok {
				cur = i
			}
		}
		heading[i] = cur
	}
	var order []int
	hitsBySection := map[int][]int{}
	for i, line := range lines {
		if !mentions(line, want) {
			continue
		}
		h := heading[i]
		if _, ok := hitsBySection[h]; !ok {
			order = append(order, h)
		}
		hitsBySection[h] = append(hitsBySection[h], i)
	}
	var out []string
	for _, h := range order {
		if len(out) >= docLinesPerFile {
			break
		}
		title := "（文件开头）"
		if h >= 0 {
			title, _ = headingText(lines[h])
		}
		out = append(out, "  § "+title)
		end := len(lines)
		for i := h + 1; i < len(lines); i++ {
			if heading[i] != h {
				end = i
				break
			}
		}
		shown := map[int]bool{}
		body := 0
		for i := h + 1; i < end && body < 4 && len(out) < docLinesPerFile; i++ {
			if strings.TrimSpace(lines[i]) == "" {
				continue
			}
			out = append(out, "    "+capRunes(strings.TrimSpace(lines[i]), 160))
			shown[i] = true
			body++
		}
		for _, i := range hitsBySection[h] {
			if shown[i] || i == h || len(out) >= docLinesPerFile {
				continue
			}
			out = append(out, "    … "+capRunes(strings.TrimSpace(lines[i]), 160))
			shown[i] = true
		}
	}
	return out
}

// docOutline lists the first headings of a doc (for docs the issue names).
func docOutline(text string) []string {
	var out []string
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if title, ok := headingText(line); ok {
			out = append(out, "  § "+title)
			if len(out) >= docLinesPerFile {
				break
			}
		}
	}
	return out
}
