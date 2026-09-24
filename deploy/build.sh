#!/bin/bash
# サーバの実行イメージ（looptrack serve）を Mac で作る（linux/amd64・静的リンク）。サーバではビルドしない。
#   bash deploy/build.sh            → looptrack:<バージョン> と looptrack:latest を作る
#   LOOPTRACK_VERSION=v1 bash deploy/build.sh
# バージョンの既定は「日付-短いコミット ID（変更があれば -dirty）」。
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${LOOPTRACK_VERSION:-$(date +%Y%m%d)-$(git rev-parse --short HEAD)$(git diff --quiet || echo -dirty)}"
OUT=$(mktemp -d)
trap 'rm -rf "$OUT"' EXIT
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" -o "$OUT/looptrack" ./cmd/looptrack
cp deploy/Dockerfile "$OUT/Dockerfile"
cp NOTICE "$OUT/NOTICE"
docker build --platform linux/amd64 -t "looptrack:$VERSION" -t looptrack:latest "$OUT" >&2
echo "$VERSION"
