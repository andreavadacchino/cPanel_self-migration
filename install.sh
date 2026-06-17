#!/usr/bin/env bash
#
# install.sh — installer for cPanel_self-migration
#
# Downloads the latest released linux/amd64 binary from GitHub, verifies the
# release SIGNATURE (ECDSA P-256) and the SHA256 checksum, and installs it under a
# per-user prefix ($HOME/.local by default). NO root/sudo is required. On a re-run
# the binary is REPLACED; an existing config file (configs/host.yaml) is NEVER
# touched. The SOURCE host is always read-only; this tool only ever writes to the
# DESTINATION.
#
# Usage (no root needed):
#   ./install.sh
#   curl -fsSL https://raw.githubusercontent.com/tis24dev/cPanel_self-migration/main/install.sh | bash
#
# Environment overrides:
#   VERSION   pin a specific release tag, e.g. v1.2.3 (default: latest)
#   PREFIX    install prefix (default: $HOME/.local; e.g. PREFIX=/usr/local needs sudo)
#
set -euo pipefail

usage() {
  cat <<'EOF'
install.sh — installer for cPanel_self-migration

Downloads the latest released linux/amd64 binary, verifies the release signature
(ECDSA P-256) and SHA256 checksum, and installs it under a per-user prefix
($HOME/.local by default). NO root/sudo is required. On a re-run the binary is
REPLACED; an existing config (configs/host.yaml) is never touched.

Usage (no root needed):
  ./install.sh
  curl -fsSL https://raw.githubusercontent.com/tis24dev/cPanel_self-migration/main/install.sh | bash

Environment:
  VERSION   pin a release tag, e.g. v1.2.3 (default: latest)
  PREFIX    install prefix (default: $HOME/.local; PREFIX=/usr/local needs sudo)
EOF
}

case "${1:-}" in
  -h | --help) usage; exit 0 ;;
  "") : ;;
  *) echo "❌ Unknown argument: $1 (use --help)" >&2; exit 1 ;;
esac

###############################################
# 1) Config & install location (user-writable — NO root/sudo needed)
###############################################
REPO="tis24dev/cPanel_self-migration"
PROJECT="cpanel-self-migration"          # GoReleaser project/binary name
BINARY="cpanel-self-migration"

# Install under a per-user prefix by default, so the installer needs NO root/sudo.
# Override PREFIX for a system-wide install (e.g. PREFIX=/usr/local), which then
# needs write access to that prefix (i.e. sudo).
PREFIX="${PREFIX:-$HOME/.local}"
TARGET_DIR="${PREFIX}/share/cPanel_self-migration"
TARGET_BIN="${TARGET_DIR}/${BINARY}"
CONFIG="${TARGET_DIR}/configs/host.yaml"
LINK="${PREFIX}/bin/${BINARY}"

# Create the install dirs up front and fail with a helpful hint if the prefix is
# not writable — never silently demand root.
if ! mkdir -p "${TARGET_DIR}/configs" "${PREFIX}/bin" 2>/dev/null || [ ! -w "${TARGET_DIR}" ]; then
  echo "❌ Cannot write to ${TARGET_DIR}."
  echo "   Pick a writable PREFIX (default: \$HOME/.local), e.g.:"
  echo "     PREFIX=\"\$HOME/.local\" bash install.sh"
  echo "   Or for a system-wide install: PREFIX=/usr/local sudo bash install.sh"
  exit 1
fi

# Pinned release-signing public key (ECDSA P-256). The matching private key lives
# ONLY in the project's GitHub Actions secret (CPANEL_KEY_RELEASE), so a release
# whose SHA256SUMS does not verify against this key is rejected.
# Fingerprint (sha256 of DER): 94ab621986047ff04b7f5b44953fa260d4fd356153dc6a45d898c0f09c4d4fa4
PUBKEY_PEM='-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEcnE2qS27//fod+j3kNz2RPOywRgu
YCWhr+gNUAhthdOoyO3Z1/XCohk+u6UMlie5W2XB/QeY2DcP0vIuSrefxA==
-----END PUBLIC KEY-----'

