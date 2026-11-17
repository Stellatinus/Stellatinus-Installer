# Security policy

An installer and an updater run with the user's trust and sometimes with administrator rights, so security problems here matter.

## Reporting a problem

**Do not open a public issue.** Use GitHub's private vulnerability reporting: on the repository, open the **Security** tab and choose **Report a vulnerability**. Include what you found, how to reproduce it and what it could do.

## Scope

In scope: deleting or overwriting something outside what the installer created, following a symbolic link out of an install folder, accepting an unverified download, a manifest that makes the engine do more than the format allows, and weaknesses in the (future) updater and signing.

## Status of this project

The project is at an early stage: there is no installer window, downloader or updater yet, and no release. There is nothing to patch in a released version; reports about the design or the current code are still welcome.

## Rules the code already enforces

- Removal deletes only entries the installation record marks as created by the installer, and refuses a path outside its recorded location or through a symbolic link.
- Manifests are read strictly: unknown fields are errors, the file size is limited, and download URLs must be `https://` with a sha256.
