#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
exec "${GO:-go}" run ./hack/crd-labels -check ./api/config/crd/bases ./helm/crds
