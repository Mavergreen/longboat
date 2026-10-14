# Longboat

`longboat` fetches and installs the Mac OS X 10.9 Mavericks SDK, and fetches and verifies the Mac OS X
install media that VM builds start from.

## How to use

```sh
longboat --help
sudo longboat install 10.9
```

A future release may handle other SDK versions.

## Fetching and verifying installers

```sh
longboat fetch installer 10.9 --into ~/installers
longboat verify installer 10.6 ~/installers/install-disc.img
```

`fetch installer <release> --into DIR` puts the release's install media in `DIR`, as `InstallESD.dmg`
(10.9) or `install-disc.img` (10.6), and prints its path. Nothing takes that name until it has passed
the release's pins. Media is trusted by what it holds, never by where it came from: 10.9's
`InstallESD.dmg` by its SHA-256, and a 10.6 disc by the SHA-256 of every package on it, so any faithful
copy passes, whatever made the image (an ISO, a Disk Utility `.dmg` or `.cdr`).

The sources are tried in the order given, and each flag may be given more than once:

- `--source-recipe`: the release's built-in recipe. 10.9's downloads `InstallESD.dmg` from Apple's
  servers. With no source flag at all, `fetch installer` uses it.
- `--source-file PATH`: an image on this machine.
- `--source-url URL [--auth-header-env NAME]`: an image to download. The request's header, written
  `Name: value`, comes from the environment variable `NAME`, never from the command line. Neither the URL
  nor the header is ever printed.
- `--source-command PATH`: a program on this machine. It runs with a stripped environment and is given,
  as its last argument, a directory to write exactly one image into.

**10.6 has no built-in recipe; bring your own disc.** Apple doesn't serve 10.6, so `fetch installer
10.6` needs a source flag: an image of a retail Snow Leopard DVD.

`verify installer <release> <image>` checks an image you already have, and prints
`<release> <build> <content-digest>`.

Both exit 0 when all is well, 1 when the media is refused or can't be had, and 2 on a usage error.

## Building installmedia

`longboat fetch installer` and `verify installer` run `libexec/longboat/installmedia`, a Go program in
`installmedia/cmd/installmedia`. `build/build-installmedia.sh darwin|linux <out>` builds it for x86_64
with the `go` on `PATH`. Releases build it with the family's pinned Go (`components/golang/version`),
whose darwin binaries run on 10.9.

To build it natively on a 10.9 Mac, install Xcode's Command Line Tools first. A darwin/amd64 build
always links through the C compiler, and without the Command Line Tools there is none (shipyard QUEUE
#52).
