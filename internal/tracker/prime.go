package tracker

import (
	"fmt"
	"sort"
	"strings"
)

const PrimeMaxBytes = 8 << 10

// PrimeWithHandoffSection appends the caller-provided best-effort server handoff
// section while preserving the existing prime bytes first. The caller is expected
// to have applied project selection, timeout and ordering before this seam.
func (s *Store) PrimeWithHandoffSection(handoff string) (string, error) {
	base, err := s.Prime()
	if err != nil {
		return "", err
	}
	section := "\n## 进行中 plan 的交接说明\n\n" + handoff
	if len([]byte(base))+len([]byte(section)) <= PrimeMaxBytes {
		return base + section, nil
	}
	remaining := PrimeMaxBytes - len([]byte(base)) - len([]byte("\n## 进行中 plan 的交接说明\n\n"))
	if remaining < 0 {
		return base, nil
	}
	cut := []byte(handoff)
	if len(cut) > remaining {
		cut = cut[:remaining]
	}
	return base + "\n## 进行中 plan 的交接说明\n\n" + string(cut) + "\n[交接说明已截断]\n", nil
}

func CommitPolicyText(policy string) (string, error) {
	switch policy {
	case "", "local-commit":
		return "按功能点本地提交是默认授权；不要 push；`.gofer/tracker/*.jsonl` 的变化随功能点一起提交；提交前 `git status` 确认没有夹带无关文件。", nil
	case "ask":
		return "提交前先询问用户；不要 push；`.gofer/tracker/*.jsonl` 的变化随获批功能点一起提交。", nil
	case "none":
		return "本仓库不提交本次改动；不要 push。", nil
	default:
		return "", fmt.Errorf("unknown commit_policy %q", policy)
	}
}

func (s *Store) Prime() (string, error) {
	cfg, err := s.ReadConfig()
	if err != nil {
		return "", err
	}
	policy, err := CommitPolicyText(cfg.CommitPolicy)
	if err != nil {
		return "", err
	}
	issues, err := s.ReadIssues()
	if err != nil {
		return "", err
	}
	ready, err := s.Ready()
	if err != nil {
		return "", err
	}
	memories, err := s.ReadMemories()
	if err != nil {
		return "", err
	}
	sort.Slice(memories, func(i, j int) bool {
		if memories[i].UpdatedAt != memories[j].UpdatedAt {
			return memories[i].UpdatedAt > memories[j].UpdatedAt
		}
		return memories[i].Key < memories[j].Key
	})
	active := make([]string, 0)
	for _, item := range issues {
		if item.Status == "in_progress" || (item.Assignee != "" && item.Status != "closed") {
			active = append(active, fmt.Sprintf("- %s [%s] %s\n", item.ID, item.Status, item.Title))
		}
	}
	readyLines := make([]string, 0, 10)
	for i, item := range ready {
		if i >= 10 {
			break
		}
		readyLines = append(readyLines, fmt.Sprintf("- %s P%d %s\n", item.ID, item.Priority, item.Title))
	}
	const headings = "## 进行中/已认领 issue\n\n## ready 前 10\n\n## memory\n"
	const notice = "\n[内容已截断：优先保留最新 memory，issue 已截断]\n"
	base := "## 提交策略\n" + policy + "\n\n" + headings
	budget := PrimeMaxBytes - len([]byte(base)) - len([]byte(notice))
	if budget < 0 {
		return "", fmt.Errorf("commit policy exceeds prime limit")
	}
	selectedMem := make([]string, 0, len(memories))
	used := 0
	truncated := false
	for _, item := range memories {
		line := fmt.Sprintf("- %s: %s\n", item.Key, item.Content)
		if used+len([]byte(line)) > budget {
			truncated = true
			continue
		}
		selectedMem = append(selectedMem, line)
		used += len([]byte(line))
	}
	selectedActive := make([]string, 0, len(active))
	selectedReady := make([]string, 0, len(readyLines))
	for _, group := range []struct {
		all []string
		out *[]string
	}{{active, &selectedActive}, {readyLines, &selectedReady}} {
		for _, line := range group.all {
			if used+len([]byte(line)) > budget {
				truncated = true
				continue
			}
			*group.out = append(*group.out, line)
			used += len([]byte(line))
		}
	}
	var out strings.Builder
	out.WriteString("## 提交策略\n")
	out.WriteString(policy)
	out.WriteString("\n\n## 进行中/已认领 issue\n")
	for _, line := range selectedActive {
		out.WriteString(line)
	}
	out.WriteString("\n## ready 前 10\n")
	for _, line := range selectedReady {
		out.WriteString(line)
	}
	out.WriteString("\n## memory\n")
	for _, line := range selectedMem {
		out.WriteString(line)
	}
	if truncated {
		out.WriteString(notice)
	}
	return out.String(), nil
}

// PrimeRule is the subset included in a dispatched job's mandatory rules.
func (s *Store) PrimeRule() (string, error) {
	cfg, err := s.ReadConfig()
	if err != nil {
		return "", err
	}
	policy, err := CommitPolicyText(cfg.CommitPolicy)
	if err != nil {
		return "", err
	}
	memories, err := s.ReadMemories()
	if err != nil {
		return "", err
	}
	sort.Slice(memories, func(i, j int) bool { return memories[i].UpdatedAt > memories[j].UpdatedAt })
	var out strings.Builder
	out.WriteString("提交策略：")
	out.WriteString(policy)
	out.WriteString("\nmemory：\n")
	for _, item := range memories {
		line := fmt.Sprintf("- %s: %s\n", item.Key, item.Content)
		if out.Len()+len([]byte(line)) > PrimeMaxBytes-64 {
			out.WriteString("[memory 已截断，优先保留最新]\n")
			break
		}
		out.WriteString(line)
	}
	return out.String(), nil
}
