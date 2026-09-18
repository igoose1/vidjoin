#!/usr/bin/env bash
# Downloads static ffmpeg + ffprobe builds (BtbN/FFmpeg-Builds, GPL variant,
# with NVENC, QSV, AMF and VA-API support) into DEST.
#
#   scripts/fetch-ffmpeg.sh linux|windows DEST
#
# FFMPEG_BUILD selects the build, e.g. ffmpeg-master-latest (default) or a
# release line such as ffmpeg-n8.0-latest (see the BtbN releases page).
set -euo pipefail

platform=${1:?usage: fetch-ffmpeg.sh linux|windows DEST}
dest=${2:?usage: fetch-ffmpeg.sh linux|windows DEST}
build=${FFMPEG_BUILD:-ffmpeg-master-latest}
base=${FFMPEG_BASE_URL:-https://github.com/BtbN/FFmpeg-Builds/releases/download/latest}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$dest"

case "$platform" in
  linux)
    name="$build-linux64-gpl"
    echo "Downloading $name.tar.xz"
    curl -fL --retry 3 --retry-delay 5 -o "$tmp/ff.tar.xz" "$base/$name.tar.xz"
    tar -xJf "$tmp/ff.tar.xz" -C "$tmp"
    cp "$tmp/$name/bin/ffmpeg" "$tmp/$name/bin/ffprobe" "$dest/"
    chmod +x "$dest/ffmpeg" "$dest/ffprobe"
    ;;
  windows)
    name="$build-win64-gpl"
    echo "Downloading $name.zip"
    curl -fL --retry 3 --retry-delay 5 -o "$tmp/ff.zip" "$base/$name.zip"
    if command -v unzip >/dev/null; then
      unzip -q "$tmp/ff.zip" -d "$tmp"
    else
      7z x -y -o"$tmp" "$tmp/ff.zip" >/dev/null
    fi
    cp "$tmp/$name/bin/ffmpeg.exe" "$tmp/$name/bin/ffprobe.exe" "$dest/"
    ;;
  *)
    echo "unknown platform: $platform (use linux or windows)" >&2
    exit 2
    ;;
esac

cp "$tmp/$name/LICENSE.txt" "$dest/LICENSE-ffmpeg.txt"
if [ "$platform" = linux ] && [ "$(uname -s)" = Linux ]; then
  "$dest/ffmpeg" -hide_banner -version | head -1
fi
