# Notes for AI coding agents

Everything an agent needs to work on this repository is in it. Read these before starting:

- [README.md](README.md): what this is, what exists today and what does not.
- [CONTRIBUTING.md](CONTRIBUTING.md): setup, commands, branches, commits, language.
- [docs/architecture.md](docs/architecture.md): the parts and how they fit.
- [docs/manifest.md](docs/manifest.md): the manifest format.
- [docs/decisions.md](docs/decisions.md): why things are the way they are, what is open, what has not been verified on a real machine.
- [docs/glossary.md](docs/glossary.md): what each term means today; add new terms here.
- [docs/design/](docs/design/README.md): the interactive draft of the setup window.

If you learn something that matters for future work and it is not written in one of these files, add it to the right one in the same pull request. Do not rely on context outside the repository.

## How to work

- **One pull request per issue**, referenced with `Closes #N`. English only for code, comments, commits, issues, pull requests and documents; only the setup window is bilingual (English and Italian).
- **Checks:** run `make check` before pushing. CI runs on every pull request and must be green; do not add `[skip ci]` to commits.
- **Package comments:** every package under `internal/` and `cmd/` has a comment that says what it does and what it does not. `golangci-lint` fails when one is missing; keeping it true is your job.
- **Decisions:** when a pull request makes or changes a design decision, update `docs/decisions.md` in the same pull request. If code and a document disagree, say so and fix the one that is wrong, or open an issue if it needs a decision.
- **New areas start with a design issue** and agreement before code. The updater, the signing model and the setup window each have one.
- **`[Test]` issues:** when a change needs a check on a real machine, open an issue titled `[Test] …` (label `test`) with what is needed, the steps, the expected result and what to report back, and say in the pull request that this part is not verified.
- **Do not overstate verification.** Say what was tested and how. Nothing has run on a real Windows, macOS or Linux machine yet; do not describe it as verified.
- **The removal rule is a safety rule:** only entries owned by the installer are ever deleted, and no path may be followed through a symbolic link. Do not weaken it without an issue and a test.
- **Manifest, schema, docs and examples change together.**

## Reporting back

At the end of a piece of work, report:

1. a **completeness score from 0 to 100** for the area you worked on, with the list of what is missing to reach 100;
2. an **issue for each gap** you found (in English, with a "Done when" line), unless one already exists;
3. the **milestone** you chose for each new issue and why (the milestones and what each holds are in the table at the end of [docs/decisions.md](docs/decisions.md)).
