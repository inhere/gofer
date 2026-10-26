## What changed

<!-- One paragraph. Link the issue when there is one: Fixes #123 -->

## Why

<!-- The problem this solves, or the behaviour that was wrong. -->

## How it was verified

<!-- The commands you ran and the tail of their output — not just "tests pass". -->

```text
$ go test -count=1 ./internal/job
ok  	github.com/inhere/gofer/internal/job
```

## Checklist

- [ ] `gofmt -l .` prints nothing, `go vet ./...` is clean, and the tests of the touched packages pass
- [ ] `web/` touched → `npx vue-tsc --noEmit && npx vitest run && npx vite build`
- [ ] user-visible change → `skills/gofer-usage/references/` updated and an entry under `未发布` in `CHANGELOG.md`
- [ ] HTTP route added or changed → declared in `internal/httpapi/authz_routes.go`, `docs/reference/permissions.md` regenerated
- [ ] no tokens, internal host names, IP addresses or private project names in the diff, docs or screenshots
- [ ] `AGENTS.md` read for the project rules (layering, CLI conventions, compatibility policy, test stability)
