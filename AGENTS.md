# Repository Guidelines

## Project Purpose & Agent Output
- `ap-query` is built for LLM agents analyzing async-profiler recordings.
- Keep outputs compact and directly actionable to avoid wasting tokens.
- Use concise plain text in terminal workflows.
- Never use JSON for agent-facing guidance, examples, or command output formatting in this repository.
- Plain text is required because LLM agents parse it well and it is more concise.

## Project Structure & Module Organization
- The repository is a single Go CLI module (`go.mod`) with command dispatch in `main.go`.
- Commands are split into root-level files like `hot.go`, `tree.go`, `diff.go`, `events.go`, and `init.go`.
- Tests live primarily in `main_test.go`; profiling fixtures are in `testdata/` (`*.jfr`, `*.jfr.gz`).
- Fixture generation utilities are under `testdata/gen/` (`generate.sh`, `Workload.java`).
- CI and release automation are defined in `.github/workflows/` and `.goreleaser.yml`.

## Build, Test, and Development Commands
- `go build -o ap-query .` builds the local binary.
- `go test -v ./...` runs the full test suite.
- `go test -run TestName -v` runs a targeted test while iterating.
- `echo "A;B;C 10" | ./ap-query hot -` runs a quick smoke check for collapsed-stack input.
- `./testdata/gen/generate.sh /path/to/libasyncProfiler.so` regenerates JFR fixtures (Java 17+ and async-profiler required).

## Coding Style & Naming Conventions
- Follow idiomatic Go formatting and structure; run `gofmt` before committing.
- Use tabs/standard Go formatting and keep functions small and command-focused.
- Keep file names lowercase and command-oriented (`callers.go`, `lines.go`, `filter.go`).
- Prefer clear lowerCamelCase helper names and concise error messages.

## Testing Guidelines
- Test extensively: add both unit tests and integration-style CLI tests for all meaningful changes.
- Include edge cases (empty input, invalid flags, missing files, unknown events, tiny/huge sample counts) and regression cases for fixed bugs.
- Write table-driven tests with `TestXxx` naming in `main_test.go`.
- Cover both data-path logic and CLI behavior (flags, output, and failure paths).
- When adding profiler scenarios, store fixtures in `testdata/` and regenerate via `testdata/gen/generate.sh` instead of hand-editing binaries.
- Before opening a PR, run `go test -v ./...` and at least one CLI smoke command.

## Commit & Pull Request Guidelines
- Match history style: short, imperative commit subjects (example: `readme tweak`).
- Keep each commit focused on one logical change.
- PRs should include: purpose, behavioral impact, and test proof (commands run).
- Include sample CLI output when changing output format or flags, and link related issues when applicable.
