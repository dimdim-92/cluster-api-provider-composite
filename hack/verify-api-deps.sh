#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root/api"
if grep -Eq '^[[:space:]]*tool([[:space:]]|[(])' go.mod; then
  printf 'The API module must not declare generator tools\n' >&2
  exit 1
fi
modules=$(GOWORK=off "${GO:-go}" list -mod=readonly -m all)
if printf '%s\n' "$modules" | grep -Eq '^(sigs[.]k8s[.]io/(cluster-api|controller-runtime|controller-tools|kube-api-linter)|github[.]com/golangci/(golangci-lint|plugin-module-register))([ /]|$)'; then
  printf 'The API consumer graph contains runtime, generator or linter tooling\n' >&2
  exit 1
fi
