package api

import (
	"bytes"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
)

// 更新トークンでの取り直し。
// ログイン（ブラウザ・PKCE）は login.go。

// OAuthRequest は認可サーバへの要求（form があれば x-www-form-urlencoded、body があれば JSON で POST、どちらも無ければ GET）。
// (状態コード, 応答の JSON オブジェクト) を返す（エラーの応答も返す。オブジェクトでなければ空）。接続できなければ *ConnError
// （URL は /oauth/ と /.well-known/ の前まで）。
func (c *Client) OAuthRequest(fullURL string, form url.Values, body any) (int, *jsonorder.Object, error) {
	headers := map[string]string{"User-Agent": c.UserAgent, "Accept": "application/json"}
	method := http.MethodGet
	var data *bytes.Reader
	switch {
	case form != nil:
		data = bytes.NewReader([]byte(form.Encode()))
		headers["Content-Type"] = "application/x-www-form-urlencoded"
		method = http.MethodPost
	case body != nil:
		data = bytes.NewReader(jsonorder.Marshal(body))
		headers["Content-Type"] = "application/json; charset=utf-8"
		method = http.MethodPost
	}
	var status int
	var raw []byte
	var err error
	if data != nil {
		status, _, raw, err = c.send(method, fullURL, data, headers)
	} else {
		status, _, raw, err = c.send(method, fullURL, nil, headers)
	}
	if err != nil {
		return 0, nil, &ConnError{URL: oauthBase(fullURL), Reason: err.Error()}
	}
	out, derr := jsonorder.DecodeObject(raw)
	if derr != nil {
		out = jsonorder.NewObject()
	}
	return status, out, nil
}

func oauthBase(full string) string {
	for _, sep := range []string{"/oauth/", "/.well-known/"} {
		if i := strings.Index(full, sep); i >= 0 {
			full = full[:i]
		}
	}
	return full
}

// TokenFields はトークン応答から資格情報に控える項目（token・refresh_token・expires_at）を entry に書く。
func (c *Client) TokenFields(entry, res *jsonorder.Object) {
	entry.Set("token", res.String("access_token"))
	if rt, _ := res.Get("refresh_token"); jsonorder.Truthy(rt) {
		entry.Set("refresh_token", rt)
	}
	if v, ok := res.Get("expires_in"); ok {
		if n, ok := jsonorder.Int(v); ok {
			entry.Set("expires_at", jsonorder.Number(strconv.FormatInt(c.Now().Unix()+n, 10)))
		}
	}
}

// refresh は保存した更新トークンで取り直して資格情報を書き換え、新しいアクセストークンを返す。
// 別のプロセスが先に取り直していればその値を返す。取り直せなければ ("", 理由)。理由は none（更新トークンが無い）・
// invalid（期限切れ・失効。保存から消す）・error（接続できない等。消さない）。資格情報のファイルの誤りは error で返す。
func (c *Client) refresh(base, used string) (string, string, error) {
	unlock, err := c.Creds.Lock()
	if err != nil {
		return "", "", err
	}
	defer unlock()
	entry, err := c.Creds.Entry(base)
	if err != nil {
		return "", "", err
	}
	if tok := entry.String("token"); tok != "" && tok != used {
		return tok, "", nil
	}
	rt, _ := entry.Get("refresh_token")
	cid, _ := entry.Get("client_id")
	if !jsonorder.Truthy(rt) || !jsonorder.Truthy(cid) {
		return "", "none", nil
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {jsonorder.Str(rt)}, "client_id": {jsonorder.Str(cid)},
		"resource": {base + "/api/v1"}}
	status, res, err := c.OAuthRequest(base+"/oauth/token", form, nil)
	if err != nil {
		return "", "error", nil
	}
	if status != http.StatusOK || res.String("access_token") == "" {
		switch res.String("error") {
		case "invalid_grant", "invalid_client", "unauthorized_client":
			if status == 400 || status == 401 {
				entry.Delete("refresh_token")
				if err := c.Creds.SaveEntry(base, entry); err != nil {
					return "", "", err
				}
				return "", "invalid", nil
			}
		}
		return "", "error", nil
	}
	if rt, _ := res.Get("refresh_token"); !jsonorder.Truthy(rt) {
		entry.Delete("refresh_token")
	}
	entry.Delete("expires_at")
	c.TokenFields(entry, res)
	if err := c.Creds.SaveEntry(base, entry); err != nil {
		return "", "", err
	}
	return entry.String("token"), "", nil
}

// refreshIfExpiring は保存した期限が近ければ先に取り直す（失敗しても今のトークンで続ける。期限切れなら 401 の側で扱う）。
func (c *Client) refreshIfExpiring(base, token string) (string, error) {
	entry, err := c.Creds.Entry(base)
	if err != nil {
		return "", err
	}
	exp, ok := jsonorder.Float(mustGet(entry, "expires_at"))
	rt, _ := entry.Get("refresh_token")
	if entry.String("token") != token || !jsonorder.Truthy(rt) || !ok {
		return token, nil
	}
	now := float64(c.Now().UnixNano()) / float64(time.Second)
	if exp-now > RefreshMargin.Seconds() {
		return token, nil
	}
	fresh, _, err := c.refresh(base, token)
	if err != nil {
		return "", err
	}
	if fresh == "" {
		return token, nil
	}
	return fresh, nil
}

func mustGet(o *jsonorder.Object, key string) any {
	v, _ := o.Get(key)
	return v
}
