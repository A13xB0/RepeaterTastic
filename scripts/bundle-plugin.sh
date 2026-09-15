#!/usr/bin/env bash
# Build a plugin bundle: bundle-plugin.sh <plugin dir> <go package> <output.zip>
# Cross-compiles bin/<id>-linux-{arm64,arm,amd64} and zips them with plugin.yaml, the logo and
# the panel folder. {os}/{arch} in run.managed.exec pick the right binary at run time.
set -euo pipefail
src=$1 pkg=$2 out=$3
id=$(sed -n 's/^id: *//p' "$src/plugin.yaml" | head -1 | tr -d "\"' " | sed 's/#.*//')
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"
for t in linux/arm64 linux/arm linux/amd64; do
  os=${t%/*} arch=${t#*/}
  echo "building $id-$os-$arch"
  GOOS=$os GOARCH=$arch GOARM=6 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$work/bin/$id-$os-$arch" "$pkg"
done
# Everything the plugin ships except its Go sources, module files, tests and dotfiles.
( cd "$src" && find . -type f ! -name '*.go' ! -name 'go.mod' ! -name 'go.sum' ! -path '*/.*' ! -path './testdata/*' -print0 | cpio -0pdm --quiet "$work" )
mkdir -p "$(dirname "$out")"
out=$(realpath -m "$out")
rm -f "$out"
( cd "$work" && zip -qr -X "$out" . )
echo "wrote $out"
