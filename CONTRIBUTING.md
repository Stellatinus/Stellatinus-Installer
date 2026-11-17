# Contributing to Stellatinus Installer

Thanks for helping. This guide covers setup, how we work and what a good pull request looks like.

## Setup

Requirements: Go 1.24 or newer, `make`, and `golangci-lint` v2 for linting.

```sh
git clone https://github.com/Stellatinus/Stellatinus-Installer.git
cd Stellatinus-Installer
make build   # bin/stellatinus-installer
make test    # go vet and go test -race
make lint    # golangci-lint
make check   # lint and test: what CI runs
```

See [docs/architecture.md](docs/architecture.md) for the parts and [docs/decisions.md](docs/decisions.md) for the reasons behind the choices and the open questions. AI coding agents: see [AGENTS.md](AGENTS.md).

## How we work

- **One pull request per issue.** Pick an issue (or open one), say you are working on it, and reference it with `Closes #N`.
- **Branches** are named after the work, for example `feat/manifest-fields` or `fix/write-check`.
- **Commits** follow [Conventional Commits](https://www.conventionalcommits.org/): `feat(checks): ...`, `fix(record): ...`, `docs: ...`, `ci: ...`, `test: ...`.
- **Run `make check` before pushing.** CI runs the same checks on Linux, Windows and macOS and must be green to merge.
- **Language:** everything is in English: code, comments, commit messages, issues, pull requests and documents. The only bilingual part is the setup window, which supports English and Italian; a pull request that adds or changes a user-facing text provides both.
- **Package comments:** every package has a comment that says what it does and what it does not. The linter fails when one is missing; keeping it true is your job.
- **`[Test]` issues** are checks that need a real machine (a clean Windows 11, a screen reader, a power cut during an update) and cannot run in CI. Title: `[Test] …`, label `test`. The body says what is needed, the steps, the expected result and what to report back. A pull request that adds behaviour only a real machine can confirm opens one and does not call that part verified.
- **Decisions:** a pull request that makes or changes a design decision updates [docs/decisions.md](docs/decisions.md).
- **Manifest changes:** update `internal/manifest`, `schema/manifest.schema.json`, `docs/manifest.md` and the examples together; a test fails if the schema and the code disagree.
- Keep changes focused. Unrelated clean-ups belong in their own pull request.

## Pull request checklist

- [ ] The change is covered by tests, or you explain why it is not.
- [ ] `make check` passes locally.
- [ ] Documents are updated (README, comments, this guide) when behaviour changes.
- [ ] The description says what changed and why, and how you tested it, and does not call something verified that was not run on a real machine.

## Reporting security problems

Do not open a public issue. See [SECURITY.md](SECURITY.md).

## Licence

By contributing you agree that your contributions are licensed under the [Apache License 2.0](LICENSE).
