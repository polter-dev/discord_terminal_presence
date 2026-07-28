#!/bin/sh
set -eu

REPO="polter-dev/discord_terminal_presence"
DEFAULT_BINDIR="/usr/local/bin"

shell_quote() {
	escaped=$(printf '%s' "$1" | sed "s/'/'\\\\''/g") || escaped=$1
	printf "'%s'" "$escaped"
}

print_retry_command() {
	printf 'termp install: retry with: curl -fsSL https://termp.polter.sh/install.sh |'
	if [ "${VERSION+x}" = x ]; then
		printf ' VERSION='
		shell_quote "$VERSION"
	fi
	if [ "${BINDIR+x}" = x ]; then
		printf ' BINDIR='
		shell_quote "$BINDIR"
	fi
	if [ "${TERMP_DOWNLOAD_CHANNEL+x}" = x ]; then
		printf ' TERMP_DOWNLOAD_CHANNEL='
		shell_quote "$TERMP_DOWNLOAD_CHANNEL"
	fi
	printf ' sh\n'
}

err() {
	printf 'termp install: %s\n' "$*" >&2
	print_retry_command >&2
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

	if ! download "$api_url" "$tmp_json"; then
		err "failed to download latest GitHub release metadata"
	fi
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

valid_semver_tag() {
	awk -v version="$1" '
		function numeric(value) {
			return value ~ /^[0-9]+$/ && (value == "0" || substr(value, 1, 1) != "0")
		}
		function identifiers(value, prerelease, parts, count, i) {
			if (value == "") {
				return 0
			}
			count = split(value, parts, ".")
			for (i = 1; i <= count; i++) {
				if (parts[i] == "" || parts[i] !~ /^[0-9A-Za-z-]+$/) {
					return 0
				}
				if (prerelease && parts[i] ~ /^[0-9]+$/ && !numeric(parts[i])) {
					return 0
				}
			}
			return 1
		}
		BEGIN {
			if (substr(version, 1, 1) == "v") {
				version = substr(version, 2)
			}
			if (version == "") {
				exit 1
			}
			build_count = split(version, build, "+")
			if (build_count > 2 ||
			    (build_count == 2 && !identifiers(build[2], 0))) {
				exit 1
			}
			base = build[1]
			dash = index(base, "-")
			if (dash > 0) {
				if (!identifiers(substr(base, dash + 1), 1)) {
					exit 1
				}
				base = substr(base, 1, dash - 1)
			}
			if (split(base, core, ".") != 3 ||
			    !numeric(core[1]) || !numeric(core[2]) || !numeric(core[3])) {
				exit 1
			}
		}
	'
}

checksum_error() {
	printf 'termp install: %s\n' "$*" >&2
	return 1
}

verify_checksum() {
	checksums=$1
	archive=$2
	archive_name=$3

	match_count=$(awk -v file="$archive_name" '$2 == file { count++ } END { print count+0 }' "$checksums")
	if [ "$match_count" -ne 1 ]; then
		checksum_error "expected exactly one checksum for $archive_name, found $match_count"
		return 1
	fi
	expected=$(awk -v file="$archive_name" '$2 == file { print $1 }' "$checksums")
	if [ -z "$expected" ]; then
		checksum_error "checksum for $archive_name not found"
		return 1
	fi
	case $expected in
	*[!0-9a-fA-F]*)
		checksum_error "invalid SHA-256 checksum for $archive_name"
		return 1
		;;
	esac
	if [ "${#expected}" -ne 64 ]; then
		checksum_error "invalid SHA-256 checksum for $archive_name"
		return 1
	fi

	if have sha256sum; then
		if ! actual=$(sha256sum "$archive" | awk '{ print $1 }'); then
			checksum_error "failed to calculate SHA-256 checksum for $archive_name"
			return 1
		fi
	elif have shasum; then
		if ! actual=$(shasum -a 256 "$archive" | awk '{ print $1 }'); then
			checksum_error "failed to calculate SHA-256 checksum for $archive_name"
			return 1
		fi
	else
		checksum_error "sha256sum or shasum is required to verify downloads"
		return 1
	fi

	if [ "$actual" != "$expected" ]; then
		checksum_error "checksum mismatch for $archive_name"
		return 1
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
		if ! install_tmp=$(mktemp "$bindir/.termp.tmp.XXXXXX"); then
			err "failed to create staging file in $bindir"
		fi
		if ! cp "$src" "$install_tmp"; then
			rm -f "$install_tmp" || true
			err "failed to stage termp in $bindir"
		fi
		if ! chmod 0755 "$install_tmp"; then
			rm -f "$install_tmp" || true
			err "failed to make staged termp executable"
		fi
		if ! mv -f "$install_tmp" "$dest"; then
			rm -f "$install_tmp" || true
			err "failed to install termp to $dest"
		fi
	else
		if ! have sudo; then
			err "$bindir is not writable and sudo is not available"
		fi
		printf 'Installing termp to %s with sudo because the directory is not writable.\n' "$bindir"
		if ! install_tmp=$(sudo mktemp "$bindir/.termp.tmp.XXXXXX"); then
			err "failed to create staging file in $bindir with sudo"
		fi
		if ! sudo cp "$src" "$install_tmp"; then
			sudo rm -f "$install_tmp" || true
			err "failed to stage termp in $bindir"
		fi
		if ! sudo chmod 0755 "$install_tmp"; then
			sudo rm -f "$install_tmp" || true
			err "failed to make staged termp executable"
		fi
		if ! sudo mv -f "$install_tmp" "$dest"; then
			sudo rm -f "$install_tmp" || true
			err "failed to install termp to $dest"
		fi
	fi
}

