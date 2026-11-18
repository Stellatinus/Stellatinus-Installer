# Command line

`stellatinus-installer` is the command line face of the engine. It is what scripts, company deployment tools and Regulus use; the setup window will call the same engine. Options go **before** the file name.

```sh
stellatinus-installer version
stellatinus-installer validate examples/haedus.json
stellatinus-installer accent '#f2c200'
stellatinus-installer check   [--scope user|machine] [--set location=path]... [--record file] [--json] <manifest.json>
stellatinus-installer install [--scope user|machine] [--set location=path]... [--response file] [--silent] [--json] [--dry-run] <manifest.json>
stellatinus-installer uninstall [--scope user|machine] [--keep-location id]... [--keep-path path]... [--yes] [--silent] [--json] <record.json | app-id>
stellatinus-installer plan    [--keep-location id]... [--keep-path path]... [--json] <record.json>
```

## `install`

1. Reads the manifest and works out every folder: the manifest's default, then the response file, then `--set location=path` (the last one wins). The scope is the manifest's, then the response file's, then `--scope`. An app can be installed for all users (`machine`) only if its manifest says so.
2. Runs the [checks](architecture.md) (privileges, permission to write by creating and deleting a test file, free space, overlapping folders, an earlier installation). **If one fails, nothing is installed** and the exit status is 1.
3. With `--dry-run` it stops here and lists what would be downloaded and where.
4. Downloads, checks and writes every component (see [the manifest](manifest.md)), saving the [installation record](glossary.md) after every step. If anything fails, or the process is interrupted (Ctrl+C), **everything this run created is removed**, and what was already there (an adopted folder, the user's files) is left alone.

An app that is already recorded is refused: remove it first. Repair and update come with the updater ([#7](https://github.com/Stellatinus/Stellatinus-Installer/issues/7)).

`--silent` prints nothing except errors. `--json` prints one JSON event per line (below).

### Response file

A JSON file that answers what the window would ask, so no one has to be there. Unknown fields are errors.

```json
{
  "scope": "user",
  "locations": {
    "install": "D:\\Apps\\Haedus",
    "models": "D:\\Models",
    "output": "D:\\Transcriptions"
  }
}
```

Shortcuts are not part of it yet: they are not created by the engine ([#26](https://github.com/Stellatinus/Stellatinus-Installer/issues/26)).

### Where the record lives

Outside every folder the user chooses, so removing an installation never removes its own record first: `<user config folder>/Stellatinus/<app id>/record.json` for the current user (`%AppData%` on Windows, `~/.config` or `$XDG_CONFIG_HOME` on Linux, `~/Library/Application Support` on macOS), and for all users `%ProgramData%\Stellatinus\<app id>\record.json`, `/Library/Application Support/Stellatinus/<app id>/record.json` or `/var/lib/Stellatinus/<app id>/record.json`.

## `uninstall`

Takes the path of a record, or an app id (with `--scope`, `user` by default). It shows the **plan** for every entry, and **only removes with `--yes`**:

- `remove`: created by the installer;
- `keep`: you asked to keep it (`--keep-location` for everything the installer made in a location, `--keep-path` for one entry);
- `protected`: not the installer's (an adopted folder, a model that was already there, anything added later). It is never removed, whatever the options.

Files and shortcuts go first, then folders from the deepest up. A folder that still holds something the installer did not create is left, and the record is kept with what is left, so the job can be finished later. When everything is removed the record goes too. No path is ever followed through a symbolic link, and none outside its recorded location.

`plan` is `uninstall` without `--yes`, for a record path.

## Exit status

| Code | Meaning | Commands |
|---|---|---|
| 0 | success | all |
| 1 | a problem was found and **nothing was changed**: invalid manifest or response file, a failed check, the app is not allowed for that scope, an installation is already recorded, no record to remove | all |
| 2 | wrong usage: missing argument, unknown option, a `--set` that names no location, a bad scope | all |
| 3 | the installation failed (or was cancelled) and **was rolled back**: everything it created was removed | `install` |
| 4 | the installation failed and **something could not be removed**; the message lists it, and the record, which is kept, lists what is left: run `uninstall --yes` on it | `install` |
| 5 | the removal finished but **some items could not be removed**; they stay in the record | `uninstall` |

## JSON events

With `--json` every line of standard output is one JSON object with an `event`:

| `event` | Fields |
|---|---|
| `check` | `result`: `id`, `title`, `status` (`ok`, `warning`, `failed`), `detail`, and for a failure `fix` and `go_back` |
| `progress` | `phase` (`prepare`, `download`, `verify`, `unpack`, `done`), `component`, `label`, `file`, `done`, `total`, `index`, `count`. `label` is the plain sentence for a progress bar; `file` is the real name, for an advanced log |
| `log` | `message` |
| `plan` | `action`, `path`, `kind`, `size`, `reason` (for `uninstall`) |
| `result` | always the last line: `ok`, `exit`, and `error`, `rolled_back`, `record`, `files`, `skipped`, `removed_count`, `kept`, `protected` as they apply |

## Not verified on a real machine

The command line is tested against a local HTTPS server on Linux, and compiled and tested on Windows and macOS runners. Real networks, antivirus, Windows file locks, and elevation have not been tried ([#13](https://github.com/Stellatinus/Stellatinus-Installer/issues/13), [#18](https://github.com/Stellatinus/Stellatinus-Installer/issues/18)).
