#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
. "$root/hack/tools.env"
cd "$root/api"
"${GO:-go}" run "sigs.k8s.io/controller-tools/cmd/controller-gen@$CONTROLLER_GEN_VERSION" \
  crd paths=./... output:crd:artifacts:config=config/crd/bases
"${GO:-go}" run "sigs.k8s.io/controller-tools/cmd/controller-gen@$CONTROLLER_GEN_VERSION" \
  object:headerFile=../hack/boilerplate.go.txt paths=./...
