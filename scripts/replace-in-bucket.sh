#!/usr/bin/env bash
# Download → upscale → upload, in place, for a list of anime ids.
#
# usage: scripts/replace-in-bucket.sh <prefix> <kinds> <id> [<id> ...]
#   prefix  bucket prefix, e.g. weeb-staging or weeb
#   kinds   comma-separated object kinds: banners,posters (root for the bare <id>)
#
# Needs AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / R2_ENDPOINT in the
# environment (the bucket credentials), and the service on UPSCALER_URL
# (default http://127.0.0.1:3100). An object that does not exist is skipped;
# one already wider than MIN_WIDTH (default 1000) is left alone, so a second
# run does not upscale the upscale. Prints one line per object, and the CDN
# URLs to purge at the end.
set -u
PREFIX=$1; KINDS=$2; shift 2
BUCKET=${BUCKET:-weeb}
UPSCALER_URL=${UPSCALER_URL:-http://127.0.0.1:3100}
MIN_WIDTH=${MIN_WIDTH:-1000}
FORMAT=${FORMAT:-jpg}
EP=${R2_ENDPOINT:?set R2_ENDPOINT to https://<account>.r2.cloudflarestorage.com}
WORK=$(mktemp -d)
PURGE=()
width() { sips -g pixelWidth "$1" 2>/dev/null | awk '/pixelWidth/ {print $2}'; }
for ID in "$@"; do
  for KIND in ${KINDS//,/ }; do
    if [ "$KIND" = root ]; then KEY="$PREFIX/$ID"; else KEY="$PREFIX/$KIND/$ID"; fi
    IN="$WORK/$ID-$KIND.in"; OUT="$WORK/$ID-$KIND.$FORMAT"
    if ! aws s3 cp --endpoint-url "$EP" "s3://$BUCKET/$KEY" "$IN" >/dev/null 2>&1; then
      printf '%-48s missing, skipped\n' "$KEY"; continue
    fi
    W=$(width "$IN")
    if [ -n "$W" ] && [ "$W" -ge "$MIN_WIDTH" ]; then
      printf '%-48s already %spx wide, skipped\n' "$KEY" "$W"; continue
    fi
    if ! curl -sf --data-binary "@$IN" -H 'Content-Type: image/jpeg' "$UPSCALER_URL/upscale?format=$FORMAT" -o "$OUT"; then
      printf '%-48s upscale failed\n' "$KEY"; continue
    fi
    CT=image/jpeg; [ "$FORMAT" = png ] && CT=image/png; [ "$FORMAT" = webp ] && CT=image/webp
    if aws s3 cp --endpoint-url "$EP" "$OUT" "s3://$BUCKET/$KEY" --content-type "$CT" >/dev/null 2>&1; then
      printf '%-48s %spx -> %spx, %s uploaded\n' "$KEY" "$W" "$(width "$OUT")" "$(du -h "$OUT" | cut -f1)"
      PURGE+=("https://cdn.weeb.vip/$KEY")
    else
      printf '%-48s upload failed\n' "$KEY"
    fi
  done
done
rm -rf "$WORK"
echo "--- purge these URLs in Cloudflare:"
printf '%s\n' "${PURGE[@]}"
