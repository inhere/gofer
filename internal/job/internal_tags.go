package job

import "github.com/inhere/gofer/internal/jobstore"

// MessengerJobTag marks the delivery record created when the web console sends
// a message to an existing terminal session. It is an internal implementation
// tag: ordinary job lists hide it, while --all and job details retain it.
const MessengerJobTag = jobstore.TagSessionMessenger

// WorkSummarizerJobTag marks a work-item summarizer (tidy-up) job, which gofer
// submits on its own: hidden from ordinary job lists and the Board like the
// messenger record, visible with --all and in job detail.
const WorkSummarizerJobTag = jobstore.TagWorkSummarizer

// TrackerSyncJobTag marks a server-dispatched `gofer repo sync` job (web Issues
// page sync button): hidden from ordinary lists and the Board like the others.
const TrackerSyncJobTag = jobstore.TagTrackerSync

// hiddenJobTag reports whether a job carries any internal tag.
func hiddenJobTag(tags []string) bool {
	for _, t := range tags {
		for _, h := range jobstore.InternalJobTags {
			if t == h {
				return true
			}
		}
	}
	return false
}
