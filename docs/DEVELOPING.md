# Working on Longboat

For people changing Longboat itself. Using it is in the [README](../README.md) and `man longboat`.

## The Linux tarball

Every release carries `longboat-<version>-linux-x86_64.tar.gz`, listed in the release's `SHA256SUMS`, for
Linux/KVM hosts (packer-plugin-macosx, macosx-vm). Everything in it is under `opt/mavergreen/longboat/`.
It is x86_64 only, there is no `.deb`, and it has no updater: a newer longboat is a newer pin.

A consumer fetches the tarball by its release tag and SHA-256 with shipyard's pinned tarball fetcher, a
sibling of `fetch_product_pkg.sh` that shipyard has yet to write, never by hand.

The tarball is reproducible. `build/package-linux.sh` packs it with `build/reprotar`, which owns every
entry by root, stamps it with the release commit's time and sorts the entries bytewise. Built with the
family's pinned Go (`components/golang/version`), as releases are, the same files always pack to the same
bytes, whoever builds them and whenever. Another Go may not: installmedia's bytes and the gzip stream
both depend on the Go release.

## Building installmedia

`longboat media list`, `media fetch` and `media verify` run `libexec/longboat/installmedia`, a Go program
in `installmedia/cmd/installmedia`, which speaks as `longboat media`. `build/build-installmedia.sh
darwin|linux <out>` builds it for x86_64 with the `go` on `PATH`. Releases build it with the family's
pinned Go (`components/golang/version`), whose darwin binaries run on 10.9.

To build it natively on a 10.9 Mac, install Xcode's Command Line Tools first. A darwin/amd64 build
always links through the C compiler, and without the Command Line Tools there is none: the family's Go
calls `/usr/bin/clang`, which on 10.9 is only a stub until they are installed.

## The manual page

`docs/longboat.1` is mdoc, read by 10.9's `nroff -mdoc` and by modern `mandoc`. `tests/manpage-test.sh`
holds it to every command `longboat --help` names and, where `mandoc` is installed, to a clean
`mandoc -Tlint`. The pkg installs it at `/usr/local/mavergreen/longboat/share/man/man1/longboat.1`, which
the `mavergreen` helper links into `/usr/local/mavergreen/share/man`.
