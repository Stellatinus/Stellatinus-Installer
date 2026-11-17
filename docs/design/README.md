# Design draft

[`wizard-mockup.html`](wizard-mockup.html) is an interactive draft of the setup window, with example data for Haedus. Open it in a browser. It is a design reference, not the product: nothing in it installs anything.

It shows:

- the steps: welcome, program folder, AI models (an existing folder can be adopted), output folder, shortcuts, checks, install, finish;
- checks one by one, with three simulated problems (missing privileges, no permission to write, not enough space); each fix returns to the step that solves it;
- a progress bar that says what is being downloaded in plain words, with an advanced log underneath;
- update and removal screens; removal lists each folder and each AI model separately and only lets the user delete what Haedus downloaded;
- the system's light or dark theme, and the accent colour chosen from one value (try the swatches; the contrast readout is the same algorithm as `internal/theme`).

The window buttons are drawn on the left only because the draft is a web page; the real window uses the system's frame.
