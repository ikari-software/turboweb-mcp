#!/usr/bin/env bash
#
# Upload a built package to the Chrome Web Store and submit it for review.
#
# This is the V2 API (the V1 endpoints were deprecated in October 2025 and are
# scheduled for shutdown on 2026-10-15). The two calls that matter:
#
#   POST /upload/v2/publishers/{publisherId}/items/{itemId}:upload   (zip body)
#   POST /v2/publishers/{publisherId}/items/{itemId}:publish         (JSON body)
#
# Note the verb change from V1: upload is a POST now, not a PUT, and the
# `/upload/` prefix is mandatory for the package body — the metadata URI
# without it takes no zip.
#
# Requires, as environment variables:
#   CWS_PUBLISHER_ID    publisher id (Developer Dashboard URL: .../publishers/<id>/)
#   CWS_ITEM_ID         the 32-character extension id
#   CWS_ACCESS_TOKEN    OAuth2 access token for
#                       https://www.googleapis.com/auth/chromewebstore
#
# Optional:
#   CWS_PUBLISH_TYPE    DEFAULT_PUBLISH (default) | STAGED_PUBLISH
#   CWS_SKIP_REVIEW     true to request skip-review (only valid for items that
#                       qualify; the API returns a validation error otherwise)
#   CWS_POLL_INTERVAL   seconds between status polls (default 10)
#   CWS_POLL_TIMEOUT    seconds before giving up on a poll loop (default 900)
#   CWS_DRY_RUN         true to check versions and stop before uploading
#
# This script is deliberately strict: missing credentials are an error here.
# The workflow that calls it is the layer that skips gracefully when the
# repository secrets are absent, so a local run with no credentials fails
# loudly instead of silently doing nothing.
#
# usage: scripts/publish-chrome-store.sh <package.zip>

set -euo pipefail

API='https://chromewebstore.googleapis.com'
POLL_INTERVAL="${CWS_POLL_INTERVAL:-10}"
POLL_TIMEOUT="${CWS_POLL_TIMEOUT:-900}"

say() { printf 'chrome-store: %s\n' "$*"; }
die() { printf 'chrome-store: error: %s\n' "$*" >&2; exit 1; }

# --- arguments and preconditions ------------------------------------------

ZIP="${1:-}"
[ -n "$ZIP" ] || die "usage: $0 <package.zip>"
[ -f "$ZIP" ] || die "no such file: $ZIP"

for var in CWS_PUBLISHER_ID CWS_ITEM_ID CWS_ACCESS_TOKEN; do
    [ -n "${!var:-}" ] || die "$var is not set"
done

command -v jq >/dev/null || die "jq is required"
command -v unzip >/dev/null || die "unzip is required"
command -v curl >/dev/null || die "curl is required"

ITEM_PATH="publishers/${CWS_PUBLISHER_ID}/items/${CWS_ITEM_ID}"

# The store rejects a package whose manifest is not at the archive root, and
# the `make extension-zip` layout (everything under chrome/) is exactly that
# mistake. Catch it here rather than in a rejected review.
unzip -l "$ZIP" | awk 'NF >= 4 { print $4 }' | grep -qx 'manifest.json' \
    || die "manifest.json is not at the zip root — build with 'make chrome-store', not 'make extension-zip'"

WANT_VERSION="$(unzip -p "$ZIP" manifest.json | jq -r '.version // empty')"
[ -n "$WANT_VERSION" ] || die "could not read a version from the packaged manifest.json"
say "package version: $WANT_VERSION"

# --- read current state ---------------------------------------------------

api_get() {
    curl -sS -f -H "Authorization: Bearer ${CWS_ACCESS_TOKEN}" \
        "${API}/v2/${ITEM_PATH}:fetchStatus"
}

STATUS="$(api_get)" || die "fetchStatus failed — check the publisher id, item id and token scope"
taken_down="$(jq -r '.takenDown // false' <<<"$STATUS")"
if [ "$taken_down" = "true" ]; then
    die "this item is taken down for a policy violation; resolve it in the dashboard first"
fi

PUBLISHED_VERSION="$(jq -r '[.publishedItemRevisionStatus.distributionChannels[]?.crxVersion // empty] | first // ""' <<<"$STATUS")"

