// Package proxycheck は、サーバの前段のリバースプロキシが会話記録の 1 チャンク（既定 32MiB）を通すかを確かめる
// （looptrack check-proxy・install.sh の起動の後）。
//
// 前段の本文の上限（nginx の client_max_body_size。既定 1MB）が小さいと、チャンクの追記は前段が JSON でない 413 で止め、
// looptrack には届かない。サーバのログにも残らないので、管理者はそれを知る手段が無かった。ここでは公開の URL へ、
// チャンクの上限と同じ大きさの本文を認証なしで送り、返った応答で前段を判定する:
//   - looptrack の誤りの形の JSON（"error" の鍵に code か message を持つ。認証が無いときの 401 など）… 本文が前段を通って
//     looptrack まで届いた。足りている
//   - looptrack の誤りの形でない 413 … 前段が大きさで止めた。足りない。JSON でも "error" を持たなければ前段のものとみる
//     （クライアントが「前段の上限」と判じる形と同じ。internal/client/api の ProxyBodyLimit）
//   - つながらない・ほかの応答 … 確かめられなかった（ホストから自分の公開 URL に届かないこともある）
//
// 確かめきれない前段もある。読んだバイト数で止める前段（Caddy の request_body の max_size など）は、本文を読み切る前に
// 後段の応答が返ると上限に届かない。looptrack は認証が無ければ本文を読まずに 401 を返すので、その前段では
// 「足りている」と出ても実際のチャンクは前段の 413 で落ちうる。塞がない: 落ちた送信はクライアントの summary に
// 「前段の上限」として出るので、踏んだ人がその場で気づける。足りているときの出力の文にもそのことを書く。
//
// 送り先は会話記録の追記と同じ経路（POST <基点>/api/v1/transcripts/0/chunks）。前段が経路ごとに上限を変えていても、
// 実際のチャンクと同じ上限に当たる。looptrack は認証を経路より先に見るので、トークンが無ければ JSON の 401 で応え、
// 会話記録には何も書かない。
//
// 送るときは Expect: 100-continue を付ける。前段が見出しの Content-Length だけで 413 を返すと、本文は送られない
// （32MiB を無駄に流さない。前段が先に応えて接続を閉じたとき、送りかけの本文の書き込みの誤りで応答を取り落とさない）。
package proxycheck

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ChunkPath は送り先の経路（基点の後ろ）。会話記録の追記の経路で、id 0 の会話記録は存在しない。
const ChunkPath = "/api/v1/transcripts/0/chunks"

// Verdict は判定。
type Verdict int

const (
	// Enough は looptrack の誤りの形の JSON が返った（本文が前段を通って looptrack まで届いた）。
	Enough Verdict = iota
	// TooSmall は looptrack の誤りの形でない 413 が返った（前段が本文の大きさで止めた）。
	TooSmall
	// Unknown はつながらない・ほかの応答で、確かめられなかった。
	Unknown
)

// Result は 1 回の確認の結果。
type Result struct {
	Verdict Verdict
	URL     string // 送った先
	Status  int    // 応答の HTTP の状態（応答が無ければ 0）
	Err     error  // つながらなかったときの誤り（Unknown のとき）
}

// Target は公開の URL（LOOPTRACK_PUBLIC_URL）と URL の接頭辞（LOOPTRACK_BASE_PATH）から送り先を作る。
// publicURL が空・http(s) でなければ誤り。
func Target(publicURL, basePath string) (string, error) {
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	u, err := url.Parse(publicURL)
	if publicURL == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("bad public URL")
	}
	basePath = "/" + strings.Trim(strings.TrimSpace(basePath), "/")
	if basePath == "/" {
		basePath = ""
	}
	return publicURL + basePath + ChunkPath, nil
}

// zeros は 0 のバイトを返し続ける読み手（大きな本文をメモリに持たずに送る）。
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// NewClient は確認に使う HTTP クライアント（100-continue を最大 wait まで待つ。全体の締切は timeout）。
func NewClient(timeout, wait time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ExpectContinueTimeout = wait
	return &http.Client{Timeout: timeout, Transport: tr,
		// 転送の先（http から https へ など）では本文を送り直せないので、最初の応答で判定する
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Check は target へ size バイトの本文を認証なしで POST し、前段を判定する。
func Check(ctx context.Context, client *http.Client, target string, size int64) Result {
	r := Result{Verdict: Unknown, URL: target}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, io.LimitReader(zeros{}, size))
	if err != nil {
		r.Err = err
		return r
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Expect", "100-continue")
	req.Header.Set("X-Looptrack-Client", "check-proxy")
	res, err := client.Do(req)
	if err != nil {
		r.Err = err
		return r
	}
	defer res.Body.Close()
	r.Status = res.StatusCode
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	switch {
	case fromLooptrack(body):
		r.Verdict = Enough
	case res.StatusCode == http.StatusRequestEntityTooLarge:
		r.Verdict = TooSmall
	}
	return r
}

// fromLooptrack は本文が looptrack の誤りの形（{"error":{"code":…,"message":…}}）か。code か message のどちらかが
// 空でなければ真。前段が返す JSON（{"message":…} など）は偽にする（クライアントの「前段の上限」の判定と同じ境目）。
func fromLooptrack(body []byte) bool {
	var v struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &v) != nil || v.Error == nil {
		return false
	}
	return v.Error.Code != "" || v.Error.Message != ""
}
