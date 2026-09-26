#!/bin/sh
# Install siltide on Linux or macOS.
#
#   curl -fsSLO https://raw.githubusercontent.com/moezdil/siltide/main/packaging/install/install.sh
#   sh install.sh
#
# Download the script and then run it, rather than piping it into sh. A
# pipeline reports the status of its LAST command, so a download that 404s
# hands sh an empty script: nothing runs, the line exits 0, and the machine
# has installed nothing while appearing to have succeeded. Two lines also
# mean the script can be read before it is run, which is the other half of
# the reason not to pipe it.
#
# Everything is inside main(), called on the last line: a script read from a
# broken connection is a syntax error that does nothing, rather than half a
# script that does half of this.
#
# POSIX sh, not bash: this has to work under dash on Debian and BusyBox ash
# on Alpine, which are /bin/sh on machines people actually run.
set -eu

REPO="moezdil/siltide"
BIN="siltide"

die() { echo "install: $*" >&2; exit 1; }

usage() {
	cat <<'USAGE'
siltide installer

  --version <x.y.z>   install a given release instead of the latest
  --dir <path>        install into this directory
  -h, --help          this text

Without --dir it installs into /usr/local/bin when that is writable, and
~/.local/bin when it is not.
USAGE
}

# target maps this machine to the asset built for it.
target() {
	os="$(uname -s)"
	arch="$(uname -m)"
	case "$os" in
		Linux) os=linux ;;
		Darwin) os=darwin ;;
		*) die "no build for $os: see the releases page" ;;
	esac
	case "$arch" in
		x86_64 | amd64) arch=amd64 ;;
		aarch64 | arm64) arch=arm64 ;;
		*) die "no build for $arch: see the releases page" ;;
	esac
	echo "${BIN}-${os}-${arch}"
}

# tag_names prints the tag of every release in a releases API response,
# newest first, without the leading v.
tag_names() {
	tr ',' '\n' |
		sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\{0,1\}\([^"]*\)".*/\1/p'
}

# latest_version is the newest release worth installing.
#
# /releases/latest answers with the newest STABLE release and 404s when there
# is not one yet, which is every project before its first tag and was this
# one: the script reported "could not work out the latest version" on a repo
# publishing a build for every push. So when there is no stable release, take
# the newest release there is. Somebody running the installer wants siltide,
# not a lecture about release channels -- but they are told which one it is.
latest_version() {
	v="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null |
		tag_names | head -1)"
	if [ -n "$v" ]; then
		echo "$v"
		return
	fi
	curl -fsSL "https://api.github.com/repos/${REPO}/releases?per_page=30" 2>/dev/null |
		tag_names | head -1
}

# install_dir is where the binary lands, and it must be on PATH or the
# install has only moved the problem.
install_dir() {
	if [ -n "${DIR:-}" ]; then
		echo "$DIR"
		return
	fi
	if [ -w /usr/local/bin ] 2>/dev/null; then
		echo /usr/local/bin
		return
	fi
	echo "$HOME/.local/bin"
}

# verify checks the download against the checksums published with it. A
# binary that is about to be put on PATH is not the place to skip this.
verify() {
	file="$1" sums="$2" name="$3"
	want="$(sed -n "s/^\([0-9a-f]\{64\}\)[[:space:]][[:space:]]*${name}$/\1/p" "$sums" | head -1)"
	[ -n "$want" ] || die "no checksum published for ${name}"
	if command -v sha256sum >/dev/null 2>&1; then
		got="$(sha256sum "$file" | cut -d' ' -f1)"
	elif command -v shasum >/dev/null 2>&1; then
		got="$(shasum -a 256 "$file" | cut -d' ' -f1)"
	else
		die "neither sha256sum nor shasum is here to check the download with"
	fi
	[ "$got" = "$want" ] || die "checksum mismatch for ${name}: got ${got}, expected ${want}"
}

main() {
	version=""
	DIR=""
	while [ $# -gt 0 ]; do
		case "$1" in
			--version) version="${2:-}"; [ -n "$version" ] || die "--version needs a value"; shift 2 ;;
			--version=*) version="${1#--version=}"; [ -n "$version" ] || die "--version needs a value"; shift ;;
			--dir) DIR="${2:-}"; [ -n "$DIR" ] || die "--dir needs a value"; shift 2 ;;
			--dir=*) DIR="${1#--dir=}"; [ -n "$DIR" ] || die "--dir needs a value"; shift ;;
			-h | --help) usage; return 0 ;;
			*) die "unknown option: $1 (try --help)" ;;
		esac
	done

	command -v curl >/dev/null 2>&1 || die "curl is needed to download anything"
	asset="$(target)"
	[ -n "$version" ] || version="$(latest_version)"
	[ -n "$version" ] ||
		die "no release found for ${REPO}; check the releases page, or pass --version"
	version="${version#v}"
	case "$version" in
		*-*) echo "install: no stable release yet; taking the pre-release ${version}" ;;
	esac

	base="https://github.com/${REPO}/releases/download/v${version}"
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT

	echo "install: siltide ${version} for ${asset#siltide-}"
	curl -fsSL "${base}/${asset}" -o "${tmp}/${asset}" || die "no asset ${asset} on release v${version}"
	curl -fsSL "${base}/checksums.txt" -o "${tmp}/checksums.txt" || die "release v${version} publishes no checksums"
	verify "${tmp}/${asset}" "${tmp}/checksums.txt" "$asset"

	dir="$(install_dir)"
	mkdir -p "$dir"
	chmod +x "${tmp}/${asset}"
	# A rename, so an interrupted install cannot leave half a binary on PATH.
	mv "${tmp}/${asset}" "${dir}/${BIN}.new" 2>/dev/null ||
		die "cannot write to ${dir}: pass --dir, or run this with the rights to write there"
	mv "${dir}/${BIN}.new" "${dir}/${BIN}"

	echo "install: ${dir}/${BIN}"
	case ":${PATH}:" in
		*":${dir}:"*) "${dir}/${BIN}" --version ;;
		*) echo "install: ${dir} is not on PATH; add it, or run ${dir}/${BIN} directly" ;;
	esac
}

main "$@"
