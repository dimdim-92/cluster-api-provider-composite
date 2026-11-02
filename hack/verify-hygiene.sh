#!/usr/bin/env bash
set -euo pipefail

# Exclusions protect local development state, not committed secrets. A committed
# excluded path is a source-delivery error, even if the scanner would skip it.
if [ -e .git ]; then
  git ls-files -z -- '.tools/**' '.idea/**' '.vscode/**' '.claude/**' '.opencode/**' \
    'bin/**' 'out/**' 'helm/charts/**' '.env' '.env.*' '**/.env' '**/.env.*' 'mgmt.yaml' \
    'kubeconfig' '**/kubeconfig' '*.kubeconfig' '.kube/**' '**/.kube/**' \
    '*.key' '*.pem' '*.p12' '*.pfx' '*.tfstate' '*.tfstate.*' '*.tfvars' '*.tfvars.json' \
    'tests/integration/test-results/**' 'capi-composite-controller' |
    while IFS= read -r -d '' path; do
      # A deletion in the working tree is already absent from the deliverable;
      # verification must not require staging it merely to inspect the source.
      if [ -e "$path" ] || [ -L "$path" ]; then
        printf 'Private or development-only path is tracked and must not be delivered: %s\n' "$path" >&2
        exit 1
      fi
    done
fi

: "${ACTIONLINT:?Set ACTIONLINT to the pinned actionlint binary}"
: "${GITLEAKS:?Set GITLEAKS to the pinned gitleaks binary}"
"$ACTIONLINT" -color
"$GITLEAKS" dir . --config .gitleaks.toml --redact --no-banner --no-color \
  --ignore-gitleaks-allow --max-decode-depth 2
