# Packaging

- **Homebrew (macOS):** the cask lives in [bkrdmrcioglu/homebrew-tap](https://github.com/bkrdmrcioglu/homebrew-tap/blob/main/Casks/clipferry.rb). Update `version` and `sha256` (from the release's `SHA256SUMS`) after each release.
- **winget (Windows):** `winget/manifests/…` mirrors the layout of [microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs). For a new version, copy the folder, update the version, URL and `InstallerSha256`, check it with `winget validate`, and open a pull request there.
