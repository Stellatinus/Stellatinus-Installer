# Decisions

Why things are the way they are, what is still open and what has not been verified on a real machine. A pull request that makes or changes a design decision updates this page.

Status: **decided** (by the maintainers), **proposed** (my first choice, applied in the code, open to change), **open**.

## Decided

| Decision | Why |
|---|---|
| Single-file installer: the manifest is appended to the engine with a length, sha256 and magic trailer; a damaged one is refused | One file to download, no mismatched engine and manifest, and the engine bytes stay intact so a signature covers it ([embedding.md](embedding.md)) |
| Apps register files they make later; the installer checks every path (inside the location, no symlink, exists, not older than the installation, not someone else's) and registers all or nothing | The removal rule is a safety rule: a caller cannot make the installer own what it does not |
| One shared engine embedded in each app, driven by a manifest; **not a suite installer** | Stellatinus tools are standalone and do not need each other; a suite installer only makes sense when products must work together |
| Two faces: a command line and a setup window | the command line serves silent installs, scripts and Regulus; the window serves people |
| **Go** | one small static binary per system, no runtime to install first, one CI cross-compiles for every system, and Regulus already has verification and swap code in Go |
| Updates of direct builds belong to the installer, as a separate process | if an app crashes or is corrupt it cannot block its own update, and files are not replaced by the running program |
| Microsoft Store builds are **postponed** | the integration is small and can be done later; the manifest keeps `channel` so nothing blocks it ([#8](https://github.com/Stellatinus/Stellatinus-Installer/issues/8)) |
| The setup window looks like the draft in `design/` | sidebar with the steps, one question per step with "why we ask", checks one by one, a progress bar naming what is downloading, an advanced log |
| The window follows the system's light or dark theme | |
| Window text is not selectable; path fields and the advanced log are | a setup screen is not a document, but people copy paths and support needs the log |
| One accent colour per app, from the manifest | one app can be yellow and another blue; the engine derives the rest and refuses a colour it cannot make legible |
| Permission checks are real: a test file is created and deleted in each folder | being administrator does not guarantee writing: read-only folders, network drives, deny rules and Windows controlled folder access exist |
| Removal deletes only what the installer created; adopted and later-added content is never removed | the user's AI models, outputs and settings must be safe |

## Proposed (applied in the code, open to change)

| Decision | Why | Alternative |
|---|---|---|
| Manifest is **JSON** | no dependency, a JSON Schema exists, strict decoding is built into the standard library | YAML is nicer to write but needs a parser and has surprises |
| **Per-user install by default**, per-machine on request | an elevated installer started with another administrator account can write shortcuts and data into the wrong profile (a known pitfall; not verified here) | always run as administrator |
| The installer **downloads** the components in the manifest, each checked against its sha256 | the maintainer described an installer that downloads what it installs | a full offline package, to be added later |
| **Velopack is not used** | its setup has no custom pages (verified in its docs), it has no Linux installer, and two update systems would be one too many; Haedus must migrate away from it (Haedus#56) | Velopack under our wizard for packaging and delta updates |
| The window is the **local web UI** (`internal/localui`) shown by an **app window of Edge, Chrome or Chromium** where one exists, else the default browser; a separate webview program is evaluated only if real machines show that is not good enough ([window.md](window.md)) | same look everywhere, no new GUI framework, no cgo (the command line stays one static binary cross-compiled from one machine), and the interface is served with a single-use address, a session cookie, Host and Origin checks and a strict Content-Security-Policy; Fyne's accessibility is a first pass, Avalonia is a new framework, MAUI has no Linux target | a webview window per system (Wails v3 or `webview/webview`): needs cgo on macOS and Linux and a build job per system; untried |
| Delivery order: install, uninstall and command line on Windows; then the updater; then Linux and macOS | the first adopter is Haedus on Windows | |
| **Install policy:** never overwrite a file that is not the installer's; a file already in place with the right hash is skipped and adopted; a second run over a recorded installation is refused | the user's AI models and files must be safe; update belongs to the updater ([#7](https://github.com/Stellatinus/Stellatinus-Installer/issues/7)) and needs the previous version kept (repair, which restores what is installed, is built) | replace files in place |
| **Downloads:** https only (also after a redirect), size capped by the manifest, resumed only when the server's `Content-Range` matches, the whole file hashed at the end, a stall (no bytes for 30 s) is a failure, at most four attempts | a malicious or broken server must not fill the disk or hand over a different file | trust the server's `Content-Length`; hash only the new bytes |
| **Zip safety:** every entry is checked before anything is written (path, link, special file, case clash, count, total size, compression ratio over 500 to 1 above 4 MiB); unpacking goes through a temporary name | a hostile archive must not write outside the folder or exhaust the disk | unpack and then check |
| **The record is saved after every step and a failed run is rolled back** | an interrupted installation can then be removed with the record | clean up only on success |
| **`uninstall` only shows the plan unless `--yes` is given**, and a plan always lists what is protected | a command that deletes should not do it by accident; `plan` stays for a record path | ask a question (a prompt would break silent mode) |
| **Exit codes** 0 ok, 1 a problem with nothing changed, 2 usage, 3 failed and rolled back, 4 failed and not fully rolled back, 5 removal left items ([cli.md](cli.md)) | a script must tell "nothing happened" from "undo it" from "finish removing" | one non-zero code for every failure |
| **The record lives outside the chosen folders** (`<config>/Stellatinus/<app id>/record.json`, machine-wide for all users) and is kept when a removal leaves items | removing an installation must never remove its own record first, and an install or removal that stops half way can be finished | store it in the install folder |
| **Options and config files:** a port is a manifest option that is checked by really listening, and a small JSON file is written for the app from the answers, never over an existing file | Mira needs its port in `config.json` before it starts, and "a free port" must be true, not just plausible | each app writes its own config after install; check only that the number is in range |
| **Shortcuts are made only when asked, are never written over an existing file and never fail an installation for a reason that is not the user's**; the manifest names the program with `launch`; the `.lnk` is written by hand (no Windows call, no helper program) | a silent install must not decorate the desktop; a shortcut that replaced someone's file or pointed at nothing would be a worse failure than no shortcut; a helper such as PowerShell is slower, noisier for antivirus and one more thing to trust | `WScript.Shell` through PowerShell; make shortcuts by default |
| **Repair restores only recorded, installer-owned files that are missing or changed, shows before it acts (`--yes`), never touches a conflict, and does not update** | the record now keeps each file's sha256, the folders chosen and the answers, so damage can be told from intact files and from the user's edits; a settings file the user edited is theirs; going to another version needs the previous version kept and belongs to the updater ([#7](https://github.com/Stellatinus/Stellatinus-Installer/issues/7)) | restore the whole package; overwrite whatever is there |
| **A location has `default_machine` next to `default`**, required for every system when the manifest is or allows `machine`; there is no fallback to the per-user folder | an all-users installation proposing a folder in one user's profile is wrong, and the privilege check would pass or fail for the wrong reason ([#38](https://github.com/Stellatinus/Stellatinus-Installer/issues/38)) | a nested `default: {user, machine}` (breaks every manifest); a silent fallback |
| **Each run that changes something or fails writes a log (JSON lines) next to the record**; the last 5 are kept; a plan or dry run writes none; a log that cannot be written never changes the outcome | a person who double-clicks the installer sees a console that closes and support has nothing to ask for; it sits with the record so a removal never takes it ([#41](https://github.com/Stellatinus/Stellatinus-Installer/issues/41)) | a log in the install folder; a log for every command; failing the run when the log fails |
| **Components have `platforms`**; only those for the running system are downloaded and counted | an app ships a `.zip` for Windows and a `.tar.gz` for Linux; one manifest must serve all of them | one manifest per system |
| **Translations are in the manifest** (`texts`), English is the base, and the project's own examples must be fully Italian (a test) | the window is bilingual; the manifest is where an app's own words live | a separate translation file per app |
| **Apache-2.0** | the same as Regulus and Haedus | to be confirmed by the maintainer |
| CI produces **build artifacts for every push**, and **on every push to `main` of this repository a rolling pre-release `test-builds`** with unsigned test installers (a fake demo app packed for the six systems, the plain command line, checksums) | the maintainer cannot otherwise take a build to a machine: artifacts need a login, are zipped and expire. The pre-release is replaced on every push, so the link never changes; only that job has `contents: write`, never for a pull request; they are labelled unsigned and are to stop before a first real release, when signing exists ([#9](https://github.com/Stellatinus/Stellatinus-Installer/issues/9), [#32](https://github.com/Stellatinus/Stellatinus-Installer/issues/32)) | artifacts only; a real release |
| **Go 1.26 or newer** (minimum in `go.mod`, and what CI uses) | the same as Regulus. `govulncheck` found a standard-library vulnerability (GO-2026-4602, `os`) fixed in Go 1.25.8 and 1.26.1, so builds must use a patched toolchain (the installer now uses `net/http`, `crypto/tls` and `archive/zip`, and Go 1.26.11 or later was needed to be clean; CI takes the newest 1.26 patch); the `golang.org/x/sys` update that Dependabot proposed also raised the minimum to 1.26 and `golangci-lint` before v2.15 cannot read a 1.26 module | keep a lower minimum such as 1.24, which would force a patched toolchain in CI anyway and block the dependency updates |
| **GitHub Actions are pinned by commit hash**, with the release in a comment (`uses: actions/checkout@<40 hex> # v7.0.1`); a CI job fails on any action that is not | a tag can be moved by whoever owns the action; Dependabot's `github-actions` updates keep hash and comment current (the same as Regulus#206) | the major-version tags the first workflows used |

## Open

- Whether the **taskbar** can be pinned at all: Windows has no supported way to pin from an installer (not verified here). **Decided for now:** the engine never pins; it says so in a note and the window shows a hint on the last screen. Reopen only if a real test shows a safe way ([#10](https://github.com/Stellatinus/Stellatinus-Installer/issues/10)).
- **Signing**: Authenticode for Windows needs a certificate; Azure Artifact Signing accepts organizations in the EU but not individuals. Sigstore for provenance ([#9](https://github.com/Stellatinus/Stellatinus-Installer/issues/9)).
- **Updater** trust model and rollback ([#7](https://github.com/Stellatinus/Stellatinus-Installer/issues/7)).
- **Linux and macOS** installers: what "start menu" and "dock" mean ([#10](https://github.com/Stellatinus/Stellatinus-Installer/issues/10)).
- The **position of the window buttons** on Windows (left in the draft, right on Windows): to review per platform.

## Not verified on a real machine

Everything. The tests run on Linux in the cloud session and on Linux, Windows and macOS runners in CI. What a real machine can still change is listed in [#13](https://github.com/Stellatinus/Stellatinus-Installer/issues/13): the write check under real ACLs, UAC, Windows controlled folder access, antivirus behaviour, the window itself with a screen reader, and the updater under a power cut.

The write check's "permission denied" case was exercised on Linux as an unprivileged user; the same case on Windows has not been run.

## Milestones

Work is grouped in milestones, in this order. An epic ([#14](https://github.com/Stellatinus/Stellatinus-Installer/issues/14)) spans them and has none.

| Milestone | What it holds | Issues |
|---|---|---|
| **M1 Foundations** | repository, manifest format, accent colours, pre-install checks, installation record, first command line, locations design | #1, #12, #15 (done) |
| **M2 Install and remove (command line, Windows first)** | download and install components, install and uninstall from the command line with a silent mode, the embedding contract, the rest of the manifest, repair, and the real-machine checks of permissions on Windows | #6, #10, #18, #26, #27, #32, #39, #40 (#38 and #41 done; #2, #4, #16, #17, #19 done; #6, #10, #26 and #32 are built and wait for the real-machine checks #18, #27, #39 and #40) |
| **M3 Setup window** | the window technology (after a spike), the wizard, accessibility, themes, English and Italian | #3, #5, #29 |
| **M4 Updates and signing** | the updater, restoring the previous version, and the signing and trust model | #7, #9, #42 |
| **M5 Adopters, Linux and macOS** | Regulus Desktop, Haedus, Mira and Polaris adopt the installer; other systems; the real-machine plan across systems | #11, #13, #33, #35 |
| **Later: Microsoft Store** | Store builds | #8 |

New issues pick the milestone whose work they belong to; if none fits, say so in the issue instead of stretching one.
