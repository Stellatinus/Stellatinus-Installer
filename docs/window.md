# The setup window

**Status: a spike and a proposal, not a decision.** The pieces that do not depend on the choice are built and tested. What only a real desktop can settle is listed at the end and tracked in [#29](https://github.com/Stellatinus/Stellatinus-Installer/issues/29).

## The question

How is the wizard drawn so that it looks like a small system window, the same on Windows, Linux and macOS, is accessible, and costs little to maintain? The look is the draft in [design/wizard-mockup.html](design/wizard-mockup.html): a sidebar with the steps, one question per step, a progress bar, an advanced log, the system's light or dark theme, one accent colour from the manifest, text that cannot be selected.

## What is built and tested

| Piece | What it does | Tested |
|---|---|---|
| `internal/localui` | serves the interface on `127.0.0.1` for the one person who opens it: a **single-use address** exchanged for a session cookie (`HttpOnly`, `SameSite=Strict`), the `Host` header must be the server's own (so DNS rebinding does not work), a request that changes something must come from the page itself, bodies are limited, replies say no cache, no framing, no referrer and a Content-Security-Policy of `default-src 'self'`, no CORS | unit tests, each protection checked by removing it |
| `internal/opener` | chooses how to show the address: an **app window** (no tabs or address bar) of Edge, Chrome or Chromium when one exists, else the default browser; a separate browser profile so the process ends when the window closes | unit tests of the choice for each system; **nothing is launched in a test** |
| `spikes/window` | a small program that serves a page, opens it and lets the page run the engine's real checks | handler tests, plus a run against a real headless Chromium (below) |

The spike was run on Linux with a headless Chromium: the page loads through the one-time address; the app name and the accent colour (yellow, from the Haedus manifest, with black text on it) come from the manifest; dark and light mode both follow the system setting; a triple-click on a heading selects nothing while a path field can be selected; the checks run and report; the address cannot be used a second time (401); and the Close button stops the program. A first run found a real fault the unit tests did not: the page had inline script and style, which the server's own policy forbids, so it showed nothing. They are now separate files, and a test fails if inline code comes back.

## The options

| Option | For | Against |
|---|---|---|
| **A. An app window of Edge, Chrome or Chromium**, opened by the engine | no new dependency and **no cgo**: the command line stays one static binary cross-compiled from one machine. Edge is on every supported Windows | on macOS and Linux a Chromium browser may not be installed; then the interface opens in a normal browser tab (still works, not a small window). Whether `--app` and the separate profile behave the same on current Edge, Chrome and Chromium is **untested** |
| **B. A webview in a separate small program** (Wails v3 or `webview/webview`) | a real native window on each system, from the system's web view (WebView2, WKWebView, WebKitGTK) | needs cgo on macOS and Linux, so it cannot be cross-compiled from one Linux runner and needs a build job per system; Linux needs WebKitGTK; Wails v3's own documentation still calls it alpha or beta (not verified here). Not tried |
| C. A toolkit that draws itself (Avalonia, Fyne) | one code base | a new framework to learn; Fyne's accessibility is a first pass, off by default (its v2.8 notes) |
| D. A native interface per system | the most native | three code bases |

## Proposal (open to change)

1. **The interface is the local web UI for every option.** `localui` does not care what shows it.
2. **Windows first: option A.** An Edge app window, which is always there.
3. **macOS and Linux: option A when a Chromium browser exists, otherwise the default browser.** Then evaluate **option B as a separate program** (`stellatinus-setup`), built per system on native runners, only if the real-machine results show the browser window is not good enough. The engine, the command line and the UI stay free of cgo either way.
4. The command line stays the engine's other face; neither depends on the window.

## Try the spike

```sh
go build -o window-spike ./spikes/window
./window-spike --manifest examples/haedus.json           # auto: an app window if Edge/Chrome/Chromium exists, else the default browser
./window-spike --mode app      # fail instead of opening a tab
./window-spike --mode default  # force the default browser
./window-spike --mode none     # only print the one-time address
```

Close it with the page's **Close** button, by closing the app window, or with Ctrl+C. It installs nothing; its "checks" are the engine's real checks on the folders the manifest proposes (the first run on a Haedus example shows the folder, space and permission results of that machine).

## What only a real desktop can settle

Tracked in [#29](https://github.com/Stellatinus/Stellatinus-Installer/issues/29).

- Does `msedge.exe --app=URL --user-data-dir=…` give a clean small window on current Windows 10 and 11, does the process end when the window is closed, and what happens if Edge is already running?
- The same for Chrome and Chromium on macOS and Linux (X11 and Wayland, GNOME and KDE), and what happens with only Safari or Firefox.
- The window's frame, size, position, taskbar icon and title; high-DPI; whether the window stays in front of the one it was started from.
- Screen readers (Narrator, NVDA, VoiceOver, Orca) on the page: focus order, announcements of the progress and of check results, `prefers-reduced-motion`.
- Whether the system's light or dark setting reaches the page in an app window on each system.
- Antivirus or SmartScreen reactions to the engine launching a browser.
- If option B is tried: Wails v3 and `webview/webview` on the three systems, the idle memory of each, and the build effort.
