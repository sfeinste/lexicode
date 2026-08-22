#!/usr/bin/env sh
# Lexicode installer — one line, no dependencies beyond curl (or wget), tar-free.
#
#   curl -fsSL <base-url>/install.sh | sh
#
# It detects the platform, downloads the matching binary and its SHA256 from the same place,
# verifies the checksum, and installs to $LEXICODE_BIN_DIR (default /usr/local/bin, or
# ~/.local/bin when that is not writable).
#
# There is no public release host yet, so the base URL is configurable and defaults to a
# local dist/ directory — the output of `make release`:
#
#   LEXICODE_BASE_URL=file://$PWD/dist sh scripts/install.sh          # from a local build
#   LEXICODE_BASE_URL=https://example.com/lexicode/v1.0.0 sh install.sh
#
# Environment:
#   LEXICODE_BASE_URL  where the binaries and SHA256SUMS live (default: ./dist)
#   LEXICODE_VERSION   version segment of the file name (default: read from SHA256SUMS)
#   LEXICODE_BIN_DIR   install directory
set -eu

BINARY=lexicode
BASE_URL=${LEXICODE_BASE_URL:-"file://$(pwd)/dist"}
BIN_DIR=${LEXICODE_BIN_DIR:-}

say()  { printf '%s\n' "$*"; }
die()  { printf 'install: %s\n' "$*" >&2; exit 1; }

# ---- platform ---------------------------------------------------------------------------
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  darwin|linux) ;;
  *) die "unsupported operating system: $os (Lexicode ships darwin and linux builds)" ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $arch (Lexicode ships amd64 and arm64 builds)" ;;
esac

# ---- fetcher ----------------------------------------------------------------------------
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
else
  die "neither curl nor wget is installed"
fi

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  die "neither sha256sum nor shasum is installed; refusing to install unverified bytes"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

# ---- checksums first: they name the version -----------------------------------------------
say "Fetching checksums from $BASE_URL"
fetch "$BASE_URL/SHA256SUMS" "$tmp/SHA256SUMS" \
  || die "could not read $BASE_URL/SHA256SUMS — check LEXICODE_BASE_URL, or run 'make release' first"

version=${LEXICODE_VERSION:-}
if [ -z "$version" ]; then
  # lexicode_<version>_<os>_<arch>
  version=$(awk '{print $2}' "$tmp/SHA256SUMS" | head -1 | sed "s/^${BINARY}_//; s/_[a-z0-9]*_[a-z0-9]*$//")
fi
[ -n "$version" ] || die "could not determine the version from SHA256SUMS"

file="${BINARY}_${version}_${os}_${arch}"
want=$(grep " [*]\{0,1\}${file}\$" "$tmp/SHA256SUMS" | awk '{print $1}' | head -1)
[ -n "$want" ] || die "$file is not listed in SHA256SUMS (this release has no $os/$arch build)"

say "Downloading $file"
fetch "$BASE_URL/$file" "$tmp/$file" || die "could not download $BASE_URL/$file"

got=$(sha256 "$tmp/$file")
[ "$got" = "$want" ] || die "checksum mismatch for $file
  expected $want
  got      $got"
say "Checksum verified."

# ---- install ------------------------------------------------------------------------------
if [ -z "$BIN_DIR" ]; then
  if [ -w /usr/local/bin ] 2>/dev/null; then
    BIN_DIR=/usr/local/bin
  else
    BIN_DIR="$HOME/.local/bin"
  fi
fi
mkdir -p "$BIN_DIR"
chmod +x "$tmp/$file"
mv "$tmp/$file" "$BIN_DIR/$BINARY" || die "could not install into $BIN_DIR (set LEXICODE_BIN_DIR)"
say "Installed $BIN_DIR/$BINARY ($version)"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) say ""
     say "$BIN_DIR is not on your PATH. Add this to your shell profile:"
     say "    export PATH=\"$BIN_DIR:\$PATH\"" ;;
esac

say ""
say "Next:"
say "    $BINARY doctor      # check Docker, credentials, ports and disk"
say "    $BINARY serve       # start the dashboard on http://127.0.0.1:7717"
