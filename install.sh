#!/bin/sh
set -eu

REPO="polter-dev/discord_terminal_presence"
DEFAULT_BINDIR="/usr/local/bin"

err() {
	printf 'termp install: %s\n' "$*" >&2
	exit 1
}

have() {
	command -v "$1" >/dev/null 2>&1
}

download() {
	url=$1
	dest=$2

	if have curl; then
		curl -fsSL "$url" -o "$dest"
	elif have wget; then
		wget -q "$url" -O "$dest"
	else
		err "curl or wget is required"
	fi
}

fetch_latest_tag() {
	api_url="https://api.github.com/repos/$REPO/releases/latest"
	tmp_json=$1

	download "$api_url" "$tmp_json"
	tag=$(sed -n 's/^[[:space:]]*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmp_json" | head -n 1)
	if [ -z "$tag" ]; then
		err "could not determine latest GitHub release tag"
	fi
	printf '%s\n' "$tag"
}

map_os() {
	case $(uname -s) in
		Darwin) printf 'darwin\n' ;;
		Linux) printf 'linux\n' ;;
		*) err "unsupported OS: $(uname -s)" ;;
	esac
}

map_arch() {
	case $(uname -m) in
		x86_64 | amd64) printf 'amd64\n' ;;
		arm64 | aarch64) printf 'arm64\n' ;;
		*) err "unsupported architecture: $(uname -m)" ;;
	esac
}

verify_checksum() {
	checksums=$1
	archive=$2
	archive_name=$3

	expected=$(awk -v file="$archive_name" '$2 == file { print $1 }' "$checksums" | head -n 1)
	if [ -z "$expected" ]; then
		err "checksum for $archive_name not found"
	fi

	if have sha256sum; then
		actual=$(sha256sum "$archive" | awk '{ print $1 }')
	elif have shasum; then
		actual=$(shasum -a 256 "$archive" | awk '{ print $1 }')
	else
		err "sha256sum or shasum is required to verify downloads"
	fi

	if [ "$actual" != "$expected" ]; then
		err "checksum mismatch for $archive_name"
	fi
}

install_binary() {
	src=$1
	bindir=$2
	dest="$bindir/termp"

	if [ ! -d "$bindir" ]; then
		err "install directory does not exist: $bindir"
	fi

	if [ -w "$bindir" ]; then
		cp "$src" "$dest"
		chmod 0755 "$dest"
	else
		if ! have sudo; then
			err "$bindir is not writable and sudo is not available"
		fi
		printf 'Installing termp to %s with sudo because the directory is not writable.\n' "$bindir"
		sudo cp "$src" "$dest"
		sudo chmod 0755 "$dest"
	fi
}

tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/termp-install.XXXXXX")
trap 'rm -rf "$tmpdir"' EXIT HUP INT TERM

os=$(map_os)
arch=$(map_arch)
bindir=${BINDIR:-$DEFAULT_BINDIR}

if [ "${VERSION:-}" ]; then
	tag=$VERSION
else
	tag=$(fetch_latest_tag "$tmpdir/latest.json")
fi
version=${tag#v}

archive_name="termp_${version}_${os}_${arch}.tar.gz"
base_url="https://github.com/$REPO/releases/download/$tag"
archive_path="$tmpdir/$archive_name"
checksums_path="$tmpdir/checksums.txt"
extract_dir="$tmpdir/extract"

printf 'Downloading termp %s for %s/%s...\n' "$tag" "$os" "$arch"
download "$base_url/$archive_name" "$archive_path"
download "$base_url/checksums.txt" "$checksums_path"
verify_checksum "$checksums_path" "$archive_path" "$archive_name"

mkdir -p "$extract_dir"
tar -xzf "$archive_path" -C "$extract_dir"

binary_path=$(find "$extract_dir" -type f -name termp -perm -111 | head -n 1)
if [ -z "$binary_path" ]; then
	binary_path=$(find "$extract_dir" -type f -name termp | head -n 1)
fi
if [ -z "$binary_path" ]; then
	err "archive did not contain a termp binary"
fi

install_binary "$binary_path" "$bindir"

printf 'termp installed to %s/termp\n' "$bindir"
printf 'Next steps:\n'
printf '  termp start    # run the daemon in the foreground\n'
printf '  termp install  # install and start the login autostart service\n'
