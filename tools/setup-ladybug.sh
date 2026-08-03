#!/bin/sh
set -eu

# go-ladybug v0.17.0 was tagged on 2026-05-29 against LadybugDB's 2026-05-28
# v0.17.0 release. Its bundled workflow used these shared-library archives.
LADYBUG_VERSION=0.17.0
GO_LADYBUG_VERSION=v0.17.0

case "$(uname -s):$(uname -m)" in
	Darwin:arm64)
		archive=liblbug-osx-arm64.tar.gz
		checksum=f2acc15c9a90874aa25cebd49921080e44774525b061ec7189fae18d1878d018
		library=liblbug.dylib
		;;
	Darwin:x86_64)
		archive=liblbug-osx-x86_64.tar.gz
		checksum=8ab59121a1aadeaeeca1f855cfc4d3a596620a737461976e6e084f02c6cf5b0a
		library=liblbug.dylib
		;;
	Linux:aarch64|Linux:arm64)
		archive=liblbug-linux-aarch64.tar.gz
		checksum=a486000f7305cb601e3074eb06026e9eddcf3d7517df7bbc913d2d36e3108121
		library=liblbug.so
		;;
	Linux:x86_64)
		archive=liblbug-linux-x86_64.tar.gz
		checksum=f546b37616347533c5d08a38fd1c9f51bfe5e2c97c56d68582e07e675f546d4d
		library=liblbug.so
		;;
	*)
		echo "unsupported LadybugDB platform: $(uname -s)/$(uname -m)" >&2
		exit 1
		;;
esac

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cache_root=$root/.cache/ladybug
cache=$cache_root/go-ladybug-$GO_LADYBUG_VERSION-$(uname -s)-$(uname -m)
mkdir -p "$cache_root"

if [ ! -f "$cache/module/lib/lbug.h" ] || [ ! -f "$cache/module/lib/$library" ]; then
	staging=$(mktemp -d "$cache_root/.staging.XXXXXX")
	trap 'rm -rf "$staging"' EXIT INT TERM
	(cd "$root/platform/cartographer" && GOWORK=off go mod download github.com/LadybugDB/go-ladybug@$GO_LADYBUG_VERSION)
	module=$(cd "$root/platform/cartographer" && GOWORK=off go list -m -f '{{.Dir}}' github.com/LadybugDB/go-ladybug@$GO_LADYBUG_VERSION)
	mkdir "$staging/module" "$staging/shim"
	cp -R "$module/." "$staging/module"
	chmod -R u+w "$staging/module"
	rm -rf "$staging/module/lib"
	ln -s "$root/tools/ladybug-verified-curl" "$staging/shim/curl"
	LADYBUG_REAL_CURL=$(command -v curl) \
		LADYBUG_ARCHIVE=$archive \
		LADYBUG_ARCHIVE_SHA256=$checksum \
		LBUG_VERSION=$LADYBUG_VERSION \
		LBUG_LIB_KIND=shared \
		PATH="$staging/shim:$PATH" \
		sh "$staging/module/download_lbug.sh"
	test -f "$staging/module/lib/lbug.h"
	test -f "$staging/module/lib/$library"
	if ln -s "$(basename "$staging")" "$cache" 2>/dev/null; then
		trap - EXIT INT TERM
	else
		rm -rf "$staging"
	fi
fi

work=$cache_root/go.work
work_tmp=$(mktemp -d "$cache_root/.work.XXXXXX")
trap 'rm -rf "$work_tmp"' EXIT INT TERM
(cd "$work_tmp" && GOWORK=off go work init)
if [ -n "${LADYBUG_WORK_MODULES:-}" ]; then
	for module in $LADYBUG_WORK_MODULES; do
		GOWORK=$work_tmp/go.work go work edit -use="$root/$module"
	done
else
	GOWORK=$root/go.work go list -m -f '{{if .Main}}{{.Dir}}{{end}}' |
		while IFS= read -r module; do
			GOWORK=$work_tmp/go.work go work edit -use="$module"
		done
fi
GOWORK=$work_tmp/go.work go work edit -replace=github.com/LadybugDB/go-ladybug="$cache/module"
mv "$work_tmp/go.work" "$work"
rm -rf "$work_tmp"
trap - EXIT INT TERM
