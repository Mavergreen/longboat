# Longboat

Longboat gets you an SDK, or install media, for a particular OS X.

It is an unofficial community tool, not affiliated with Apple. SDKs are fetched from third-party mirrors
onto your own machine and never redistributed.

## Getting an SDK

```sh
sudo longboat sdk install 10.9
```

It fetches the pinned SDK, checks its SHA-256, installs it as its own package, and prints where it is:

```
/usr/local/mavergreen/sdk109/SDKs/MacOSX10.9.sdk
```

Build against it:

```sh
clang -isysroot /usr/local/mavergreen/sdk109/SDKs/MacOSX10.9.sdk -mmacosx-version-min=10.9 hello.c
```

`longboat sdk list` shows the SDKs on offer, 10.9 and 11.3 today, and which are installed. 11.3's lands at
`/usr/local/mavergreen/sdk113/SDKs/MacOSX11.3.sdk`. To remove one, run `sudo longboat sdk uninstall 10.9`.

`longboat sdk fetch 10.9 --into DIR` puts the SDK in a directory of your own instead, without root and
without installing anything.

## Getting install media

```sh
longboat media fetch 10.9 --into ~/installers
```

It puts 10.9's `InstallESD.dmg` in `~/installers`, downloaded from Apple, and prints its path. Nothing
takes that name until it has passed 10.9's pins: media is trusted by what it holds, not by where it came
from.

10.6 has no download: Apple doesn't serve it. Bring an image of a retail Snow Leopard DVD:

```sh
longboat media fetch 10.6 --into ~/installers --source-file ~/snow-leopard.iso
```

`longboat media verify 10.6 IMAGE` checks an image you already have. `longboat media list` shows the
versions Longboat knows.

Mavergreen's Packer plugin, packer-plugin-macosx, doesn't need Longboat: it checks whatever media you give
it with the same checks. Longboat is for getting the media onto your machine yourself, for UTM, a real
Mac, a 10.9 box or a Linux/KVM host, and a file it fetched is one the plugin accepts.

## What works where

`longboat list` shows both jobs at once, one line per OS X version.

| | Mac | Linux |
| --- | --- | --- |
| `longboat list` | yes | yes |
| `longboat sdk list`, `sdk fetch` | yes | yes, with `curl`, `xz` and `shasum` |
| `longboat sdk install`, `sdk uninstall` | yes, as root | not yet: it says so |
| `longboat media list`, `media fetch`, `media verify` | yes | yes |

On a Mac, install Longboat's `.pkg` from the [latest release](https://github.com/Mavergreen/longboat/releases/latest);
it keeps itself up to date. On Linux, unpack the same release's `longboat-<version>-linux-x86_64.tar.gz`,
listed in its `SHA256SUMS`, and run `opt/mavergreen/longboat/bin/longboat`. Its manual page is
`opt/mavergreen/longboat/share/man/man1/longboat.1`.

## More

`man longboat` has every command and flag, where things go, the exit codes, and the commands' earlier
spellings, which still work. [docs/DEVELOPING.md](docs/DEVELOPING.md) is for working on Longboat itself.
