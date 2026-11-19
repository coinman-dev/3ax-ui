#!/bin/bash
# 3AX-UI: build x-ui-linux-<platform>.tar.gz from this checkout, in the layout
# release.yml ships, so install.sh / update.sh run from a clone install exactly
# what was built here instead of a downloaded release.
#
# Usage: scripts/build-local.sh <output.tar.gz> <platform>
#   platform: amd64 | arm64 | armv7 | armv6 | 386 (the ones DockerInit.sh fetches for)
#
# Missing toolchains are fetched into XUI_BUILD_TOOLS (default /opt/3ax-ui-build)
# and never replace a system Go or Node. Xray, geo files and mtg-multi are cached
# there per platform and refetched when DockerInit.sh pins another Xray or after
# XUI_BUILD_BIN_MAX_AGE_DAYS (default 7) days, so a rebuild costs no 100+ MB download.
set -euo pipefail

red='\033[0;31m'
green='\033[0;32m'
plain='\033[0m'

die() {
    echo -e "${red}build-local: $*${plain}" >&2
    exit 1
}
info() { echo -e "${green}build-local: $*${plain}"; }

[[ $# -eq 2 ]] || die "usage: $0 <output.tar.gz> <platform>"
out="$1"
platform="$2"
repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tools="${XUI_BUILD_TOOLS:-/opt/3ax-ui-build}"
bin_max_age_days="${XUI_BUILD_BIN_MAX_AGE_DAYS:-7}"

case "$platform" in
    amd64 | arm64 | armv7 | armv6 | 386) ;;
    *) die "no local build for ${platform}: DockerInit.sh fetches Xray and mtg-multi only for amd64, arm64, armv7, armv6 and 386" ;;
esac

install_packages() {
    if command -v apt-get > /dev/null 2>&1; then
        apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@"
    elif command -v dnf > /dev/null 2>&1; then
        dnf install -y -q "$@"
    elif command -v yum > /dev/null 2>&1; then
        yum install -y -q "$@"
    elif command -v apk > /dev/null 2>&1; then
        apk add --no-cache "$@"
    elif command -v pacman > /dev/null 2>&1; then
        pacman -Sy --noconfirm --needed "$@"
    elif command -v zypper > /dev/null 2>&1; then
        zypper -q install -y "$@"
    else
        return 1
    fi
}

# The panel links SQLite through CGo, so a C compiler is not optional.
ensure_build_packages() {
    local missing=()
    command -v gcc > /dev/null 2>&1 || missing+=(gcc)
    command -v unzip > /dev/null 2>&1 || missing+=(unzip)
    command -v xz > /dev/null 2>&1 || missing+=(xz)
    command -v curl > /dev/null 2>&1 || missing+=(curl)
    [[ ${#missing[@]} -eq 0 ]] && return 0
    info "installing build packages: ${missing[*]}"
    local pkgs=()
    for p in "${missing[@]}"; do
        case "$p" in
            gcc)
                if command -v apt-get > /dev/null 2>&1; then pkgs+=(build-essential)
                elif command -v apk > /dev/null 2>&1; then pkgs+=(build-base)
                elif command -v pacman > /dev/null 2>&1; then pkgs+=(base-devel)
                else pkgs+=(gcc); fi
                ;;
            xz)
                if command -v apt-get > /dev/null 2>&1; then pkgs+=(xz-utils); else pkgs+=(xz); fi
                ;;
            *) pkgs+=("$p") ;;
        esac
    done
    install_packages "${pkgs[@]}" || die "could not install ${pkgs[*]}; install them and run again"
}

version_at_least() {
    [[ "$(printf '%s\n' "$2" "$1" | sort -V | head -n1)" == "$2" ]]
}

# Go 1.21+ fetches the exact toolchain go.mod names by itself (GOTOOLCHAIN=auto),
# so an older system Go is only a bootstrap; without one, download go.mod's.
ensure_go() {
    if command -v go > /dev/null 2>&1 && version_at_least "$(go env GOVERSION 2> /dev/null | sed 's/^go//')" "1.21"; then
        info "using $(go version)"
        return 0
    fi
    local want goarch
    want=$(awk '/^go [0-9]/ {print $2; exit}' "${repo}/go.mod")
    case "$platform" in
        amd64) goarch=amd64 ;;
        arm64) goarch=arm64 ;;
        armv7 | armv6) goarch=armv6l ;;
        386) goarch=386 ;;
    esac
    if [[ ! -x "${tools}/go/bin/go" ]] || [[ "$("${tools}/go/bin/go" env GOVERSION)" != "go${want}" ]]; then
        local file="go${want}.linux-${goarch}.tar.gz" tmp
        tmp=$(mktemp -d)
        info "downloading Go ${want}"
        curl -fsSL --retry 3 -o "${tmp}/${file}" "https://dl.google.com/go/${file}" || die "Go ${want} download failed"
        local sum
        sum=$(curl -fsSL --retry 3 "https://dl.google.com/go/${file}.sha256") || die "Go ${want} checksum download failed"
        echo "${sum}  ${tmp}/${file}" | sha256sum -c --status - || die "Go ${want} checksum mismatch"
        rm -rf "${tools}/go"
        mkdir -p "${tools}"
        tar -C "${tools}" -xzf "${tmp}/${file}"
        rm -rf "${tmp}"
    fi
    export PATH="${tools}/go/bin:${PATH}"
    info "using $(go version)"
}

