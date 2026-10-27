#!/usr/bin/env bash
# Promote the CI-built Docker archives without rebuilding them. Requires docker
# (with buildx for publish), jq and the already-validated release VERSION.
# Metadata files contain exactly one value, with an optional final LF.
set -euo pipefail
export LC_ALL=C

fail() {
  printf 'release-images: %s\n' "$*" >&2
  exit 1
}

is_digest() {
  [[ $1 =~ ^sha256:[0-9a-f]{64}$ ]]
}

same_metadata() {
  cmp -s "$1" <(printf '%s\n' "$2") || cmp -s "$1" <(printf '%s' "$2")
}

verify_artifact() {
  local directory=$1 arch=$2 name image_id local_tag loaded line found=0
  case "$arch" in amd64|arm64) ;; *) fail 'ARCH must be amd64 or arm64' ;; esac
  [[ -d "$directory" && ! -L "$directory" ]] || fail 'artifact directory is missing or a symlink'
  for name in image.tar image.id commit.sha version; do
    [[ -f "$directory/$name" && ! -L "$directory/$name" && -s "$directory/$name" ]] || fail "missing or invalid artifact file: $name"
  done
  same_metadata "$directory/commit.sha" "$GITHUB_SHA" || fail 'artifact commit.sha does not match GITHUB_SHA'
  same_metadata "$directory/version" "$VERSION" || fail 'artifact version does not match VERSION'
  image_id=$(< "$directory/image.id")
  is_digest "$image_id" && same_metadata "$directory/image.id" "$image_id" || fail 'artifact image.id must be one sha256 image ID'
  local_tag="composite-ci:${GITHUB_SHA}-${arch}"
  loaded=$(docker image load --input "$directory/image.tar") || fail 'loading image archive failed'
  # Do not accidentally verify a stale local tag if this archive loaded something
  # else. Docker load restores tags and reports each as "Loaded image: TAG".
  while IFS= read -r line; do
    if [[ "$line" == "Loaded image: $local_tag" ]]; then found=1; fi
  done <<< "$loaded"
  [[ $found == 1 ]] || fail 'archive did not load the expected local image tag'
  docker image inspect "$local_tag" | jq -es \
    --arg id "$image_id" --arg arch "$arch" --arg revision "$GITHUB_SHA" \
    --arg source "https://github.com/$GITHUB_REPOSITORY" --arg version "$VERSION" '
      length == 1 and (.[0] | type == "array" and length == 1 and
        (.[0] | .Id == $id and .Os == "linux" and .Architecture == $arch and
          .Config.Labels["org.opencontainers.image.revision"] == $revision and
          .Config.Labels["org.opencontainers.image.source"] == $source and
          .Config.Labels["org.opencontainers.image.version"] == $version))
    ' >/dev/null || fail 'loaded image identity, platform or OCI labels do not match'
  verified_id=$image_id
}

