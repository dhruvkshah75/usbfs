#!/usr/bin/env bash
set -euo pipefail

usage() {
    printf '%s\n' 'Usage: ./install.sh [--skip-system-deps]' \
        'Build usbfs and install it to ~/.local/bin/usbfs.' \
        'By default, install build and device support packages on Ubuntu 24.04.' \
        'Use --skip-system-deps if Go and the system tools are already installed.'
}

case "${1-}" in
    -h|--help) usage; exit 0 ;;
    ''|--skip-system-deps) ;;
    *) usage >&2; exit 2 ;;
esac
if (( $# > 1 )); then
    usage >&2
    exit 2
fi

if [[ $OSTYPE != linux* || $EUID == 0 ]]; then
    printf '%s\n' 'Run this installer as a regular Linux user.' >&2
    exit 1
fi

cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
go_bin=$(command -v go || true)
if [[ -z $go_bin && -x /usr/lib/go-1.25/bin/go ]]; then
    go_bin=/usr/lib/go-1.25/bin/go
fi

if [[ ${1-} != --skip-system-deps ]]; then
    . /etc/os-release
    if [[ ${ID-} != ubuntu || ${VERSION_ID-} != 24.04 ]]; then
        printf '%s\n' 'Install the prerequisites for your distribution (see README.md), then run ./install.sh --skip-system-deps.' >&2
        exit 1
    fi

    packages=(fuse3 xdg-utils jmtpfs ifuse libimobiledevice-utils usbmuxd)
    if [[ -z $go_bin ]]; then
        packages+=(golang-1.25-go)
        go_bin=/usr/lib/go-1.25/bin/go
    fi
    sudo apt-get update
    if ! sudo apt-get install --no-remove "${packages[@]}"; then
        printf '%s\n' 'If Ubuntu cannot locate a package, enable Universe with sudo add-apt-repository universe and retry.' >&2
        exit 1
    fi
fi

if [[ ! -x $go_bin ]]; then
    printf '%s\n' 'Install Go 1.25.0 or newer from https://go.dev/doc/install, add go to PATH, and retry.' >&2
    exit 1
fi

bin_dir=$HOME/.local/bin
mkdir -p -- "$bin_dir"
build_dir=$(mktemp -d -- "$bin_dir/.usbfs-build.XXXXXXXX")
trap 'rm -rf -- "$build_dir"' EXIT
"$go_bin" build -mod=readonly -o "$build_dir/usbfs" ./cmd/usbfs
mv -fT -- "$build_dir/usbfs" "$bin_dir/usbfs"

printf 'Installed usbfs to %s\n' "$bin_dir/usbfs"
printf 'Launch with %q\n' "$bin_dir/usbfs"
