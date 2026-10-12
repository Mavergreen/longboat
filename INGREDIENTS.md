# Build ingredients

Everything baked into what Longboat ships, and how a change to it reaches a release.

Longboat is **its own upstream**: it is versioned `YYYYMMDD.N` and nothing external releases it.

| Ingredient | Pinned in | Renovate | On a bump |
|---|---|---|---|
| Shipyard scripts (fetching, pins, packaging, staging) | `components/shipyard/version`, with the copied scripts' digest in `components/shipyard/scripts.sha256` | ✅ | a bump repackages |
| MacOSX10.9 SDK pin | lives in shipyard's `sdk-pins.sh` | n/a here: Longboat ships shipyard's pin | follows shipyard |

No upstream release notes: Longboat is its own upstream.

## Declared state

- upstream: UPSTREAM_VERSION
- shipyard-scripts: components/shipyard/scripts.sha256

## Conformance deviations

- scheme: Longboat is its own upstream (no upstream release to repackage), so it versions itself by date as YYYYMMDD.N with no -mavericks.N axis
