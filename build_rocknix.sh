#!/usr/bin/env bash
#
# Build and package Grout Save Sync for ROCKNIX (arm64).
#
# Does the same as `task build:arm64 && task package:rocknix` without needing
# Task. The binary is cross-compiled in Docker, so the host only needs Docker
# with buildx and arm64 emulation (qemu binfmt).
#
#   ./build_rocknix.sh                     build with the version from pak.json
#   ./build_rocknix.sh --version 5.3.1.4   override the version
#   ./build_rocknix.sh --no-cache          rebuild the Docker image from scratch
#
# Output: dist/ROCKNIX/Grout-Save-Sync.sh and dist/ROCKNIX/Grout-Save-Sync/.
# Copy both into the device's roms/ports directory.
set -euo pipefail

cd "$(dirname "$0")"

IMAGE_TAG=grout-build-arm64
CONTAINER_NAME=grout-extract-build64
LABEL=app.romm.grout.build=true
BUILD_DIR=build64
DIST_DIR=dist/ROCKNIX
PKG_NAME=Grout-Save-Sync
LIB_PATH=/usr/lib/aarch64-linux-gnu/libSDL2_gfx-1.0.so.0.0.2

version=""
no_cache=""

usage() {
	sed -n '3,14p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		version="${2:?--version needs a value}"
		shift 2
		;;
	--no-cache)
		no_cache="--no-cache"
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "Unknown argument: $1" >&2
		usage >&2
		exit 1
		;;
	esac
done

fail() {
	echo "error: $*" >&2
	exit 1
}

# Prerequisites
command -v docker >/dev/null || fail "docker is not installed"
docker info >/dev/null 2>&1 || fail "cannot talk to the Docker daemon (is it running, are you in the docker group?)"
docker buildx version >/dev/null 2>&1 || fail "docker buildx is missing (pacman -S docker-buildx)"
if [ "$(uname -m)" != "aarch64" ] && [ ! -e /proc/sys/fs/binfmt_misc/qemu-aarch64 ]; then
	fail "arm64 emulation is not registered (pacman -S qemu-user-static qemu-user-static-binfmt && sudo systemctl restart systemd-binfmt)"
fi

version_arg=()
if [ -n "$version" ]; then
	version_arg=(--build-arg "GROUT_VERSION=$version")
fi

cleanup() {
	docker rm "$CONTAINER_NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Prints one line when each BuildKit step starts (its first line, whitespace
# collapsed and truncated) and one when it finishes (DONE/CACHED/ERROR).
# BuildKit repeats both for some steps, so each is printed once. Everything
# else only goes to the log.
progress() {
	awk '
		!/^#[0-9]+ / { next }
		!($1 in seen) {
			seen[$1] = 1
			line = substr($0, length($1) + 2)
			gsub(/[ \t]+/, " ", line)
			if (length(line) > 100) line = substr(line, 1, 97) "..."
			print $1 " " line
			fflush()
			next
		}
		($2 == "DONE" || $2 == "CACHED" || $2 ~ /^ERROR/) && !finished[$1]++ { print; fflush() }
	'
}

# Build
rm -rf "$BUILD_DIR"
echo "Building linux/arm64 (full log: $BUILD_DIR.log)..."
if ! docker buildx build $no_cache --progress=plain --load --platform=linux/arm64 \
	--build-arg GITHUB_ACTIONS=false "${version_arg[@]}" \
	--label "$LABEL" -t "$IMAGE_TAG" -f docker/Dockerfile . 2>&1 |
	tee "$BUILD_DIR.log" | progress; then
	echo "--- last 40 lines of $BUILD_DIR.log ---" >&2
	tail -n 40 "$BUILD_DIR.log" >&2
	fail "Docker build failed"
fi

# Extract
echo "Extracting binary and libraries..."
mkdir -p "$BUILD_DIR/lib"
cleanup
docker create --name "$CONTAINER_NAME" --label "$LABEL" "$IMAGE_TAG" >/dev/null
docker cp "$CONTAINER_NAME:/build/grout" "$BUILD_DIR/grout"
docker cp "$CONTAINER_NAME:$LIB_PATH" "$BUILD_DIR/lib/libSDL2_gfx-1.0.so.0"
cleanup
docker image prune --filter "label=$LABEL" -f >/dev/null 2>&1 || true

# Package
echo "Packaging $DIST_DIR..."
rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR/$PKG_NAME/lib"
cp "scripts/ROCKNIX/$PKG_NAME.sh" "$DIST_DIR/"
cp "$BUILD_DIR/grout" scripts/ROCKNIX/logo.png README.md LICENSE "$DIST_DIR/$PKG_NAME/"
cp -R "$BUILD_DIR"/lib/* "$DIST_DIR/$PKG_NAME/lib/"
chmod a+x "$DIST_DIR/$PKG_NAME/grout" "$DIST_DIR/$PKG_NAME.sh"

echo "Done: $DIST_DIR/$PKG_NAME.sh and $DIST_DIR/$PKG_NAME/"
