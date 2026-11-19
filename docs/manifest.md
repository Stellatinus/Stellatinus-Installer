# Manifest

A manifest is one JSON file that an app gives to the installer. The Go code ([`internal/manifest`](../internal/manifest/manifest.go)) is the authority; the [JSON Schema](../schema/manifest.schema.json) describes the same format and a test keeps the two in step. Unknown fields are errors. All problems are reported together.

Check one with:

```sh
stellatinus-installer validate examples/haedus.json
```

Format version: **1**.

## Top level

| Field | Required | Meaning |
|---|---|---|
| `schema_version` | yes | `1` |
| `app` | yes | who the app is, see below |
| `channel` | no | `direct` (default) or `store`. Store builds are postponed ([#8](https://github.com/Stellatinus/Stellatinus-Installer/issues/8)); the field is kept so nothing blocks them |
| `scope` | no | `user` (default, no administrator needed) or `machine` (all users, asks for elevation) |
| `allow_machine` | no | the user may choose `machine` |
| `locations` | yes | the folders, see below |
| `components` | no | what to download, in order |
| `options` | no | questions that are not folders (a port), see below |
| `config` | no | small files the installer writes from the answers |
| `shortcuts` | no | where to offer shortcuts |
| `texts` | no | translations, see below |

## `app`

| Field | Required | Meaning |
|---|---|---|
| `id` | yes | 2 to 32 characters: a lower-case letter, then lower-case letters, digits or `-` |
| `name` | yes | shown in the window |
| `description` | no | one sentence for the first screen |
| `version` | yes | semantic version, for example `1.4.0` |
| `publisher` | no | |
| `accent` | yes | **one** `#rrggbb` colour. The installer derives the button colour, the text colour on it, the accent as text and a tint, for the light and the dark theme, and **refuses a colour it cannot make legible** (contrast of at least 4.5:1). Try one with `stellatinus-installer accent '#f2c200'` |
| `logo` | no | path to the logo |

## `locations`

The folders the user chooses. Exactly one has `kind: "install"`.

| Field | Required | Meaning |
|---|---|---|
| `id` | yes | for example `install`, `models`, `output` |
| `label` | yes | shown in the window |
| `kind` | yes | `install`, `models`, `output` or `data` |
| `default` | yes | the proposed path per system: `windows`, `linux`, `darwin`. `${VAR}` is replaced with an environment variable; `..` is not allowed |
| `required` | no | cannot be left out |
| `adopt` | no | the user may point at a folder that already holds content, for example AI models downloaded earlier. The installer uses it where it is, never copies it and **never deletes it** |
| `on_uninstall` | no | `remove`, `keep` or `ask` (default) |

## `components`

What to download while installing. The window shows `label` above the progress bar ("Downloading the core") and the file names only in the advanced log.

| Field | Meaning |
|---|---|
| `id`, `label` | |
| `url` | must start with `https://` |
| `sha256` | 64 lower-case hexadecimal digits; the download is checked against it |
| `size` | bytes of the download, more than zero. It **caps the download** (a server that sends more is cut off) and feeds the free-space check |
| `location` | the id of the location it is stored in |
| `kind` | `file` (default: stored as one file) or `zip` (unpacked) |
| `name` | the file name for kind `file`; by default the last part of the URL (a query string is ignored). Not allowed for `zip` |
| `dest` | a sub-folder of the location, written with `/`; empty means the location itself |
| `platforms` | the systems it is for: `windows`, `linux` or `darwin`, optionally with `/amd64` or `/arm64` (`["windows/amd64", "linux"]`). Empty means every system. Only the components for the system the installer runs on are downloaded, counted for the free space, and shown in the progress |

Names and paths must be valid on every system, so a package built on one installs the same on another: no `..`, no absolute path, no `\`, no `:`, no trailing dot or space, no Windows device name such as `CON` or `NUL`.

How a component is installed ([`internal/install`](../internal/install/install.go)): it is downloaded to a `.stellatinus-<id>.part` file next to its destination and checked against `sha256`; only then is it renamed into place (kind `file`) or unpacked (kind `zip`). A file that is already there with the right hash is **not downloaded again** and is recorded as adopted; a different file with the same name is **never overwritten**. A zip entry is never allowed to overwrite an existing file either, and the archive is refused before a single file is written if an entry escapes the folder, is a symbolic link or special file, differs from another only in case, or if the archive is too large, has too many entries or is compressed suspiciously well.

## `options` and `config`

An **option** is a question the wizard asks that is not a folder. Only `port` exists: a number from 1024 to 65535 (a lower port needs privileges). The checks **really listen** on `127.0.0.1:<port>` for a moment, because a number that looks fine can be taken by another program or refused by the system; if it fails the window goes back to that question.

A **config file** is a small file written for the app from the answers, so the app finds its port without a setup step of its own:

```json
"options": [{ "id": "port", "label": "Port", "kind": "port", "default": 5080 }],
"config":  [{ "location": "data", "name": "config.json", "format": "json", "values": { "port": "port" } }]
```

writes `{"port": 5081}` to `config.json` in the `data` folder. It is recorded as the installer's, so uninstall removes it, and it is **never written over a file that already exists** (the installation stops and rolls back instead). Only `json` exists.

## `texts`: translations

English is the base, written in the fields themselves. `texts` adds other languages: language code, then text key, then the text.

```json
"texts": { "it": { "app.description": "Trasforma la voce in testo.", "location.models.label": "Modelli IA" } }
```

Keys: `app.name`, `app.description`, `location.<id>.label`, `component.<id>.label`, `option.<id>.label`. A key that names nothing, or an empty or very long text, is an error. The window uses `m.Text(lang, key)`: the translation (for `pt-BR` also `pt`), else English. The Stellatinus apps are bilingual, so a test requires every example to have an Italian text for everything that has an English one ("MissingTexts"); a third party may leave languages out.

## `launch`

The program a shortcut starts: `location` (one of the locations) and `path`, a program per system written with `/`, inside that location. It is **required when a shortcut is offered**, and a shortcut is made only on a system that has a path.

```json
"launch": { "location": "install", "path": { "windows": "Haedus.exe", "linux": "bin/haedus", "darwin": "Haedus.app" } }
```

## `shortcuts`

`start_menu`, `taskbar` and `desktop`, each with `offer` (show the question) and `default` (ticked at first). The taskbar is best effort: Windows has no supported way to pin an app from an installer (not verified here), so the engine never makes one: it says so in a note, and the window shows a hint on the last screen. Where each shortcut goes on each system is in [cli.md](cli.md#shortcuts).

## Not in the format yet

The update source and trust anchor ([#7](https://github.com/Stellatinus/Stellatinus-Installer/issues/7), [#9](https://github.com/Stellatinus/Stellatinus-Installer/issues/9)), autostart and services ([#33](https://github.com/Stellatinus/Stellatinus-Installer/issues/33)), components as `.tar.gz` ([#35](https://github.com/Stellatinus/Stellatinus-Installer/issues/35)), per-system default folders for an all-users install ([#10](https://github.com/Stellatinus/Stellatinus-Installer/issues/10)), and app-specific checks and hooks (not planned until an adopter needs them).

## Examples

[`examples/`](../examples) has a manifest for each adopter, and `stellatinus-demo.json`, the fake app of the [test installers](../README.md#try-it-without-building) (its hashes are filled in by `tools/demo` in CI): `haedus.json`, `mira.json` (a port written to `config.json`, settings kept on removal), `polaris.json` and `regulus-desktop.json` (illustrative: those apps do not have packages yet), and `minimal.json`. Their URLs and hashes are fake (`example.invalid`; the demo points at the test-builds release). Every one is checked by the tests: valid, fully translated to Italian, and with default folders that do not overlap on any system.

## Anything an app adds later

If an app downloads files into a location after the installation (for example a model from inside Haedus), it must add them to the installation record as `installer`-owned, or removal will treat them as the user's and keep them. The contract for adopters, with `register`, is in [embedding.md](embedding.md).
