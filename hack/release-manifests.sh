#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
sh "$root/hack/verify-version.sh"
: "${IMAGE:?IMAGE is required}"
: "${KUSTOMIZE:?KUSTOMIZE command is required}"
case "$IMAGE" in
  *[!a-zA-Z0-9./:@_-]*) printf 'Invalid image reference characters\n' >&2; exit 1 ;;
esac
directory="$root/out/repository/infrastructure-composite/$VERSION"
mkdir -p "$directory"
rendered=$(mktemp "${TMPDIR:-/tmp}/composite-components.XXXXXX")
trap 'rm -f "$rendered"' EXIT HUP INT TERM
# KUSTOMIZE is a trusted tool command (binary or pinned go run), not user input.
$KUSTOMIZE build "$root/config/default" > "$rendered"
test -s "$rendered"
grep -Fq 'ghcr.io/numspot/cluster-api-provider-composite:dev' "$rendered"
sed "s|ghcr[.]io/numspot/cluster-api-provider-composite:dev|$IMAGE|g" "$rendered" > "$directory/infrastructure-components.yaml"
cp "$root/metadata.yaml" "$directory/metadata.yaml"
printf 'providers:\n  - name: composite\n    type: InfrastructureProvider\n    url: "%s/infrastructure-components.yaml"\n' "$directory" > "$root/out/clusterctl.yaml"
