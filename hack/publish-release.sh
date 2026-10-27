#!/usr/bin/env bash
# Finalize a fully uploaded draft only after repository protections are enabled.
# VERSION/PRERELEASE come from the strict release-tag gate; never create a tag or
# release here. check-draft binds/rechecks the reviewed ID before any uploads.
# Final publication requires that bound RELEASE_ID. An immutable retry is read-only.
set +x
set -euo pipefail
export LC_ALL=C

fail() {
  printf 'publish-release: %s\n' "$1" >&2
  exit 1
}

# Keep raw gh/jq diagnostics private, including GH_DEBUG HTTP payloads. Validate
# the raw stream before command substitution can strip newlines or NUL bytes.
api() {
  gh api --hostname "$github_host" --header 'Accept: application/vnd.github+json' \
    --header 'X-GitHub-Api-Version: 2026-03-10' "$@" 2>/dev/null |
    jq -ecs 'select(length == 1 and (.[0] | type == "object")) | .[0]' 2>/dev/null
}

# Drafts must be discovered through an authenticated, paginated release listing;
# the by-tag endpoint is documented for published releases only.
release_list() {
  gh api --hostname "$github_host" --header 'Accept: application/vnd.github+json' \
    --header 'X-GitHub-Api-Version: 2026-03-10' --method GET \
    "$repository_api/releases?per_page=100" --paginate --slurp 2>/dev/null |
    jq -ecs 'select(length == 1) | .[0] |
      select(type == "array" and length > 0 and
        all(.[]; type == "array" and all(.[]; type == "object"))) | add' 2>/dev/null
}

verify_tag() {
  local commit
  commit=$(api --method GET "$repository_api/commits/$VERSION") || fail 'tag lookup failed or returned invalid JSON'
  printf '%s\n' "$commit" | jq -e --arg sha "$GITHUB_SHA" '.sha == $sha' >/dev/null 2>&1 ||
    fail 'tag does not resolve to GITHUB_SHA'
}

validate_identity() {
  printf '%s\n' "$1" | jq -e --arg version "$VERSION" --arg id "$release_id" --argjson prerelease "$PRERELEASE" '
    def positive_integer: type == "number" and . > 0 and . <= 9007199254740991 and floor == .;
    .tag_name == $version and .name == $version and .prerelease == $prerelease and
    (.id | positive_integer and tostring == $id) and
    (.body | type == "string" and test("\\S")) and
    (.draft | type == "boolean") and (.immutable | type == "boolean") and
    (.assets | type == "array")
  ' >/dev/null 2>&1
}

validate_release() {
  validate_identity "$1" || return 1
  printf '%s\n' "$1" | jq -e --argjson expected "$expected_assets" '
    def positive_integer: type == "number" and . > 0 and . <= 9007199254740991 and floor == .;
    (.draft or (.published_at | type == "string" and length > 0)) and
    (.assets | length == 8 and (map(.name) | sort) == $expected and
      (map(.id) | unique | length == 8) and
      all(.[]; type == "object" and (.id | positive_integer) and
        .state == "uploaded" and (.size | positive_integer)))
  ' >/dev/null 2>&1
}

validate_draft() {
  validate_identity "$1" || return 1
  printf '%s\n' "$1" | jq -e --argjson expected "$expected_assets" '
    def positive_integer: type == "number" and . > 0 and . <= 9007199254740991 and floor == .;
    .draft == true and .immutable == false and
    (.assets | (map(.name) | unique | length) == length and
      (map(.id) | unique | length) == length and
      all(.[]; (.id | positive_integer) and
        (.name as $name | ($expected | index($name)) != null)))
  ' >/dev/null 2>&1
}

# Ignore mutable download counters/order, but pin the uploaded asset identities,
# sizes and available digests as well as the release's notes and identity.
release_evidence() {
  printf '%s\n' "$1" | jq -c '
    {id, name, tag_name, prerelease, body,
      assets: (.assets | map({id, name, state, size, digest}) | sort_by(.name))}
  ' 2>/dev/null
}

