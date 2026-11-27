# Platforms and privileges

**Status: proposed.** What the code does today is stated as fact; what is a choice waiting for the maintainers is marked *proposed*; what only a real machine can settle is marked *not verified* and tracked in [#13](https://github.com/Stellatinus/Stellatinus-Installer/issues/13), [#18](https://github.com/Stellatinus/Stellatinus-Installer/issues/18), [#27](https://github.com/Stellatinus/Stellatinus-Installer/issues/27) and [#29](https://github.com/Stellatinus/Stellatinus-Installer/issues/29).

## Systems

| System | Built and tested in CI | The engine does today | Delivery order |
|---|---|---|---|
| **Windows** (amd64, arm64) | `go vet` and the tests on a Windows runner | everything: checks, install, uninstall, repair, shortcuts (`.lnk`), single-file installer | first |
| **Linux** (amd64, arm64) | the tests, with the race detector | the same, with `.desktop` shortcuts | after Windows |
| **macOS** (amd64, arm64) | the tests, with the race detector | the same, with a link as the shortcut | after Linux |

The engine is one static Go binary per system and architecture, with no cgo and no runtime to install. The minimum versions are those of the Go toolchain in `go.mod` (Go 1.26); the exact oldest Windows, macOS and Linux kernel that works has **not** been checked here. Which Linux distributions and package formats to support is **open**: the engine itself needs neither, and the headless servers of Regulus keep their `install.sh` and `.deb` ([#11](https://github.com/Stellatinus/Stellatinus-Installer/issues/11)).

**Not supported, on purpose:** 32-bit systems; Microsoft Store builds (postponed, [#8](https://github.com/Stellatinus/Stellatinus-Installer/issues/8)); services and autostart ([#33](https://github.com/Stellatinus/Stellatinus-Installer/issues/33)); file associations; components as `.tar.gz` ([#35](https://github.com/Stellatinus/Stellatinus-Installer/issues/35)), so today an adopter can ship only `.zip` and single files on every system.

## Per user and for all users

**Per user is the default** (*proposed*, applied in the code): it needs no administrator, and an elevated installer started from another administrator account can write shortcuts and data into the wrong profile (a known pitfall, *not verified here*). **All users** (`--scope machine`) is available only if the manifest says `allow_machine`, and then needs an elevated process. It proposes the folders of `default_machine`, never the per-user ones.

| | per user | all users |
|---|---|---|
| Privileges | none | the privilege check fails unless the process is elevated: administrator on Windows, root on Linux and macOS |
| Where the record goes | `%AppData%\Stellatinus\<id>\record.json`, `~/.config/Stellatinus/<id>/record.json` (or `$XDG_CONFIG_HOME`), `~/Library/Application Support/Stellatinus/<id>/record.json` | `%ProgramData%\Stellatinus\<id>\record.json`, `/var/lib/Stellatinus/<id>/record.json`, `/Library/Application Support/Stellatinus/<id>/record.json` |
| Start menu shortcut | `%APPDATA%\Microsoft\Windows\Start Menu\Programs`, `~/.local/share/applications`, `~/Applications` | `%ProgramData%\Microsoft\Windows\Start Menu\Programs`, `/usr/share/applications`, `/Applications` |
| Desktop shortcut | `%USERPROFILE%\Desktop`, the folder in `user-dirs.dirs` or `~/Desktop`, `~/Desktop` | `%PUBLIC%\Desktop`; **none** on Linux and macOS |

**Gap:** a manifest has one default folder per system, not one per scope, so an all-users installation proposes the same folders as a per-user one (for Haedus, a folder under the user's profile). The user can choose another, but the proposal is wrong; per-scope defaults are tracked in [#38](https://github.com/Stellatinus/Stellatinus-Installer/issues/38).

**Elevation.** Today the engine only *reports* a missing privilege (`Insufficient privileges`, exit 1, nothing changed) and tells the user to run again as administrator. Asking for it (UAC, `pkexec`, the macOS prompt) is [#34](https://github.com/Stellatinus/Stellatinus-Installer/issues/34).

**Whatever the scope**, the installer checks the privilege and then **really writes and deletes a test file** in each folder before installing anything, because being administrator does not guarantee writing (read-only folders, network drives, deny rules, Windows controlled folder access). Tested on Linux as root and as an unprivileged user; the Windows cases are [#18](https://github.com/Stellatinus/Stellatinus-Installer/issues/18).

## Where things go

| What | Windows | Linux | macOS |
|---|---|---|---|
| The program (`install` location) | the manifest's default, for the examples `%LOCALAPPDATA%\<App>` | `~/.local/share/<app>` | `~/Applications/<App>` |
| Data and settings | a `data` location, `%APPDATA%\<App>` in the examples | `~/.config/<app>` or `~/.local/share/<app>` | `~/Library/Application Support/<App>` |
| Models and outputs the user may already have | a location with `adopt`, never removed | the same | the same |
| The installation record | see the table above | | |
| Installer logs | **none yet**: the output of the command (`--json` for scripts) is the log, and the record says what was made | | |

The examples in [`examples/`](../examples) show one choice per adopter; the manifest decides, and a test requires that the default folders of an app do not overlap on any system, for each scope. Nothing is ever written outside the folders the user chose, the record's folder and the shortcut folders above.

## A machine with no display

The **command line** works anywhere: `install --silent`, `uninstall --yes`, `repair --yes`, with exit codes and `--json` ([cli.md](cli.md)). The setup window does not exist yet; when it does, *proposed*: it refuses to start without a display, says so in one sentence, and points to the silent mode with a response file, instead of failing with a technical error or opening nothing.

## Antivirus and SmartScreen

*Not verified.* Expected: an unsigned engine triggers Windows SmartScreen and may be scanned by Defender while it unpacks (many small files); macOS Gatekeeper refuses an unsigned download until it is opened from the context menu; Linux has no equivalent. Signing is [#9](https://github.com/Stellatinus/Stellatinus-Installer/issues/9). Until it exists the test builds say so and what to do ([#32](https://github.com/Stellatinus/Stellatinus-Installer/issues/32)). A file that is locked by a running program (Windows) is reported as not removed or not restored, never skipped silently; step 9 of [#27](https://github.com/Stellatinus/Stellatinus-Installer/issues/27) checks it.

## What only a real machine can settle

- UAC, ACLs, controlled folder access, the elevated-as-another-user profile, network and removable disks: [#18](https://github.com/Stellatinus/Stellatinus-Installer/issues/18).
- Install, interrupt, uninstall and Defender on Windows, Linux and macOS: [#27](https://github.com/Stellatinus/Stellatinus-Installer/issues/27).
- That Windows, GNOME, KDE and macOS accept and show the shortcuts the engine writes (the `.lnk` is written by hand): [#39](https://github.com/Stellatinus/Stellatinus-Installer/issues/39).
- The window on each desktop: [#29](https://github.com/Stellatinus/Stellatinus-Installer/issues/29).
