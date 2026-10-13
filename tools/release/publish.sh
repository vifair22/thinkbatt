#!/usr/bin/env bash
# thinkbatt: publish a release from a tag pipeline.
#
# Uploads every file in DIR to the project's generic package registry as
# thinkbatt/<version>/<file>, then creates the GitLab release for the tag with
# a link to each. The version is the tag's, which the caller (the release job)
# has already checked against release_version. The source tarball the overlay
# ebuild fetches is one of these files; its SRC_URI points at the registry
# path this script uploads to, so the bytes are frozen here once and the
# ebuild Manifest pins their hash.
#
# Usage: tools/release/publish.sh DIR
# Env:   the pipeline's CI_API_V4_URL, CI_PROJECT_ID, CI_JOB_TOKEN,
#        CI_COMMIT_TAG
set -euo pipefail

DIR=${1:?usage: publish.sh DIR}
TAG=${CI_COMMIT_TAG:?not a tag pipeline}
VER=${TAG#v}
PKG="$CI_API_V4_URL/projects/$CI_PROJECT_ID/packages/generic/thinkbatt/$VER"

links=()
for f in "$DIR"/*; do
    name=$(basename "$f")
    echo "== uploading $name"
    curl -sS --fail-with-body -H "JOB-TOKEN: $CI_JOB_TOKEN" \
        --upload-file "$f" "$PKG/$name" >/dev/null
    links+=("$name")
done

# The release, with the checksums a person compares a download against in the
# notes.
python3 - "$TAG" "$PKG" "$DIR/SHA256SUMS" "${links[@]}" >release.json <<'PY'
import json, sys
tag, pkg, sums, names = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4:]
with open(sums) as f:
    checksums = f.read()
print(json.dumps({
    "tag_name": tag,
    "name": "thinkbatt " + tag[1:],
    "description": "Source release of thinkbatt, the ThinkPad battery "
                   "charge-policy and fuel-gauge calibration daemon. The "
                   "sys-power/thinkbatt ebuild in vifair-overlay builds from "
                   "the tarball below.\n\n```\n" + checksums + "```\n",
    "assets": {"links": [
        {"name": n, "url": pkg + "/" + n, "link_type": "package"}
        for n in names]},
}))
PY
echo "== creating the release $TAG"
curl -sS --fail-with-body -H "JOB-TOKEN: $CI_JOB_TOKEN" \
    -H "Content-Type: application/json" --data @release.json \
    "$CI_API_V4_URL/projects/$CI_PROJECT_ID/releases" >/dev/null
echo "released $TAG"