if [ -n "$PUBLISHED_VERSION" ]; then
    say "published version: $PUBLISHED_VERSION"
    if [ "$PUBLISHED_VERSION" = "$WANT_VERSION" ]; then
        die "packaged version $WANT_VERSION is already published — bump VERSION before releasing"
    fi
    if [ "$(printf '%s\n%s\n' "$WANT_VERSION" "$PUBLISHED_VERSION" | sort -V | tail -1)" = "$PUBLISHED_VERSION" ]; then
        die "packaged version $WANT_VERSION is not greater than the published version $PUBLISHED_VERSION"
    fi
else
    say "no published version reported — treating this as the first API upload"
fi

if [ "${CWS_DRY_RUN:-false}" = "true" ]; then
    say "dry run: stopping before upload"
    exit 0
fi

# --- upload ---------------------------------------------------------------

say "uploading $(basename "$ZIP") ($(wc -c <"$ZIP" | tr -d ' ') bytes)"
UPLOAD="$(curl -sS -X POST -T "$ZIP" \
    -H "Authorization: Bearer ${CWS_ACCESS_TOKEN}" \
    -H "Content-Type: application/zip" \
    "${API}/upload/v2/${ITEM_PATH}:upload")" \
    || die "upload request failed"

if jq -e '.error' >/dev/null 2>&1 <<<"$UPLOAD"; then
    die "upload rejected: $(jq -rc '.error | {code, message, status}' <<<"$UPLOAD")"
fi

UPLOAD_STATE="$(jq -r '.uploadState // "UPLOAD_STATE_UNSPECIFIED"' <<<"$UPLOAD")"
CRX_VERSION="$(jq -r '.crxVersion // ""' <<<"$UPLOAD")"
say "upload state: $UPLOAD_STATE${CRX_VERSION:+ (crxVersion $CRX_VERSION)}"

# An async upload reports IN_PROGRESS; poll until it settles before publishing,
# otherwise `publish` races the package and fails.
if [ "$UPLOAD_STATE" = "IN_PROGRESS" ]; then
    say "upload is asynchronous — polling fetchStatus"
    deadline=$(( $(date +%s) + POLL_TIMEOUT ))
    while :; do
        [ "$(date +%s)" -lt "$deadline" ] || die "timed out after ${POLL_TIMEOUT}s waiting for the upload to finish"
        sleep "$POLL_INTERVAL"
        STATUS="$(api_get)" || die "fetchStatus failed while polling the upload"
        UPLOAD_STATE="$(jq -r '.lastAsyncUploadState // "UPLOAD_STATE_UNSPECIFIED"' <<<"$STATUS")"
        say "upload state: $UPLOAD_STATE"
        case "$UPLOAD_STATE" in
            SUCCEEDED) break ;;
            FAILED|NOT_FOUND) die "upload finished as $UPLOAD_STATE" ;;
        esac
    done
fi

# --- publish (this is also submit-for-review) -----------------------------
#
# There is no separate "submit" call: `publish` both submits the revision for
# review and — once approved — flips it live, governed by publishType.

BODY="$(jq -n \
    --arg publishType "${CWS_PUBLISH_TYPE:-DEFAULT_PUBLISH}" \
    --argjson skipReview "${CWS_SKIP_REVIEW:-false}" \
    '{publishType: $publishType, skipReview: $skipReview}')"

say "submitting for review: $BODY"
RESP="$(curl -sS -X POST \
    -H "Authorization: Bearer ${CWS_ACCESS_TOKEN}" \
    -H "Content-Type: application/json" \
    -d "$BODY" \
    "${API}/v2/${ITEM_PATH}:publish")" \
    || die "publish request failed"

if jq -e '.error' >/dev/null 2>&1 <<<"$RESP"; then
    die "publish rejected: $(jq -rc '.error | {code, message, status, details}' <<<"$RESP")"
fi

if [ "$(jq -r '.warningInfo.warnings // [] | length' <<<"$RESP")" != "0" ]; then
    say "warnings: $(jq -rc '[.warningInfo.warnings[] | {reason, description}]' <<<"$RESP")"
fi

FINAL_STATE="$(jq -r '.state // "ITEM_STATE_UNSPECIFIED"' <<<"$RESP")"
say "submitted; item state: $FINAL_STATE"
say "the listing goes live once review approves it — no further API call is needed"