publish() {
  local root=$1 expected_image alias tag has_version=0 arch image_id remote_tag details repo_digest digest
  local index_digest='' current_digest manifest
  local aliases=() tag_args=() image_ids=() sources=() digests=()
  : "${IMAGE:?IMAGE is required}" "${IMAGE_TAGS:?IMAGE_TAGS is required}" "${GITHUB_OUTPUT:?GITHUB_OUTPUT is required}"
  expected_image="ghcr.io/$(printf '%s' "$GITHUB_REPOSITORY" | tr '[:upper:]' '[:lower:]')"
  [[ "$IMAGE" == "$expected_image" ]] || fail 'IMAGE must be the untagged ghcr.io repository for GITHUB_REPOSITORY'
  [[ -f "$GITHUB_OUTPUT" && ! -L "$GITHUB_OUTPUT" && -w "$GITHUB_OUTPUT" ]] || fail 'GITHUB_OUTPUT must be a writable regular file'
  while IFS= read -r alias || [[ -n "$alias" ]]; do
    [[ -n "$alias" ]] || continue
    [[ "$alias" == "$IMAGE:"* ]] || fail 'image alias is outside IMAGE repository'
    tag=${alias#"$IMAGE:"}
    [[ "$tag" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$ ]] || fail 'image alias must have one valid Docker tag'
    case "$tag" in
      "sha-${GITHUB_SHA}-amd64"|"sha-${GITHUB_SHA}-arm64") fail 'image alias collides with a platform tag' ;;
    esac
    if [[ "$tag" == "$VERSION" ]]; then has_version=1; fi
    aliases+=("$alias")
    tag_args+=(--tag "$alias")
  done <<< "$IMAGE_TAGS"
  [[ $has_version == 1 ]] || fail 'IMAGE_TAGS must include the validated VERSION tag'

  # This entire validation phase must finish before the first registry write.
  for arch in amd64 arm64; do
    verify_artifact "$root/operator-${GITHUB_SHA}-${arch}" "$arch"
    image_ids+=("$verified_id")
  done
  for arch in amd64 arm64; do
    if [[ "$arch" == amd64 ]]; then image_id=${image_ids[0]}; else image_id=${image_ids[1]}; fi
    remote_tag="$IMAGE:sha-${GITHUB_SHA}-${arch}"
    docker image tag "$image_id" "$remote_tag"
    docker image push "$remote_tag"
    details=$(docker image inspect "$remote_tag")
    # Match the complete repository, never a substring or another repository's
    # RepoDigest. Ambiguous digests must not become index sources.
    repo_digest=$(printf '%s\n' "$details" | jq -ers --arg image "$IMAGE" --arg id "$image_id" '
      select(length == 1) | .[0] |
      select(type == "array" and length == 1 and .[0].Id == $id) |
      .[0].RepoDigests | map(select(split("@")[0] == $image)) |
      select(length == 1) | .[0] |
      select((split("@") | length == 2) and
        (split("@")[1] | length == 71 and test("^sha256:[0-9a-f]{64}$")))
    ') || fail 'pushed image has no unique sha256 digest for the exact IMAGE repository'
    digest=${repo_digest#"$IMAGE@"}
    is_digest "$digest" || fail 'pushed image repository digest is not sha256'
    sources+=("$IMAGE@$digest")
    digests+=("$digest")
  done

  # Buildx accepts repeatable --tag options and immutable source references:
  # https://docs.docker.com/reference/cli/docker/buildx/imagetools/create/
  docker buildx imagetools create "${tag_args[@]}" "${sources[@]}"
  for alias in "${aliases[@]}"; do
    # .Manifest's JSON includes the resolved digest and platform descriptors;
    # --raw does not include the index's own digest.
    # https://docs.docker.com/reference/cli/docker/buildx/imagetools/inspect/
    manifest=$(docker buildx imagetools inspect --format '{{json .Manifest}}' "$alias")
    current_digest=$(printf '%s\n' "$manifest" | jq -ers --arg amd64 "${digests[0]}" --arg arm64 "${digests[1]}" '
      select(length == 1) | .[0] |
      select(.schemaVersion == 2 and
        (.mediaType == "application/vnd.oci.image.index.v1+json" or
         .mediaType == "application/vnd.docker.distribution.manifest.list.v2+json")) |
      select(.manifests | type == "array" and length == 2) |
      select(all(.manifests[];
        .mediaType == "application/vnd.oci.image.manifest.v1+json" or
        .mediaType == "application/vnd.docker.distribution.manifest.v2+json")) |
      select((.manifests | map({digest, os: .platform.os, arch: .platform.architecture}) | sort_by(.arch)) ==
        [{digest: $amd64, os: "linux", arch: "amd64"}, {digest: $arm64, os: "linux", arch: "arm64"}]) |
      .digest | select(type == "string" and length == 71 and test("^sha256:[0-9a-f]{64}$"))
    ') || fail 'published index digest or platform descriptors do not match the promoted images'
    if [[ -n "$index_digest" && "$current_digest" != "$index_digest" ]]; then
      fail 'published aliases do not resolve to the same index digest'
    fi
    index_digest=$current_digest
  done
  # Leave outputs untouched on every failure, including alias/index verification.
  printf 'digest=%s\n' "$index_digest" >> "$GITHUB_OUTPUT"
}

case "${1:-}" in
  verify) [[ $# == 3 ]] || fail 'usage: verify ARTIFACT_DIR ARCH' ;;
  publish) [[ $# == 2 ]] || fail 'usage: publish ARTIFACT_ROOT' ;;
  *) fail 'usage: release-images.sh {verify ARTIFACT_DIR ARCH|publish ARTIFACT_ROOT}' ;;
esac
: "${VERSION:?VERSION is required}" "${GITHUB_SHA:?GITHUB_SHA is required}" "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}"
[[ "$GITHUB_SHA" =~ ^[0-9a-f]{40}$ ]] || fail 'GITHUB_SHA must be a full lowercase commit SHA'
[[ "$GITHUB_REPOSITORY" =~ ^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$ ]] || fail 'GITHUB_REPOSITORY must be owner/repository'
[[ "$VERSION" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$ ]] || fail 'VERSION must be a valid Docker tag'
command -v docker >/dev/null || fail 'docker is required'
command -v jq >/dev/null || fail 'jq is required'
case "$1" in
  verify) verify_artifact "$2" "$3" ;;
  publish) publish "$2" ;;
esac
