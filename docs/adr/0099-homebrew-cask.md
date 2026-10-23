# 0099. Homebrew installs migr from a cask in emrebdr/homebrew-tap, written by each release with a token scoped to that repository

## Status

Accepted

## Date

2026-10-23

## Context

M14-T2's done-when is that `brew install emrebdr/tap/migr` works. Homebrew reads `emrebdr/tap` as the
repository `github.com/emrebdr/homebrew-tap`. ADR 0098's release job builds the archives, and
goreleaser can write the tap entry from them on every release.

Three facts shaped the decision, each measured against Homebrew 7.0.10 with goreleaser v2.17.1's
generated cask installed from a local tap:

- **goreleaser has two generators, and one is deprecated.** `brews` writes a formula and has been
  deprecated since goreleaser v2.10. `homebrew_casks` writes a cask, with `on_macos` and `on_linux`
  blocks, so it serves Linuxbrew too.
- **Homebrew carries a download's quarantine onto the installed binary.** A quarantined archive
  installed with no hook left `com.apple.quarantine` on `Caskroom/migr/<version>/migr`. ADR 0098 left
  the binary unnotarized, so Gatekeeper would refuse its first run.
- **Homebrew 7 deprecates `postflight`.** goreleaser's `hooks.post.install` renders one, and every
  load of the cask then prints a deprecation warning that asks the user to report it to the tap.
  Homebrew 7's replacement is `postflight_steps`, whose `run` step ran the same `xattr` cleanly.

## Decision Drivers

- `brew install emrebdr/tap/migr` should work, and print nothing alarming, on current Homebrew on
  macOS and Linux.
- The secret that can write the tap should be able to write nothing else.
- A pre-release should never reach a `brew install` user.

## Considered Options

### A. Formula or cask
- **A1: a cask** ✔ (by agreement). goreleaser's supported generator.
- A2: a formula through `brews`. **Rejected:** deprecated, so `goreleaser check` warns about it, and a
  later major will remove it.

### B. Clearing quarantine
- **B1: `postflight_steps` with a `run` of `/usr/bin/xattr`, macOS only, through `custom_block`** ✔.
  goreleaser renders it at the top of the cask, which `brew style` reports as stanza order and which
  Homebrew loads correctly.
- B2: goreleaser's `hooks.post.install`. **Rejected:** the `postflight` it renders warns on every load.
- B3: no hook. **Rejected:** measured, the binary stays quarantined.

### C. Completions
- **C1: generated at release by `migr completion`, shipped in every archive under `completions/`, and
  linked by the cask's `bash_completion`, `zsh_completion` and `fish_completion`** ✔. The npm wrapper
  and the Docker image can ship the same files.
- C2: `generate_completions_from_executable`. **Rejected:** it runs the binary on the user's machine
  at install, for output that is the same on every machine.

### D. Who writes the tap
- **D1: a fine-grained personal access token whose only repository is `emrebdr/homebrew-tap`, with
  Contents read and write, held as `HOMEBREW_TAP_GITHUB_TOKEN`** ✔. The job's `GITHUB_TOKEN` can't
  push to another repository.
- D2: a GitHub App installation token. **Rejected for now:** an App, a private key and an extra action
  step, for one repository.

## Decision Outcome

A1, B1, C1, D1.

- `skip_upload: auto` keeps a pre-release tag out of the tap.
- A snapshot writes the cask under `dist/homebrew/` and pushes nothing. The hand-run workflow uploads it
  with the snapshot artifact.
- Homebrew 6 made non-official taps untrusted by default. Installing a fully qualified name trusts
  that item alone, so `brew install emrebdr/tap/migr` works as written, while a short `brew install
  migr` needs `brew trust --cask emrebdr/tap/migr` first. The README says so.

## Consequences

### Positive
- One command installs the binary and completions on macOS and Linux, and `brew upgrade` follows each
  release.
- The tap token can write one repository's contents and nothing else.

### Negative
- A personal access token expires, and the cask step then fails after the GitHub release is
  published. `docs/releasing.md` gives the recovery.
- `postflight_steps` needs Homebrew 7, so an older Homebrew can't load the cask until it updates,
  which `brew install` does by default.
- The cask is hand-tuned past goreleaser's hooks, so a goreleaser upgrade that renders
  `postflight_steps` itself should replace the custom block.

### Neutral
- The tap repository and its secret are set up by hand, once, by the repository owner.
