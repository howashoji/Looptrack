# Looptrack のサーバの実行イメージ（looptrack serve）。**ビルドはこの Dockerfile では行わない**（サーバの CPU・メモリを使わないため、
# Mac で linux/amd64 に静的リンクした実行ファイルを deploy/build.sh が用意し、ここでは置くだけにする）。
# scratch なのでシェルも curl も無い。健全性確認は `looptrack healthcheck`（自分自身の /healthz を叩く）で行う。
# 管理コマンドは docker exec <コンテナ> /looptrack <サブコマンド>（user・project・migrate など）。
FROM scratch
COPY looptrack /looptrack
# 第三者のライセンス文（リポジトリのルートの NOTICE。材料は dist.sh image-context・deploy/build.sh が置く）
COPY NOTICE /NOTICE
USER 65534:65534
EXPOSE 8090
ENTRYPOINT ["/looptrack"]
CMD ["serve"]
