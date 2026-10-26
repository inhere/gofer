## Gofer Issue Tracker

This repository tracks its tasks and memories with `gofer issue` / `gofer memory` (data in `.gofer/tracker/`; the context is injected when a session starts).

```bash
gofer issue ready                        # issues that can start (open, not blocked)
gofer issue show <id>                    # details: comments / dependencies / parent and children
gofer issue create "title" -p 1 -l tag   # new issue (-l tags it into a sub-project)
gofer issue update <id> --claim          # claim it and start working
gofer issue comment <id> "progress"      # append a comment
gofer issue close <id> --reason "..."    # done
gofer memory set <key> "text" --summary "one sentence"  # remember a lesson; recall with gofer memory ls <keyword> / show <key>
```

- Track every task with `gofer issue`; do not keep a separate markdown TODO list. Lasting knowledge goes into `gofer memory`.
- memory write hint
- commit policy line
- To see what changed in the tracker run `gofer repo status --changed` (it lists the added, updated and removed issues / memories); do **not** `git diff` / `cat` `.gofer/tracker/*.jsonl`, whole JSON lines flood the context.
