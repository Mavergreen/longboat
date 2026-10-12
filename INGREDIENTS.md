# Build ingredients

Everything baked into what Longboat ships, and how a change to it reaches a release.

Longboat is **its own upstream**: it is versioned `YYYYMMDD.N` and nothing external releases it.

| Ingredient | Pinned in | Renovate | On a bump |
|---|---|---|---|
| Shipyard scripts (fetching, pins, packaging, staging) | `components/shipyard/version`, with the copied scripts' digest in `components/shipyard/scripts.sha256` | ✅ | a bump repackages |
| MacOSX10.9 SDK pin | lives in shipyard's `sdk-pins.sh` | n/a here: Longboat ships shipyard's pin | follows shipyard |

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
read), commits it to the branch (`build/digest-bot.sh apply`, in a job that reads no shipyard bytes) and
dispatches `release.yml` there, whose `build` is the check the bump merges on. That build runs
`build/shipyard-scripts.sh check`, so a stale digest cannot merge.

Exit task: this is interim, until shipyard can itself recognise an ingredient bump that produces an
identical build and cut no release for it (shipyard queue #17). When that lands, delete
`components/shipyard/scripts.sha256`, `build/digest-bot.sh`, `.github/workflows/shipyard-digest-bot.yml`
and the `shipyard-scripts` entry in `## Declared state`, and declare `components/shipyard/version`
instead.

## Declared state

- upstream: UPSTREAM_VERSION
- shipyard-scripts: components/shipyard/scripts.sha256

## Conformance deviations

- scheme: Longboat is its own upstream (no upstream release to repackage), so it versions itself by date as YYYYMMDD.N with no -mavericks.N axis
