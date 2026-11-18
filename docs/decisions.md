# Decisions

Why things are the way they are, what is still open and what has not been verified on a real machine. A pull request that makes or changes a design decision updates this page.

Status: **decided** (by the maintainers), **proposed** (my first choice, applied in the code, open to change), **open**.

## Decided

| Decision | Why |
|---|---|
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
| The window is a **web UI in a system window** (webview) | same look everywhere, no new GUI framework, accessibility from the web view; Fyne's accessibility is a first pass and off by default, Avalonia is a new framework, MAUI has no Linux target | a native UI per system; Wails v3 or `webview/webview` is chosen with a spike ([#3](https://github.com/Stellatinus/Stellatinus-Installer/issues/3)) |
| Delivery order: install, uninstall and command line on Windows; then the updater; then Linux and macOS | the first adopter is Haedus on Windows | |
| **Apache-2.0** | the same as Regulus and Haedus | to be confirmed by the maintainer |
| CI produces **build artifacts only**, no releases and no packages | nothing consumes them yet; releases come with signing ([#9](https://github.com/Stellatinus/Stellatinus-Installer/issues/9)) when the first adopter must download the engine | |
| **Go 1.26 or newer** (minimum in `go.mod`, and what CI uses) | the same as Regulus. `govulncheck` found a standard-library vulnerability (GO-2026-4602, `os`) fixed in Go 1.25.8 and 1.26.1, so builds must use a patched toolchain; the `golang.org/x/sys` update that Dependabot proposed also raised the minimum to 1.26 and `golangci-lint` before v2.15 cannot read a 1.26 module | keep a lower minimum such as 1.24, which would force a patched toolchain in CI anyway and block the dependency updates |
| GitHub Actions are referenced by major version, not by commit | the commit hashes could not be looked up from where this was written; Dependabot keeps them current | pin by hash (Regulus#206 does) |

## Open

- Whether the **taskbar** shortcut works at all: Windows has no supported way to pin from an installer (not verified here). Fallback: a hint on the last screen ([#10](https://github.com/Stellatinus/Stellatinus-Installer/issues/10)).
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
| **M2 Install and remove (command line, Windows first)** | download and install components, install and uninstall from the command line with a silent mode, the embedding contract, the rest of the manifest, repair, and the real-machine checks of permissions on Windows | #2, #4, #6, #10, #16, #17, #18, #19 |
| **M3 Setup window** | the window technology (after a spike), the wizard, accessibility, themes, English and Italian | #3, #5 |
| **M4 Updates and signing** | the updater, and the signing and trust model | #7, #9 |
| **M5 Adopters, Linux and macOS** | Regulus Desktop, Haedus, Mira and Polaris adopt the installer; other systems; the real-machine plan across systems | #11, #13 |
| **Later: Microsoft Store** | Store builds | #8 |

New issues pick the milestone whose work they belong to; if none fits, say so in the issue instead of stretching one.
