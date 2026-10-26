# Contributing to Clipferry

Thanks for helping! Bug reports, translations, ideas and code are all welcome.

## Quick start

You need Go 1.26+. On macOS you also need the Xcode command line tools.

```bash
git clone https://github.com/bkrdmrcioglu/clipferry
cd clipferry
go test ./...                 # unit tests and the end-to-end tests
go run ./cmd/clipferry-demo   # two simulated devices in one process, no second computer needed
```

The demo prints a URL for each device's window. Run it with `CLIPFERRY_UI_DIR=internal/ui/dist` to edit the UI and just reload the page.

To run the real desktop app from source:

```bash
go run ./cmd/clipferry
```

It uses your normal Clipferry data directory. Set `CLIPFERRY_DATA_DIR=/some/folder` to run a separate copy next to an installed one.

## Where things live

See the project layout table in the [README](README.md#project-layout). In short: `internal/core` is the engine, `internal/ui/dist` is the window (plain HTML/CSS/JS, no build step), and `cmd/clipferry` is the thin desktop shell.

## Good first contributions

- **Translations.** Copy the `en` block in [`internal/ui/dist/i18n.js`](internal/ui/dist/i18n.js), translate it, add the language to `languages` in `internal/core/updates.go`, and add the tray menu texts in `cmd/clipferry/main.go`.
- **Secret detection.** Patterns live in [`internal/secrets/secrets.go`](internal/secrets/secrets.go), with a test table next to them. A new pattern needs a test, and a check that ordinary text does not trigger it.
- Issues labelled [good first issue](https://github.com/bkrdmrcioglu/clipferry/labels/good%20first%20issue).

## Pull requests

- Keep changes focused; one topic per pull request.
- Run `gofmt`, `go vet ./...` and `go test ./...` before pushing. CI runs the same, plus the macOS and Windows builds.
- If you change what the user sees, add a screenshot to the pull request, and keep both themes and narrow windows working.
- Anything that touches pairing, encryption or the local API deserves a test.

## Releases

Pushing a `v*` tag builds the macOS `.dmg`, the Windows installer and portable `.exe` files, publishes them with a `SHA256SUMS` file, and installed apps update themselves from it.