###############################################
# 3) Platform: the release only publishes linux/amd64
###############################################
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH_RAW="$(uname -m)"

if [ "${OS}" != "linux" ]; then
  echo "❌ Only Linux is published (got '${OS}'). Build from source: make build"
  exit 1
fi
case "${ARCH_RAW}" in
  x86_64 | amd64) ARCH="amd64" ;;
  *)
    echo "❌ The release only ships linux/amd64 (got '${ARCH_RAW}'). Build from source: make build"
    exit 1
    ;;
esac

echo "--------------------------------------------"
echo " cPanel_self-migration installer"
echo " OS:   ${OS}"
echo " Arch: ${ARCH}"
echo " Dir:  ${TARGET_DIR}"
echo "--------------------------------------------"

###############################################
# 4) Required tools + HTTP helpers
###############################################
fetch() {
  local url="$1"
  # Bounded timeouts + a few retries so a transient blip (or a stalled connection,
  # which bare curl would wait on indefinitely) doesn't hang or fail the install.
  # Both still write to stdout and exit non-zero only after the retries are spent.
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --connect-timeout 30 --max-time 600 \
      --retry 3 --retry-delay 2 --retry-connrefused "${url}"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O - --timeout=30 --tries=3 --waitretry=2 --retry-connrefused "${url}"
  else
    echo "❌ Neither curl nor wget is installed" >&2
    exit 1
  fi
}
download() { fetch "$1" >"$2"; }

command -v openssl >/dev/null 2>&1 || { echo "❌ openssl is required to verify the release signature"; exit 1; }
command -v sha256sum >/dev/null 2>&1 || { echo "❌ sha256sum is required"; exit 1; }
command -v tar >/dev/null 2>&1 || { echo "❌ tar is required"; exit 1; }

