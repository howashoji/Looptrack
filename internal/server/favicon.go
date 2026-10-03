package server

import "net/http"

// serveIcon は埋め込んだ favicon の画像（assets 内のパス）を返す。認証は要らない（ログイン前の画面のタブにも出すため）。
// 絵は実行ファイルに埋め込んだ固定のバイト列なので、1 日はブラウザに持たせる（既定の no-store のままだと、画面を開くたびに取りに来る）。
func serveIcon(contentType, name string) http.HandlerFunc {
	body, err := assets.ReadFile(name)
	if err != nil {
		panic("favicon is not embedded: " + name) // //go:embed の取りこぼし。起動時に落ちる
	}
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", contentType)
		h.Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(body)
	}
}
