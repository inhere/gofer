package job

// The prompt texts of a CONTINUATION (design 2026-10-27-i18n-design.md §4, P1d).
//
// Three different purposes, so three different templates — an agent reading one of
// them must never get the wording of another:
//
//   - NameAutoResume: gofer itself continues a run a transient error interrupted;
//   - NameUncommittedResume: the continuation first has to commit what the previous
//     run left behind;
//   - NameTodoContinue: a plan item continues the session of the item before it (the
//     bridging line its task book follows).
//
// The text lives in internal/i18n/prompts, one template per language; the language is
// the one of the job's own text (promptLanguage over the source job's request), so a
// continuation keeps speaking what its source was written in even after the server's
// language changed.

import (
	"strings"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/i18n/prompts"
)

// autoResumePrompt is the prompt of an automatic continuation after a transient error:
// the error that hit, so the agent can see what interrupted it instead of guessing.
func autoResumePrompt(cfg *config.Config, src JobResult, hit string) string {
	return prompts.MustRender(jobPromptLanguage(cfg, src), prompts.NameAutoResume,
		prompts.AutoResumeData{Hit: strings.TrimSpace(hit)})
}

// uncommittedResumePromptText is the prompt of a continuation that has to commit what
// the previous run left uncommitted: the count and the first files (count is the full
// number of changes, files the ones worth naming).
func uncommittedResumePromptText(lang i18n.Lang, count int, files []string) string {
	return prompts.MustRender(lang, prompts.NameUncommittedResume,
		prompts.UncommittedResumeData{Count: count, Files: files})
}

// todoContinuePrompt is the bridging line a plan item's prompt opens with: the session
// already knows the item before, the task book that follows is the new one.
func todoContinuePrompt(lang i18n.Lang, title string) string {
	return prompts.MustRender(lang, prompts.NameTodoContinue, prompts.TodoContinueData{Title: title})
}

// jobPromptLanguage is the language of an existing job's text: the one fixed when the
// job was admitted (JobRequest.Language), else the language its project / server asks
// for now. The project is the request's own, else the one recorded on the job.
func jobPromptLanguage(cfg *config.Config, src JobResult) i18n.Lang {
	req := requestOfJob(src)
	if req.ProjectKey == "" {
		req.ProjectKey = src.ProjectKey
	}
	return promptLanguage(cfg, &req)
}