###############################################
# 5) Resolve the release tag (latest unless VERSION is set)
###############################################
LATEST_TAG="${VERSION:-}"
if [ -z "${LATEST_TAG}" ]; then
  LATEST_JSON="$(fetch "https://api.github.com/repos/${REPO}/releases/latest")"
  if command -v jq >/dev/null 2>&1; then
    LATEST_TAG="$(jq -r '.tag_name // empty' <<<"${LATEST_JSON}" 2>/dev/null || true)"
  fi
  if [ -z "${LATEST_TAG}" ] && [[ ${LATEST_JSON} =~ \"tag_name\"[[:space:]]*:[[:space:]]*\"([^\"]+)\" ]]; then
    LATEST_TAG="${BASH_REMATCH[1]}"
  fi
fi
if [ -z "${LATEST_TAG}" ]; then
  echo "❌ Could not detect the release tag (set VERSION=vX.Y.Z)"
  exit 1
fi
echo "📦 Release: ${LATEST_TAG}"

VER="${LATEST_TAG#v}" # archive names use the version WITHOUT the leading 'v'

###############################################
# 6) URLs
###############################################
ARCHIVE="${PROJECT}_${VER}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/${REPO}/releases/download/${LATEST_TAG}"
ARCHIVE_URL="${BASE}/${ARCHIVE}"
CHECKSUM_URL="${BASE}/SHA256SUMS"
SIG_URL="${BASE}/SHA256SUMS.sig"

###############################################
# 7) Work in a temp dir
###############################################
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT
cd "${TMP_DIR}"

###############################################
# 8) Download archive, checksum and signature
###############################################
echo "[+] Downloading ${ARCHIVE} ..."
download "${ARCHIVE_URL}" "${ARCHIVE}"

echo "[+] Downloading SHA256SUMS ..."
download "${CHECKSUM_URL}" "SHA256SUMS"

echo "[+] Downloading SHA256SUMS.sig ..."
if ! download "${SIG_URL}" "SHA256SUMS.sig"; then
  echo "❌ Could not download SHA256SUMS.sig for ${LATEST_TAG}"
  echo "   Refusing to install without a verifiable release signature."
  exit 1
fi

###############################################
# 9) Verify the SHA256SUMS signature (authenticity)
###############################################
echo "[+] Verifying release signature ..."
printf '%s\n' "${PUBKEY_PEM}" >pubkey.pem
if ! openssl dgst -sha256 -verify pubkey.pem -signature SHA256SUMS.sig SHA256SUMS >/dev/null 2>&1; then
  echo "❌ SHA256SUMS signature verification FAILED — refusing to install"
  exit 1
fi
echo "✔ Signature OK (release authenticity verified)"

###############################################
# 10) Verify the checksum (integrity)
###############################################
echo "[+] Verifying checksum ..."
awk -v f="${ARCHIVE}" '$2 == f' SHA256SUMS >CHECK
if [ ! -s CHECK ]; then
  echo "❌ Checksum entry not found for ${ARCHIVE}"
  exit 1
fi
sha256sum -c CHECK
echo "✔ Checksum OK"

###############################################
# 11) Extract only the binary
###############################################
echo "[+] Extracting ${BINARY} ..."
tar -xzf "${ARCHIVE}" "${BINARY}"
if [ ! -f "${BINARY}" ]; then
  echo "❌ Binary '${BINARY}' not found inside the archive"
  exit 1
fi

###############################################
# 12) Install the binary (REPLACE on re-run; atomic)
###############################################
mkdir -p "${TARGET_DIR}/configs"
echo "[+] Installing binary -> ${TARGET_BIN}"
install -m 0755 "${BINARY}" "${TARGET_BIN}.new"
mv -f "${TARGET_BIN}.new" "${TARGET_BIN}"

###############################################
# 13) Config template — install ONLY if absent; NEVER overwrite credentials
###############################################
if [ -e "${CONFIG}" ]; then
  echo "[=] Existing config kept untouched: ${CONFIG}"
elif fetch "https://raw.githubusercontent.com/${REPO}/${LATEST_TAG}/configs/host_template.yaml" >"${CONFIG}.tmp" 2>/dev/null; then
  mv -f "${CONFIG}.tmp" "${CONFIG}"
  chmod 600 "${CONFIG}"
  echo "[+] Wrote config template -> ${CONFIG} (chmod 600; fill in your credentials)"
else
  rm -f "${CONFIG}.tmp"
  echo "[!] Could not fetch the config template — create ${CONFIG} by hand (see the docs)"
fi

###############################################
# 14) PATH symlink (best effort)
###############################################
RUN="${TARGET_BIN}"
LINK_DIR="$(dirname "${LINK}")"
if ln -sf "${TARGET_BIN}" "${LINK}" 2>/dev/null; then
  echo "[+] Linked ${LINK} -> ${TARGET_BIN}"
  # Only shorten the "how to run" hint to the bare name if the link dir is on PATH;
  # ~/.local/bin often is not, so otherwise point at the full path.
  case ":${PATH}:" in
    *":${LINK_DIR}:"*) RUN="${BINARY}" ;;
    *)
      echo "[!] ${LINK_DIR} is not on your PATH — run via ${TARGET_BIN}, or add it:"
      echo "      echo 'export PATH=\"${LINK_DIR}:\$PATH\"' >> ~/.bashrc && . ~/.bashrc"
      ;;
  esac
fi

###############################################
# 15) Done
###############################################
echo "--------------------------------------------"
echo "✔ Installation completed successfully!"
echo " Binary: ${TARGET_BIN}"
echo " Config: ${CONFIG}"
echo "--------------------------------------------"
cat <<EOF

Next steps:
  1) Put your SRC/DEST credentials in the config (SOURCE is read-only):
       \${EDITOR:-nano} ${CONFIG}
  2) Dry-run (writes nothing — the default mode):
       ${RUN}
  3) When the dry-run looks right, apply:
       ${RUN} --apply

The binary finds its config at ${CONFIG} (next to itself), so it runs from any
directory. Full guide: https://github.com/${REPO}/blob/${LATEST_TAG}/docs/USAGE.md
EOF
