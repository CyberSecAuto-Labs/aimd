#!/usr/bin/env sh
# Install the latest aimd Linux package (.deb or .rpm).
# Detects architecture and package manager, downloads the matching release
# asset, and installs it. Override the version with: VERSION=0.1.0 sh install.sh
set -eu

REPO="CyberSecAuto-Labs/aimd"

arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*)
		echo "unsupported architecture: $arch" >&2
		exit 1
		;;
esac

if command -v dpkg >/dev/null 2>&1; then
	fmt=deb
	install_cmd="dpkg -i"
elif command -v rpm >/dev/null 2>&1; then
	fmt=rpm
	install_cmd="rpm -i"
else
	echo "no supported package manager found (need dpkg or rpm)" >&2
	exit 1
fi

version="${VERSION:-}"
if [ -z "$version" ]; then
	version=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" |
		grep '"tag_name"' | head -n1 | cut -d'"' -f4)
fi
version="${version#v}"
if [ -z "$version" ]; then
	echo "could not determine the latest version (set VERSION to override)" >&2
	exit 1
fi

file="aimd_${version}_linux_${arch}.${fmt}"
url="https://github.com/${REPO}/releases/download/v${version}/${file}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading ${file}..."
curl -fSL -o "${tmp}/${file}" "$url"

echo "Installing aimd ${version} (${arch}, ${fmt})..."
if [ "$(id -u)" -eq 0 ]; then
	$install_cmd "${tmp}/${file}"
else
	sudo $install_cmd "${tmp}/${file}"
fi

echo "Done. Run 'aimd --help' to get started."
