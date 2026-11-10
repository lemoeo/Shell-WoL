#!/usr/bin/env bash
# Maintainer/CI utility. Uses a private index; never checks out the assets tree.
set -euo pipefail

fail() { printf 'Error: %s\n' "$*" >&2; exit 1; }
usage() {
    printf '%s\n' 'Usage: bash scripts/publish-assets.sh snapshot VERSION DIRECTORY [REMOTE]' \
        '       bash scripts/publish-assets.sh latest VERSION [REMOTE]' \
        'Publishes assets-VERSION tags; latest advances only the managed assets branch.'
}
valid_version() { [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9]+([.-][A-Za-z0-9]+)*)?$ ]]; }
[[ $# -gt 0 ]] || { usage; exit 1; }
action=$1
case "$action" in
    --help|-h) usage; exit 0 ;;
    snapshot) [[ $# -ge 3 && $# -le 4 ]] || fail 'snapshot requires VERSION DIRECTORY [REMOTE]'; version=$2; directory=$3; remote=${4:-origin} ;;
    latest) [[ $# -ge 2 && $# -le 3 ]] || fail 'latest requires VERSION [REMOTE]'; version=$2; remote=${3:-origin} ;;
    *) fail 'action must be snapshot or latest' ;;
esac
valid_version "$version" || fail 'invalid release version'
[[ "$remote" != -* && -n "$remote" ]] || fail 'invalid remote'
git rev-parse --git-dir >/dev/null
tag=assets-$version
temporary=$(mktemp -d "${TMPDIR:-/tmp}/shell-wol-publish.XXXXXX")
trap 'rm -rf "$temporary"' EXIT
asset_index=$temporary/index

remote_ref() {
    git ls-remote --refs "$remote" "$1" | awk '{print $1}'
}
fetch_commit() {
    git fetch --quiet --no-tags "$remote" "$1" || return 1
    git rev-parse 'FETCH_HEAD^{commit}'
}
add_file() {
    local blob
    blob=$(git hash-object -w --no-filters -- "$directory/$1")
    GIT_INDEX_FILE="$asset_index" git update-index --add --cacheinfo "$2" "$blob" "$1"
}
if [[ "$action" == snapshot ]]; then
    [[ -d "$directory" ]] || fail 'asset directory is missing'
    directory=$(cd "$directory" && pwd -P)
    for file in VERSION SHA256SUMS; do
        [[ -f "$directory/$file" && ! -L "$directory/$file" ]] || fail "invalid $file"
    done
    [[ $(cat "$directory/VERSION") == "$version" ]] || fail 'VERSION does not match requested version'
    # Exact, reviewed artifact names prevent path traversal and accidental uploads.
    count=0; seen='|'; total=0
    GIT_INDEX_FILE="$asset_index" git read-tree --empty
    while read -r digest binary extra; do
        [[ -z "$extra" && "$digest" =~ ^[0-9a-f]{64}$ ]] || fail 'invalid SHA256SUMS row'
        case "$binary" in
            shell-wol-darwin-amd64|shell-wol-darwin-arm64|shell-wol-windows-amd64.exe|shell-wol-windows-arm64.exe|shell-wol-linux-amd64|shell-wol-linux-arm64|shell-wol-linux-arm-7|shell-wol-linux-mips-softfloat|shell-wol-linux-mipsle-softfloat) ;;
            *) fail "unexpected asset name: $binary" ;;
        esac
        [[ "$seen" != *"|$binary|"* ]] || fail "duplicate asset: $binary"
        seen+="$binary|"; count=$((count + 1))
        [[ -f "$directory/$binary" && ! -L "$directory/$binary" ]] || fail "missing or symlinked asset: $binary"
        if command -v sha256sum >/dev/null 2>&1; then actual=$(sha256sum <"$directory/$binary")
        else actual=$(shasum -a 256 <"$directory/$binary"); fi
        [[ "${actual%% *}" == "$digest" ]] || fail "SHA256 mismatch: $binary"
        size=$(wc -c <"$directory/$binary")
        (( size <= 20000000 )) || fail "asset exceeds jsDelivr's 20 MB limit: $binary"
        total=$((total + size))
        add_file "$binary" 100755
    done <"$directory/SHA256SUMS"
    [[ "$count" == 9 ]] || fail 'manifest must contain all nine release binaries'
    for file in VERSION SHA256SUMS; do
        size=$(wc -c <"$directory/$file"); total=$((total + size))
        (( size <= 20000000 )) || fail "file exceeds jsDelivr's 20 MB limit: $file"
        add_file "$file" 100644
    done
    (( total + 1024 <= 150000000 )) || fail 'snapshot exceeds jsDelivr package limit'
    marker=$(printf 'schema=1\nversion=%s\n' "$version" | git hash-object -w --stdin)
    GIT_INDEX_FILE="$asset_index" git update-index --add --cacheinfo 100644 "$marker" .shell-wol-assets
    tree=$(GIT_INDEX_FILE="$asset_index" git write-tree)
    existing=$(remote_ref "refs/tags/$tag")
    if [[ -n "$existing" ]]; then
        existing_commit=$(fetch_commit "refs/tags/$tag")
        [[ $(git rev-parse "$existing_commit^{tree}") == "$tree" ]] || fail "$tag already exists with different files; immutable tags cannot be replaced"
        printf '%s already contains the same verified files.\n' "$tag"
        exit 0
    fi
    commit=$(git -c user.name='github-actions[bot]' -c user.email='41898282+github-actions[bot]@users.noreply.github.com' \
        commit-tree "$tree" -m "Shell-WoL binary assets $version")
    # No force: a concurrently created tag must never be replaced.
    git push "$remote" "$commit:refs/tags/$tag"
    printf 'Published immutable snapshot %s.\n' "$tag"
