# Build ingredients

Everything baked into what Longboat ships, and how a change to it reaches a release.

Longboat is **its own upstream**: it is versioned `YYYYMMDD.N` and nothing external releases it.

| Ingredient | Pinned in | Renovate | On a bump |
|---|---|---|---|
| Shipyard scripts (fetching, pins, packaging, staging) | `components/shipyard/version`, with the copied scripts' digest in `components/shipyard/scripts.sha256` | ✅ | a bump that changes the digest of the shipped scripts releases; one that leaves it unchanged merges and publishes nothing (see below) |
| MacOSX10.9 SDK pin | lives in shipyard's `sdk-pins.sh` | n/a here: Longboat ships shipyard's pin | follows shipyard |
| Go toolchain (Mavergreen/golang-126's go126-cross pkg; `build/install-go.sh` puts it in place in CI through shipyard's `fetch_product_pkg.sh`) | `components/golang/version` | ✅ | releases: the pin is declared state |
| Install-media pins (Mac OS X 10.9.5's `InstallESD.dmg`, 10.6 retail disc packages) | `installmedia/pins/*` | ❌ untrackable: frozen Apple artifacts | compensates: whole-file or package-level content pins, checked by `installmedia.Check`; a new pin is a deliberate commit, and releases |

Go modules: none (neither `installmedia/go.mod` nor `build/reprotar/go.mod` has any requires).

No upstream release notes: Longboat is its own upstream.

## The shipyard scripts digest

Longboat ships copies of the shipyard scripts listed in `build/shipyard-scripts.list`, taken at package
time from the shipyard release pinned in `components/shipyard/version`. Shipyard releases on every push
to its `main`, so the pin moves often while the scripts Longboat ships rarely change. The declared state
therefore names the scripts' digest, `components/shipyard/scripts.sha256`, and not the pin: a shipyard
bump that changes none of those scripts merges with an unchanged digest and publishes nothing, and one
that changes them moves the digest, and its merge to `main` publishes.

Renovate moves the pin on a `renovate/**` branch. `.github/workflows/shipyard-digest-bot.yml` then
recomputes the digest from the newly pinned shipyard (`build/digest-bot.sh fill`, in a job that can only
read) and commits it to the branch (`build/digest-bot.sh apply`, in a job that reads no shipyard bytes),
pushing as the family's pins-bot GitHub App, the one swift's and recaulk's pins bots use. That push starts
the PR's own `release.yml` run, whose `build` is the check the bump merges on, and which runs
`build/shipyard-scripts.sh check`, so a stale digest cannot merge. The App is needed because a push made
with `GITHUB_TOKEN` starts PR runs that wait for a person's approval, and a `release.yml` run it
dispatches does not count for the PR's required check. The App's client ID is the org variable
`PINS_BOT_CLIENT_ID` and its key the org secret `PINS_BOT_PRIVATE_KEY`; its only permission is
Contents: Read and write.

Exit task: this is interim, until shipyard can itself recognise an ingredient bump that produces an
identical build and cut no release for it. When shipyard can, delete:

- `components/shipyard/scripts.sha256`;
- `build/digest-bot.sh` and `tests/digest-bot-test.sh`;
- `.github/workflows/shipyard-digest-bot.yml`;
- the bot and digest invariants in `tests/workflow-invariants-test.sh` (`build_checks_the_digest`,
  `bot_digest_job_reads_only`, `bot_push_job_follows_digest`, `no_expressions_in_run_blocks` on the bot,
  their mutants, and `$BOT` in its file-presence loop);
- the "committed shipyard scripts digest" step in `.github/workflows/release.yml`, which runs
  `build/shipyard-scripts.sh check`;
- `build/shipyard-scripts.sh check` itself, and its tests in `tests/shipyard-scripts-test.sh`;
- the `shipyard-scripts` entry in `## Declared state`;

repoint the two tests that read the committed digest at a fresh `build/shipyard-scripts.sh extract`
of the pinned shipyard instead: `tests/no-shipyard-copies-test.sh` (its by-sha check) and
`tests/pkg-payload-test.sh` (`test_shipped_copies_match_digest`); and declare
`components/shipyard/version` in `## Declared state`, so the shipyard pin is an ordinary ingredient.
`build/shipyard-scripts.sh extract` stays: packaging still copies the scripts from the pinned shipyard.

## Declared state

- upstream: UPSTREAM_VERSION
- shipyard-scripts: components/shipyard/scripts.sha256
- golang: components/golang/version
- installesd-pin: installmedia/pins/installesd-10.9.tsv
- snowleopard-pins: installmedia/pins/snowleopard-packages.sha256

## Conformance deviations

- scheme: Longboat is its own upstream (no upstream release to repackage), so it versions itself by date as YYYYMMDD.N with no -mavericks.N axis
- sparkle-updater:sdk*.pkg: the SDK pkgs that `longboat sdk install` builds on the user's Mac carry an empty manifest `appcast` and no update-check agent. An SDK's bytes never change (the pin's sha256 guarantees it) and a newer SDK is a different product, so an SDK updater could only deliver repackages to people who already have verified bytes; Longboat's own updater delivers new SDKs (new copied pins) and fixes. Reconsider if an SDK ever tracks a moving upstream
- linux: longboat fetches and verifies Mac OS X install media for VM builds on Linux/KVM hosts (packer-plugin-macosx, macosx-vm), which serves producing Mavericks artifacts; x86_64 only, a tarball under opt/mavergreen/longboat/, no .deb
- self-upstream-exclude:build/vm-acceptance.sh: runs only in CI's 10.9 guest; never shipped
- self-upstream-exclude:build/digest-bot.sh: serves the Renovate digest bot; never shipped
- self-upstream-exclude:build/install-go.sh: installs CI's Go; never shipped
- self-upstream-exclude:**/*_test.go: Go tests; never shipped
- self-upstream-exclude:installmedia/internal/fixture/**: test fixtures; only tests import them
- self-upstream-exclude:docs/DEVELOPING.md: for Longboat's developers; never shipped