valid_output() {
  [[ -f "$GITHUB_OUTPUT" && ! -L "$GITHUB_OUTPUT" && -w "$GITHUB_OUTPUT" ]]
}

mode=publish
if [[ $# == 1 && "$1" == check-draft ]]; then
  mode=check-draft
elif [[ $# != 0 ]]; then
  fail 'usage: bash hack/publish-release.sh [check-draft] (environment inputs only)'
fi
if [[ -n "${RELEASE_ID:-}" ]]; then
  [[ "$RELEASE_ID" =~ ^[1-9][0-9]*$ && ${#RELEASE_ID} -le 16 ]] || fail 'RELEASE_ID must be a positive safe integer'
  if [[ ${#RELEASE_ID} == 16 && "$RELEASE_ID" > 9007199254740991 ]]; then
    fail 'RELEASE_ID must be a positive safe integer'
  fi
elif [[ "$mode" == publish ]]; then
  fail 'RELEASE_ID from the protected draft gate is required for publication'
fi
[[ -n "${VERSION:-}" && -n "${PRERELEASE:-}" && -n "${GITHUB_SHA:-}" && -n "${GITHUB_REPOSITORY:-}" ]] ||
  fail 'VERSION, PRERELEASE, GITHUB_SHA and GITHUB_REPOSITORY are required'
[[ -n "${GH_TOKEN:-}" && ! "$GH_TOKEN" =~ [[:space:][:cntrl:]] ]] || fail 'GH_TOKEN must be a nonempty token'
[[ -n "${GITHUB_OUTPUT:-}" ]] || fail 'GITHUB_OUTPUT is required'
valid_output || fail 'GITHUB_OUTPUT must be a writable regular file, not a symlink'
[[ "$GITHUB_SHA" =~ ^[0-9a-f]{40}$ ]] || fail 'GITHUB_SHA must be a full lowercase commit SHA'
[[ "$GITHUB_REPOSITORY" =~ ^[a-zA-Z0-9][a-zA-Z0-9-]*/[a-zA-Z0-9_.-]+$ ]] ||
  fail 'GITHUB_REPOSITORY must be owner/repository'
case "$GITHUB_REPOSITORY" in */.|*/..) fail 'GITHUB_REPOSITORY must be owner/repository' ;; esac

# The upstream gate owns supported release series; repeat the lexical checks so
# standalone misuse cannot change an endpoint, inject an output or mark an RC latest.
version_pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
[[ "$VERSION" =~ $version_pattern && ${#VERSION} -le 128 ]] || fail 'VERSION must be a validated release tag'
expected_prerelease=false
case "$VERSION" in
  *-*)
    expected_prerelease=true
    IFS=. read -r -a identifiers <<< "${VERSION#*-}"
    for identifier in "${identifiers[@]}"; do
      if [[ "$identifier" =~ ^[0-9]+$ && ${#identifier} -gt 1 && ${identifier:0:1} == 0 ]]; then
        fail 'VERSION prerelease numeric identifiers must not have leading zeros'
      fi
    done
    ;;
esac
[[ "$PRERELEASE" == "$expected_prerelease" ]] || fail 'PRERELEASE must be true/false and match VERSION'
github_server=${GITHUB_SERVER_URL-https://github.com}
[[ "$github_server" =~ ^https://[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?(:[0-9]{1,5})?$ ]] ||
  fail 'GITHUB_SERVER_URL must be an HTTPS origin without a path'
github_host=${github_server#https://}
repository_api="repos/$GITHUB_REPOSITORY"
release_url="$github_server/$GITHUB_REPOSITORY/releases/tag/$VERSION"
command -v gh >/dev/null || fail 'gh is required'
command -v jq >/dev/null || fail 'jq is required'
export GH_PROMPT_DISABLED=1 GH_ENTERPRISE_TOKEN="$GH_TOKEN"
unset GH_DEBUG
expected_assets=$(jq -cn --arg version "${VERSION#v}" '
  "cluster-api-provider-composite" as $project |
  ["linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64"] |
  map($project + "_" + $version + "_" + . + ".tar.gz") +
  [$project + "-" + $version + ".tgz", "infrastructure-components.yaml", "metadata.yaml", "checksums.txt"] | sort
' 2>/dev/null) || fail 'constructing expected asset names failed'

verify_tag
releases=$(release_list) || fail 'release listing failed or returned invalid paginated JSON'
candidate=$(printf '%s\n' "$releases" | jq -ce --arg version "$VERSION" '
  map(select(.tag_name == $version or .name == $version)) | select(length == 1) | .[0] |
  select(.tag_name == $version and .name == $version and
    (.id | type == "number" and . > 0 and . <= 9007199254740991 and floor == .))
' 2>/dev/null) || fail 'a unique release with the exact version title and tag is required'
release_id=$(printf '%s\n' "$candidate" | jq -r '.id' 2>/dev/null) || fail 'reading release identity failed'
if [[ -n "${RELEASE_ID:-}" && "$RELEASE_ID" != "$release_id" ]]; then
  fail 'release identity does not match the protected draft gate'
fi
release=$(api --method GET "$repository_api/releases/$release_id") || fail 'release lookup failed or returned invalid JSON'
if [[ "$mode" == check-draft ]]; then
  validate_draft "$release" || fail 'the bound reviewed draft is missing, published or has invalid evidence'
  verify_tag
  valid_output || fail 'GITHUB_OUTPUT must remain a writable regular file, not a symlink'
  printf 'release_id=%s\n' "$release_id" 2>/dev/null >> "$GITHUB_OUTPUT" || fail 'writing verified draft identity failed'
  exit 0
fi
validate_release "$release" || fail 'release identity, notes, flags or uploaded assets are invalid'
evidence=$(release_evidence "$release") || fail 'reading release evidence failed'
if printf '%s\n' "$release" | jq -e '.draft == true and .immutable == false' >/dev/null 2>&1; then
  make_latest=true
  if [[ "$PRERELEASE" == true ]]; then make_latest=false; fi
  # Only these two fields change; never replace notes, names or other metadata.
  # https://docs.github.com/en/rest/releases/releases#update-a-release
  updated=$(printf '{"draft":false,"make_latest":"%s"}\n' "$make_latest" |
    api --method PATCH "$repository_api/releases/$release_id" --input -) ||
    fail 'release publication failed or returned invalid JSON'
  validate_release "$updated" || fail 'publication response contains invalid release evidence'
  updated_evidence=$(release_evidence "$updated") || fail 'reading publication response failed'
  [[ "$updated_evidence" == "$evidence" ]] || fail 'publication response changed release evidence'
  printf '%s\n' "$updated" | jq -e '.draft == false' >/dev/null 2>&1 || fail 'publication response is still a draft'
else
  printf '%s\n' "$release" | jq -e --arg url "$release_url" \
    '.draft == false and .immutable == true and .html_url == $url' >/dev/null 2>&1 ||
    fail 'only a mutable draft or verified published immutable release is allowed'
fi

published=$(api --method GET "$repository_api/releases/$release_id") || fail 'published release lookup failed or returned invalid JSON'
validate_release "$published" || fail 'published release identity, notes, flags or uploaded assets are invalid'
published_evidence=$(release_evidence "$published") || fail 'reading published release evidence failed'
[[ "$published_evidence" == "$evidence" ]] || fail 'published release evidence changed'
printf '%s\n' "$published" | jq -e --arg url "$release_url" \
  '.draft == false and .immutable == true and .html_url == $url' >/dev/null 2>&1 ||
  fail 'published release must be immutable and have the owned release URL'
verify_tag
valid_output || fail 'GITHUB_OUTPUT must remain a writable regular file, not a symlink'
# Nothing is appended until all publication and immutable-tag evidence is verified.
printf 'url=%s\n' "$release_url" 2>/dev/null >> "$GITHUB_OUTPUT" || fail 'writing verified release output failed'
