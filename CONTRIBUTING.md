# Contributing to gofer

gofer is a `0.x` project that its author uses every day, so it moves fast: bug reports with a reproduction, small focused pull requests and corrections to the docs are all welcome.

Working with an AI agent? Point it at [`AGENTS.md`](AGENTS.md) first — the project rules (layering, CLI conventions, compatibility policy, shared utilities, test stability) live there. The same ground as this page, in Chinese, is [`docs/guide/development.md`](docs/guide/development.md).

## Build

Prerequisites: Go 1.26+ (see `go.mod`). Node.js and pnpm are needed only when you build the web console.

```bash
make web build                      # web console embedded into the binary, dist/gofer
make install                        # the same, then copy to $GOPATH/bin
go build -o dist/gofer ./cmd/gofer  # skip both: CLI / HTTP API / MCP only
```

`make web` installs the `web/` dependencies with pnpm, builds the console and copies it into `internal/webui/dist`; `make build` links that directory into the binary and compresses it with `upx`. A fresh clone has no built console, so the plain `go build` route shows a placeholder page — fine when you only touch Go, in which case you can also skip `upx` and just run `go build` yourself.

## Tests

```bash
make test                            # the whole Go suite
go test -count=1 ./internal/job      # one package while you iterate
cd web && npx vitest run             # web console unit tests
```

## Checks before you push

- `gofmt -l .` prints nothing (`gofmt -w` the files you touched).
- `go vet ./...` is clean, and the tests of the packages you changed pass — `make test` once the change crosses packages. For anything that touches concurrency or the job lifecycle, `go test -race -count=1` on Linux is worth the wait.
- Changed `web/`: `cd web && npx vue-tsc --noEmit && npx vitest run && npx vite build`.
- Changed a `*.go` file with a platform build tag: `GOOS=darwin go vet ./...` (macOS is only exercised by CI, so a compile error there blocks a release).
- No real tokens, internal hostnames, IP addresses or private project names in code, docs, screenshots or pasted logs. Maintainers also run `sh scripts/check-private-names.sh` before publishing: it greps the tracked files for the names listed in the untracked `.private-names` (format in `.private-names.example`) and exits non-zero on a hit. Without that file it exits 2, which only means "no list here".
- User-visible changes: update the matching file under [`skills/gofer-usage/references/`](skills/gofer-usage/references/) and add an entry under `未发布` in [`CHANGELOG.md`](CHANGELOG.md) in the same change set.

## Commit messages

The history follows conventional commits — `git log --no-merges --oneline` is the best style guide:

```
feat(web): searchable project picker across pages
fix(job): retry worktree add when a registration is half-written
docs: document steward.approval
test(steward): cover the steward approval gate
chore(tracker): batch 22 progress
```

- A type (`feat`, `fix`, `docs`, `test`, `refactor`, `chore`, `perf`) and, where it helps, a scope naming the area.
- Lowercase subject, one line, no trailing period; put the why in the body when it is not obvious from the diff.
- One functional change per commit; no unrelated reformatting, renaming or cleanup mixed in.
- Reference the tracker issue id in the body when the work comes from one.

## Issues and pull requests

- Search the open issues first, and check `docs/` and `gofer <command> -h` before filing — flags are authoritative.
- **Bug report**: use the form. It asks for `gofer --version`, OS and architecture, whether it happened on the server or a worker, the exact commands, and the relevant log lines. Redact tokens, addresses, project and file names before pasting.
- **Feature request**: describe the workflow you are trying to get done and what blocks it today, not only the flag you have in mind. Changes to protocol, config semantics or the permission model need a design note in `docs/design/` before code.
- **Pull request**: keep it to one concern; say what changed, why, and how you verified it (paste the commands and the tail of their output — "tests pass" alone does not tell a reviewer what ran). CI has to be green, and a PR that only reformats unrelated code is not worth the review time.
- **Security problems do not go into an issue** — see [`SECURITY.md`](SECURITY.md).

## License

By contributing you agree that your contribution is licensed under the [MIT License](LICENSE).