stdout_width() {
	width=${COLUMNS:-}
	case $width in
	'' | *[!0-9]* | 0) width= ;;
	esac

	if [ -z "$width" ] && [ -t 1 ] && have tput && [ -n "${TERM:-}" ]; then
		width=$(tput cols 2>/dev/null || true)
		case $width in
		'' | *[!0-9]* | 0) width= ;;
		esac
	fi

	if [ -z "$width" ]; then
		width=80
	elif [ "$width" -gt 80 ]; then
		width=80
	fi
	printf '%s\n' "$width"
}

print_setup_cta() {
	box_width=$(stdout_width)
	if [ "$box_width" -lt 13 ]; then
		# A bordered 11-character command cannot fit below 13 columns.
		printf '\n\nTERMP INSTALLED\n\nRUN THIS NOW:\ntermp setup\n\nDiscord stays blank until you do.\n\n'
		printf 'termp start\ntermp uninstall\n'
		return
	fi

	if [ "$box_width" -ge 40 ]; then
		pad=3
	elif [ "$box_width" -ge 17 ]; then
		pad=2
	elif [ "$box_width" -ge 15 ]; then
		pad=1
	else
		pad=0
	fi
	inner_width=$((box_width - 2))
	content_width=$((inner_width - (pad * 2)))
	rule=$(printf '%*s' "$inner_width" '' | tr ' ' '-')

	accent=
	header=
	next=
	command_style=
	consequence=
	muted=
	reset=
	if [ -t 1 ] && [ "${NO_COLOR+x}" != x ]; then
		esc=$(printf '\033')
		accent="${esc}[1;95m"
		header="${esc}[1;96m"
		next="${esc}[1;93m"
		command_style="${esc}[1;30;103m"
		consequence="${esc}[1;97m"
		muted="${esc}[2;90m"
		reset="${esc}[0m"
	fi

	box_blank() {
		printf '%s|%s%*s%s|%s\n' "$accent" "$reset" "$inner_width" '' "$accent" "$reset"
	}

	box_text() {
		text_value=$1
		text_style=$2
		printf '%s|%s%*s%s%-*s%s%*s%s|%s\n' \
			"$accent" "$reset" "$pad" '' "$text_style" "$content_width" "$text_value" "$reset" "$pad" '' "$accent" "$reset"
	}

	box_command() {
		command_value='termp setup'
		if [ "$content_width" -ge 21 ]; then
			command_value='>>>  termp setup  <<<'
		fi
		command_length=${#command_value}
		left_space=$(((content_width - command_length) / 2))
		right_space=$((content_width - command_length - left_space))
		printf '%s|%s%*s%s%*s%s%*s%s%*s%s|%s\n' \
			"$accent" "$reset" "$pad" '' "$command_style" "$left_space" '' "$command_value" "$right_space" '' "$reset" "$pad" '' "$accent" "$reset"
	}

	printf '\n\n'
	printf '%s+%s+%s\n' "$accent" "$rule" "$reset"
	box_blank
	if [ "$content_width" -ge 15 ]; then
		box_text 'TERMP INSTALLED' "$header"
	else
		box_text 'INSTALLED' "$header"
	fi
	box_blank
	if [ "$content_width" -ge 25 ]; then
		box_text 'NEXT STEP - RUN THIS NOW:' "$next"
	elif [ "$content_width" -ge 13 ]; then
		box_text 'RUN THIS NOW:' "$next"
	else
		box_text 'RUN NOW:' "$next"
	fi
	box_blank
	box_command
	box_blank
	if [ "$content_width" -ge 51 ]; then
		box_text 'Nothing shows on your Discord profile until you do.' "$consequence"
	elif [ "$content_width" -ge 29 ]; then
		box_text 'Nothing shows on your' "$consequence"
		box_text 'Discord profile until you do.' "$consequence"
	elif [ "$content_width" -ge 19 ]; then
		box_text 'Discord stays blank' "$consequence"
		box_text 'until you do.' "$consequence"
	else
		box_text 'No Discord' "$consequence"
		box_text 'until setup' "$consequence"
	fi
	box_blank
	printf '%s+%s+%s\n' "$accent" "$rule" "$reset"
	printf '\n\n'

	if [ "$box_width" -ge 27 ]; then
		printf '%soptional:  termp start%s\n' "$muted" "$reset"
		printf '%slogin off: termp uninstall%s\n' "$muted" "$reset"
	else
		printf '%stermp start%s\n' "$muted" "$reset"
		printf '%stermp uninstall%s\n' "$muted" "$reset"
	fi
}

if ! tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/termp-install.XXXXXX"); then
	err "failed to create temporary directory"
fi
trap 'rm -rf "$tmpdir" || true' EXIT HUP INT TERM

os=$(map_os)
arch=$(map_arch)
bindir=${BINDIR:-$DEFAULT_BINDIR}

if [ "${VERSION:-}" ]; then
	tag=$VERSION
else
	tag=$(fetch_latest_tag "$tmpdir/latest.json")
fi
if ! valid_semver_tag "$tag"; then
	err "invalid release tag: $tag"
fi
version=${tag#v}

archive_name="termp_${version}_${os}_${arch}.tar.gz"
base_url="https://github.com/$REPO/releases/download/$tag"
archive_path="$tmpdir/$archive_name"
checksums_path="$tmpdir/checksums.txt"
extract_dir="$tmpdir/extract"

# The archive is pinned to the exact release tag and verified against that
# release's SHA-256 checksums.
printf 'Downloading termp %s for %s/%s...\n' "$tag" "$os" "$arch"
download_channel=${TERMP_DOWNLOAD_CHANNEL:-curl}
case $download_channel in
curl | update) ;;
*) err "invalid download channel: $download_channel" ;;
esac
worker_archive_url=${TERMP_DOWNLOAD_URL:-"https://termp.polter.sh/dl/$download_channel/$os/$arch/$tag"}
worker_archive_downloaded=true
if ! download "$worker_archive_url" "$archive_path"; then
	worker_archive_downloaded=false
