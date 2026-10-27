package desktop

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/howashoji/looptrack/internal/service"
)

// デスクトップ版の版は、画面のサーバ（localserve）への設定にそのまま渡る
// （利用者メニューの「ガイド」の行き先がこの版で決まる。渡し忘れると黙って latest へ向かう）。
func TestServeOptionsCarryVersion(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	paths := Paths{DataDir: t.TempDir()}
	for _, v := range []string{"v1.0.1", "v1.0.0-rc.5", "dev"} {
		o := &Options{Version: v}
		got := o.serveOptions(paths, nil, logger, &updates{})
		if got.Version != v {
			t.Errorf("Options.Version %q → localserve.Options.Version %q", v, got.Version)
		}
		if got.DBPath != paths.DB() || got.BasePath != BasePath {
			t.Errorf("版以外の設定が崩れた: DBPath=%q BasePath=%q", got.DBPath, got.BasePath)
		}
	}
	a, b := (&Options{Version: "v1.0.1"}).serveOptions(paths, nil, logger, &updates{}), (&Options{Version: "dev"}).serveOptions(paths, nil, logger, &updates{})
	if a.Version == b.Version {
		t.Errorf("対照: 版の違いが設定に出ていない: %q", a.Version)
	}
}

// デスクトップ版の会話記録の置き場は DataDir/transcripts で、添付の置き場（DataDir/attachments）とは別のディレクトリ
// （入れ子だと会話記録が使えない。service.BlobRootsNested）。
func TestServeOptionsTranscriptDir(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	paths := Paths{DataDir: t.TempDir()}
	got := (&Options{Version: "dev"}).serveOptions(paths, nil, logger, &updates{})
	if got.TranscriptDir != filepath.Join(paths.DataDir, "transcripts") || got.AttachDir != filepath.Join(paths.DataDir, "attachments") {
		t.Errorf("TranscriptDir=%q AttachDir=%q", got.TranscriptDir, got.AttachDir)
	}
	if service.BlobRootsNested(got.AttachDir, got.TranscriptDir) {
		t.Errorf("添付と会話記録の置き場が入れ子: %q %q", got.AttachDir, got.TranscriptDir)
	}
}
