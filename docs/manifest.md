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
| `shortcuts` | no | where to offer shortcuts |

## `app`

| Field | Required | Meaning |
|---|---|---|
| `id` | yes | 2 to 32 characters: a lower-case letter, then lower-case letters, digits or `-` |
| `name` | yes | shown in the window |
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
| `size` | bytes, used for the free-space check |
| `location` | the id of the location it is stored in |

## `shortcuts`

`start_menu`, `taskbar` and `desktop`, each with `offer` (show the question) and `default` (ticked at first). The taskbar is best effort: Windows has no supported way to pin an app from an installer (not verified here), so it may become a hint on the last screen ([#10](https://github.com/Stellatinus/Stellatinus-Installer/issues/10)).

## Not in the format yet

Update source and trust anchor ([#7](https://github.com/Stellatinus/Stellatinus-Installer/issues/7), [#9](https://github.com/Stellatinus/Stellatinus-Installer/issues/9)), services and autostart, app-specific checks and hooks, translations of app texts. See [#2](https://github.com/Stellatinus/Stellatinus-Installer/issues/2).

## Anything an app adds later

If an app downloads files into a location after the installation (for example a model from inside Haedus), it must add them to the installation record as `installer`-owned, or removal will treat them as the user's and keep them. The contract for adopters is in [#4](https://github.com/Stellatinus/Stellatinus-Installer/issues/4).
