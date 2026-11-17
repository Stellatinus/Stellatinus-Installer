# Glossary

What each term means today. Add new terms here.

| Term | Meaning |
|---|---|
| **Engine** | the Go code that reads a manifest, runs checks, writes the record and (later) installs, removes and updates |
| **Manifest** | the JSON file an app gives the engine: identity, folders, downloads, shortcuts, one accent colour |
| **Location** | a named folder the user chooses (install, models, output, data) |
| **Adopted** | a folder or file that was already there; the installer uses it where it is and never deletes it |
| **Installation record** | the file listing every file, folder and shortcut the installer created or adopted, with its owner |
| **Owner** | `installer` (created by it, may be removed), `adopted` (already there), `user` (added later). Only `installer` entries are ever removed |
| **Plan** | what removal would do with each record entry: remove, keep or protected |
| **Protected** | an entry that removal never touches because the installer does not own it |
| **Check** | a verification before installing (privileges, write permission, space, overlap, earlier installation) |
| **Fix** | what the window offers when a check fails: go back to a step, or restart as administrator |
| **Accent** | the one colour an app sets; the engine derives every other colour from it |
| **Scope** | `user` (only the current user, no administrator) or `machine` (all users, needs elevation) |
| **Component** | something downloaded while installing, checked against its sha256 |
| **Channel** | `direct` (downloaded from the app's own site) or `store` (Microsoft Store, postponed) |
| **Wizard** | the setup window with its steps; planned |
| **Adopter** | an app that uses the installer (Haedus, Mira, Regulus Desktop, Polaris) |
