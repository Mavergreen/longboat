# Longboat

**This README has not been read or edited by a human yet.** Until it has, this project cannot cut its first release.

Longboat for Mavericks installs pinned Apple SDKs, starting with the Mac OS X 10.9 SDK, as local Mavergreen family products. It is a small POSIX `sh` command, `longboat`.

**Disclaimer:** an unofficial community tool, not affiliated with Apple; SDKs are fetched from third-party mirrors onto the user's own machine and never redistributed.

## What it does

`longboat install 10.9` fetches the pinned SDK, builds a local pkg for it on your own Mac and installs it, so each SDK is an ordinary family product with its own tree, manifest and Installer receipt. The 10.9 SDK lands at `/usr/local/mavergreen/sdk109/SDKs/MacOSX10.9.sdk`, which exists exactly when `sdk109` is installed. `mavergreen list` shows it and `mavergreen uninstall sdk109` removes it. Each SDK has its own product tree, so upgrading Longboat never deletes an SDK. Installing Longboat itself installs no SDK.

Longboat is for anyone targeting Mavericks, whether or not they develop Mavergreen products, and does not require shipyard to be installed. It also carries no fetching code, pins or packaging scripts of its own: its package step copies them from shipyard (`fetch_sdk.sh`, `sdk-pins.sh`, `mavericks_fetch.sh` and the staging scripts), so every shipped copy is byte-identical to shipyard's. Shipyard still owns how an SDK is fetched, and Mavergreen product builds keep getting their sysroot from it. An SDK becomes available through Longboat once shipyard can fetch it, and reaches users with a Longboat release.

Releases carry scripts only. Apple's bytes are never published; an SDK pkg is built on your own Mac and never leaves it.

## Usage

- `longboat list` shows the SDKs Longboat offers and which are installed.
- `longboat fetch <sdk> --into DIR` fetches and verifies an SDK into `DIR` and prints the path. It works on any host.
- `sudo longboat install <sdk>` builds a local pkg for the SDK and installs it. It needs a Mac and root.

For example, `sudo longboat install 10.9`.
