package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
)

// 既定で切の機能の知らせ（判定は service の DefaultOffFeatures の 1 か所）。
// serve の起動ログと looptrack features（install.sh の --upgrade の終わりに呼ぶ）がここを通る。

// featuresCmd は looptrack features。既定で切の機能ごとに入のプロジェクトの数を示し、どこでも切なら入れ方を添える。
// 知らせだけなので、どの結果でも終了コードは 0（DB を開けないなどの誤りだけが 1）。
// install.sh が名前で呼ぶので、サブコマンドの名前は変えない。
func featuresCmd(args []string) int {
	lang := cmdLang()
	if len(args) > 0 {
		return usageErr(i18n.T(lang, "cmd.err.extra_args", "args", strings.Join(args, " ")))
	}
	db, err := openDB()
	if err != nil {
		return fail(err)
	}
	defer db.Close()
	if err := printFeatures(context.Background(), db, lang, os.Stdout); err != nil {
		return fail(err)
	}
	return 0
}

// printFeatures は looptrack features の本体。プロジェクトが 0 件なら 1 行だけ出す。
func printFeatures(ctx context.Context, db *sql.DB, lang i18n.Lang, w io.Writer) error {
	counts, err := service.DefaultOffFeatureCounts(ctx, db)
	if err != nil {
		return err
	}
	if len(counts) > 0 && counts[0].Total == 0 {
		fmt.Fprintln(w, i18n.T(lang, "cmd.features.no_projects"))
		return nil
	}
	for _, c := range counts {
		fmt.Fprintln(w, i18n.T(lang, "cmd.features.count", "name", c.Feature.Name(lang), "key", c.Feature.Key,
			"enabled", c.Enabled, "total", c.Total))
		if c.OffEverywhere() {
			fmt.Fprintln(w, "  "+service.FeatureOffNotice(lang, c.Feature))
			fmt.Fprintln(w, "  "+service.FeatureEnableHint(lang, c.Feature))
		}
	}
	return nil
}

// logFeaturesOff は serve の起動時に、どこでも切の機能ごとに 1 行の Warn を残す（該当なしなら何も出さない）。
// 読めなければ起動は止めず、その旨だけを残す。
func logFeaturesOff(ctx context.Context, db *sql.DB, lang i18n.Lang, logger *slog.Logger) {
	counts, err := service.DefaultOffFeatureCounts(ctx, db)
	if err != nil {
		logger.Warn("feature-notices", "error", err.Error())
		return
	}
	for _, c := range counts {
		if !c.OffEverywhere() {
			continue
		}
		logger.Warn(i18n.T(lang, "cmd.serve.feature_off", "notice", service.FeatureOffNotice(lang, c.Feature),
			"hint", service.FeatureEnableHint(lang, c.Feature)), "feature", c.Feature.Key, "since", c.Feature.Since)
	}
}