ensure_node() {
    local want_major
    want_major=$(tr -dc '0-9' < "${repo}/.nvmrc")
    if command -v node > /dev/null 2>&1; then
        local have
        have=$(node -p 'process.versions.node.split(".")[0]' 2> /dev/null || echo 0)
        if [[ "$have" -ge "$want_major" ]]; then
            info "using Node $(node --version)"
            return 0
        fi
    fi
    if [[ ! -x "${tools}/node/bin/node" ]] || [[ "$("${tools}/node/bin/node" -p 'process.versions.node.split(".")[0]')" != "$want_major" ]]; then
        local narch
        case "$platform" in
            amd64) narch=x64 ;;
            arm64) narch=arm64 ;;
            armv7) narch=armv7l ;;
            *) die "Node ${want_major} publishes no build for ${platform}; install Node ${want_major}+ yourself or use a release" ;;
        esac
        local base="https://nodejs.org/dist/latest-v${want_major}.x" tmp sums file
        tmp=$(mktemp -d)
        sums=$(curl -fsSL --retry 3 "${base}/SHASUMS256.txt") || die "Node ${want_major} release list download failed"
        file=$(awk -v a="linux-${narch}.tar.xz" '$2 ~ a"$" {print $2; exit}' <<< "$sums")
        [[ -n "$file" ]] || die "no Node ${want_major} build for linux-${narch}"
        info "downloading ${file}"
        curl -fsSL --retry 3 -o "${tmp}/${file}" "${base}/${file}" || die "Node download failed"
        (cd "$tmp" && grep " ${file}\$" <<< "$sums" | sha256sum -c --status -) || die "Node checksum mismatch"
        rm -rf "${tools}/node"
        mkdir -p "${tools}/node"
        tar -C "${tools}/node" --strip-components=1 -xJf "${tmp}/${file}"
        rm -rf "${tmp}"
    fi
    export PATH="${tools}/node/bin:${PATH}"
    info "using Node $(node --version)"
}

# Root builds in someone's clone; hand what it wrote back to the clone's owner.
restore_ownership() {
    local owner
    owner=$(stat -c '%u:%g' "$repo")
    [[ "$owner" == "0:0" ]] && return 0
    chown -R "$owner" "${repo}/frontend/node_modules" "${repo}/internal/web/dist" "${repo}/frontend/public/openapi.json" 2> /dev/null || true
}

# Xray / geo / mtg-multi as DockerInit.sh fetches them, kept between builds.
fetch_bin() {
    local dest="$1" cache="${tools}/bin-cache/${platform}" pinned stamp
    pinned=$(grep -oE 'Xray-core/releases/download/v[0-9.]+' "${repo}/DockerInit.sh" | head -n1 | sed 's#.*/##')
    [[ -n "$pinned" ]] || die "cannot read the pinned Xray version from DockerInit.sh"
    stamp="${cache}/xray-version"
    if [[ -f "$stamp" && "$(cat "$stamp")" == "$pinned" && -d "${cache}/build/bin" ]] &&
        [[ -z "$(find "$stamp" -mtime +"${bin_max_age_days}" 2> /dev/null)" ]]; then
        info "reusing cached Xray ${pinned}, geo files and mtg-multi"
    else
        info "fetching Xray ${pinned}, geo files and mtg-multi"
        rm -rf "$cache"
        mkdir -p "$cache"
        (cd "$cache" && sh "${repo}/DockerInit.sh" "$platform") || die "DockerInit.sh failed"
        echo "$pinned" > "$stamp"
    fi
    mkdir -p "$dest"
    cp -a "${cache}/build/bin/." "$dest/"
    # DockerInit.sh names arm binaries the way the panel runs them; the release
    # archive keeps the platform name and install.sh / update.sh rename it.
    if [[ "$platform" == armv7 || "$platform" == armv6 ]]; then
        [[ -f "${dest}/xray-linux-arm32" ]] && mv "${dest}/xray-linux-arm32" "${dest}/xray-linux-${platform}"
        [[ -f "${dest}/mtg-linux-arm" ]] && mv "${dest}/mtg-linux-arm" "${dest}/mtg-linux-${platform}"
    fi
    return 0
}

ensure_build_packages
ensure_go
ensure_node
trap restore_ownership EXIT

info "building the frontend"
(cd "${repo}/frontend" && HUSKY=0 npm ci --no-audit --no-fund && npm run build) || die "frontend build failed"

work=$(mktemp -d)
trap 'restore_ownership; rm -rf "$work"' EXIT
mkdir -p "${work}/x-ui"

info "building the panel binary"
(cd "$repo" && CGO_ENABLED=1 GOTOOLCHAIN=auto go build -trimpath -ldflags "-w -s" -o "${work}/x-ui/x-ui" .) || die "go build failed"

cp "${repo}/x-ui.sh" "${repo}/x-ui.service.debian" "${repo}/x-ui.service.arch" "${repo}/x-ui.service.rhel" "${work}/x-ui/"
fetch_bin "${work}/x-ui/bin"

tar -C "$work" -czf "$out" x-ui
info "wrote ${out} (version $(tr -d '[:space:]' < "${repo}/internal/config/version"))"
