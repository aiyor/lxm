#!/bin/sh
# lxm installer - download and install the lxm binary from GitHub Releases.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/aiyor/lxm/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/aiyor/lxm/main/install.sh | LXM_VERSION=1.0.0 sh
#   curl -fsSL https://raw.githubusercontent.com/aiyor/lxm/main/install.sh | PREFIX=$HOME/.local sh
#
# Environment variables:
#   LXM_VERSION   Release tag to install (default: latest)
#   PREFIX        Install directory (default: /usr/local)

set -eu

REPO="aiyor/lxm"
BASE_URL="https://github.com/${REPO}/releases/download"
VERSION="${LXM_VERSION:-latest}"
PREFIX="${PREFIX:-/usr/local}"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "${OS}" in
  linux) ;;
  darwin) ;;
  *)
    echo "error: unsupported OS '${OS}'" >&2
    exit 1
    ;;
esac

case "${ARCH}" in
  x86_64 | amd64) ARCH="amd64" ;;
  aarch64 | arm64) ARCH="arm64" ;;
  *)
    echo "error: unsupported architecture '${ARCH}'" >&2
    exit 1
    ;;
esac

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

if [ "${VERSION}" = "latest" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')"
fi

VERSION="${VERSION#v}"

TARBALL="lxm_${VERSION}_${OS}_${ARCH}.tar.gz"
TARBALL_URL="${BASE_URL}/v${VERSION}/${TARBALL}"

echo "Downloading ${TARBALL}..."
curl -fsSL -o "${TMPDIR}/${TARBALL}" "${TARBALL_URL}"

if command -v sha256sum >/dev/null 2>&1; then
  CHECKSUM_CMD="sha256sum"
else
  CHECKSUM_CMD="shasum -a 256"
fi

curl -fsSL -o "${TMPDIR}/checksums.txt" "${BASE_URL}/v${VERSION}/checksums.txt"
(
  cd "${TMPDIR}"
  grep " ${TARBALL}\$" checksums.txt | ${CHECKSUM_CMD} -c -
)

tar -xzf "${TMPDIR}/${TARBALL}" -C "${TMPDIR}"

mkdir -p "${PREFIX}/bin"
install -m 0755 "${TMPDIR}/lxm" "${PREFIX}/bin/lxm"

echo
echo "lxm ${VERSION} installed to ${PREFIX}/bin/lxm"
echo "Make sure ${PREFIX}/bin is on your PATH, then run:"
echo "  lxm --version"
