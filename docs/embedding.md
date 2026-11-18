# Embedding the installer in an app

How an app (Haedus, Mira, Polaris, Regulus Desktop) ships the installer. This is the contract of [#4](https://github.com/Stellatinus/Stellatinus-Installer/issues/4).

## One file

The app's release produces **one file** per system: the engine with the app's manifest appended.

```bash
stellatinus-installer pack --manifest haedus.json --out Haedus-Setup.exe
```

`pack` validates the manifest first (a broken one is never packed), copies the engine, then appends the manifest and a trailer (its length, its sha256 and the text `STELLATINUSPACK1`). The engine bytes are not changed, so a signature applied to the file afterwards (Authenticode) covers the manifest too. Packing an already packed file replaces its manifest. `--engine` names another engine file; the default is the running program.

Because the manifest travels inside the file, the engine and the manifest can never be a mismatched pair.

## What a packed installer does

| Started as | Does |
|---|---|
| `Haedus-Setup.exe` (no arguments) | names the app and asks `Install it with the default folders? [y/N]`; only `y` or `yes` installs. This is the whole interaction until the window exists ([#3](https://github.com/Stellatinus/Stellatinus-Installer/issues/3)) |
| `Haedus-Setup.exe install [flags]` | installs the carried manifest; no manifest file is named |
| `Haedus-Setup.exe check [flags]` | runs the checks for the carried manifest |
| any command with a file named | uses that file instead |

A file that claims to carry a manifest but fails its checksum or length is refused (exit 1) and nothing is installed. The plain engine, with nothing to carry, shows its usage.

## After the installation: telling the installer what the app made

If the app creates files inside a location after the installation (a model it downloads, say), it must tell the installer, or uninstall will leave them as if they were the user's:

```bash
stellatinus-installer register --location models --origin "model catalog" haedus /path/to/model.bin
stellatinus-installer register --location models --recursive haedus /path/to/model-pack
stellatinus-installer unregister haedus /path/to/model.bin
```

`register` makes the entries `installer`-owned so uninstall removes them (unless the user chooses to keep that location's data). The installer does not trust the caller blindly. It refuses, and registers nothing at all, if a path:

- is outside the location's folder, relative, or reached through a symbolic link;
- does not exist, or is neither a regular file nor a folder;
- was last modified before the installation (it was there already, so it is not the app's);
- is recorded as `adopted` or `user`.

Registering the same path twice is harmless. `unregister` only forgets an entry the installer owns; it never touches the disk, so a file the app takes back is kept. Both rewrite the record atomically. An app written in Go can call `record.Register` directly.

## Not verified

Nothing here has run on a real Windows, macOS or Linux machine. Not yet decided or built: how the app finds the installer file to call `register` (the installed app folder is the natural place) and signing the packed file ([#9](https://github.com/Stellatinus/Stellatinus-Installer/issues/9)).
