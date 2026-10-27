#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
sh hack/verify-version.sh
scratch=$(mktemp -d "${TMPDIR:-/tmp}/composite-helm.XXXXXX")
trap 'rm -rf -- "${scratch:?}"' EXIT HUP INT TERM
"${GO:-go}" run ./hack/package-helm helm "$scratch/helm" "$IMAGE"
mkdir -p out
"${HELM:-helm}" package "$scratch/helm" --destination out --version "${VERSION#v}" --app-version "$VERSION"