else
    # GitHub latest also excludes prereleases.
    if [[ "$version" == *-* ]]; then printf 'Prerelease %s: latest is unchanged.\n' "$version"; exit 0; fi
    [[ -n $(remote_ref "refs/tags/$tag") ]] || fail "missing immutable snapshot $tag"
    target=$(fetch_commit "refs/tags/$tag")
    expected_marker=$(printf 'schema=1\nversion=%s' "$version")
    [[ $(git show "$target:.shell-wol-assets") == "$expected_marker" ]] || fail 'snapshot ownership marker is invalid'
    [[ $(git show "$target:VERSION") == "$version" ]] || fail 'snapshot VERSION is invalid'
    current=$(remote_ref refs/heads/assets)
    if [[ -n "$current" ]]; then
        current_commit=$(fetch_commit refs/heads/assets)
        [[ "$current" == "$current_commit" ]] || fail 'assets branch changed during inspection; retry'
        old_version=$(git show "$current:VERSION") || fail 'assets branch is not managed by Shell-WoL'
        valid_version "$old_version" && [[ "$old_version" != *-* ]] || fail 'invalid assets branch version'
        expected_marker=$(printf 'schema=1\nversion=%s' "$old_version")
        [[ $(git show "$current:.shell-wol-assets") == "$expected_marker" ]] || fail 'refusing to overwrite an unmanaged assets branch'
        if [[ "$old_version" == "$version" ]]; then
            [[ "$current" == "$target" ]] || fail 'assets branch differs from its immutable snapshot'
            printf 'Latest already points to %s.\n' "$version"; exit 0
        fi
        # Compare numeric version components; backfilling an old release must not
        # advertise it as latest. awk avoids shell octal rules for leading zeros.
        if ! awk -v old="$old_version" -v new="$version" 'BEGIN {
            sub(/^v/, "", old); sub(/^v/, "", new); split(old,a,/[.]/); split(new,b,/[.]/)
            for(i=1;i<=3;i++){if(b[i]+0>a[i]+0)exit 0;if(b[i]+0<a[i]+0)exit 1} exit 1
        }'; then printf 'Keeping newer latest %s; synchronized %s only.\n' "$old_version" "$version"; exit 0; fi
    fi
    # Snapshots are orphan trees. Update only this dedicated, owned branch with
    # an exact lease; source branches and historical tags are never rewritten.
    git push "--force-with-lease=refs/heads/assets:$current" "$remote" "$target:refs/heads/assets"
    printf 'Latest assets now point to %s.\n' "$version"
fi