fi
if ! download "$base_url/checksums.txt" "$checksums_path"; then
	err "failed to download checksums for $tag"
fi
if [ "$worker_archive_downloaded" = false ] ||
	! (verify_checksum "$checksums_path" "$archive_path" "$archive_name"); then
	if ! download "$base_url/$archive_name" "$archive_path"; then
		err "failed to download $archive_name"
	fi
	if ! verify_checksum "$checksums_path" "$archive_path" "$archive_name"; then
		err "failed to verify $archive_name"
	fi
fi

if ! mkdir -p "$extract_dir"; then
	err "failed to create archive extraction directory"
fi
if ! tar -xzf "$archive_path" -C "$extract_dir"; then
	err "failed to extract $archive_name"
fi

binary_path=$(find "$extract_dir" -type f -name termp -perm -111 | head -n 1)
if [ -z "$binary_path" ]; then
	binary_path=$(find "$extract_dir" -type f -name termp | head -n 1)
fi
if [ -z "$binary_path" ]; then
	err "archive did not contain a termp binary"
fi

install_binary "$binary_path" "$bindir"

printf 'termp installed to %s/termp\n' "$bindir"
if [ -n "${XDG_CONFIG_HOME:-}" ]; then
	config_path="$XDG_CONFIG_HOME/termp/config.toml"
elif [ -n "${HOME:-}" ]; then
	config_path="$HOME/.config/termp/config.toml"
else
	config_path=
fi

if [ -n "$config_path" ] && [ -e "$config_path" ]; then
	printf 'Next steps:\n'
	printf '  termp start    # run the daemon in the background\n'
	printf '  termp install  # install and start the login autostart service\n'
else
	print_setup_cta
fi
