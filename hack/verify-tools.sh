#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
. "$root/hack/tools.env"

go_version=$("${GO:-go}" env GOVERSION)
if [ "$go_version" != "go$GO_VERSION" ]; then
  printf 'Go version mismatch: got %s; expected go%s (hack/tools.env)\n' "$go_version" "$GO_VERSION" >&2
  exit 1
fi
lint_version=$("${GOLANGCI_LINT:-golangci-lint}" version)
case "$lint_version" in
  *" version $GOLANGCI_LINT_VERSION "*) ;;
  *) printf 'golangci-lint version mismatch; expected %s (hack/tools.env)\n' "$GOLANGCI_LINT_VERSION" >&2; exit 1 ;;
esac
helm_version=$("${HELM:-helm}" version --template '{{.Version}}')
if [ "${helm_version%%+*}" != "$HELM_VERSION" ]; then
  printf 'Helm version mismatch: got %s; expected %s (hack/tools.env)\n' "$helm_version" "$HELM_VERSION" >&2
  exit 1
fi
