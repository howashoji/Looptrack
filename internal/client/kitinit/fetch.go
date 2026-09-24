package kitinit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/client/api"
	"github.com/howashoji/looptrack/internal/client/cli"
	"github.com/howashoji/looptrack/internal/client/env"
	"github.com/howashoji/looptrack/internal/i18n"
)

// fetchKit はサーバの配布物から kit/ のファイルを取り、SHA-256 を確かめて返す。
// dist（setup ツールの期限つき URL）があればトークンなしでそこから、無ければ GET /api/v1/dist（トークンが要る）から取る。
// 配布物に残っている以前の CLI（1.0.0 より前）のスクリプトは取らない（置かないため）。
func fetchKit(e env.Env, url, dist string) (map[string]string, error) {
	cl, err := api.New(e)
	if err != nil {
		return nil, err
	}
	if dist == "" {
		if tok, _, _ := cl.TokenSource(url); tok == "" && !cl.LocalMode(url) { // ローカルモードのサーバはトークンなしで取れる
			return nil, i18n.Errorf("kitinit.fetch.err.no_token", "cli", GoCLI, "url", url)
		}
	}
	dist = strings.TrimRight(dist, "/")
	get := func(name string) ([]byte, error) {
		if dist != "" {
			p := "/"
			if name != "" {
				p += escapeName(name)
			}
			return plainGet(e, dist+p)
		}
		path := "/dist"
		if name != "" {
			path += "/" + escapeName(name)
		}
		res, err := cl.Do(api.Request{Method: "GET", Path: path, URL: url, Raw: name != "", Headers: acceptAny(name != "")})
		if err != nil {
			return nil, err
		}
		return res.Raw, nil
	}
	wrap := func(err error) error {
		var ae *api.Error
		if errors.As(err, &ae) {
			return i18n.Errorf("kitinit.fetch.err.get", "reason", ae.Message)
		}
		return cli.Failf("%s", err.Error())
	}
	raw, err := get("")
	if err != nil {
		return nil, wrap(err)
	}
	var listing struct {
		Files []struct {
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil, i18n.Wrapf(err, "kitinit.fetch.err.listing")
	}
	out := map[string]string{}
	for _, f := range listing.Files {
		if !strings.HasPrefix(f.Name, "kit/") {
			continue
		}
		body, err := get(f.Name)
		if err != nil {
			return nil, wrap(err)
		}
		if sha(string(body)) != f.SHA256 {
			return nil, i18n.Errorf("kitinit.fetch.err.sha_mismatch", "file", f.Name)
		}
		out[f.Name] = string(body)
	}
	return out, nil
}

func acceptAny(raw bool) map[string]string {
	if !raw {
		return nil
	}
	return map[string]string{"Accept": "*/*"}
}

// escapeName は kit の名前（kit/core/…）を「/」を残して URL に入れる。
func escapeName(name string) string {
	parts := strings.Split(name, "/")
	for i, s := range parts {
		parts[i] = api.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// plainGet は認証なしの GET（setup ツールの期限つき URL 用）。
func plainGet(e env.Env, full string) ([]byte, error) {
	cl := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: api.TLSConfig(e)}}
	req, err := http.NewRequest("GET", full, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "looptrack/"+api.Version)
	req.Header.Set("Accept", "*/*")
	res, err := cl.Do(req)
	if err != nil {
		reason := err.Error()
		var ue interface{ Unwrap() error }
		if errors.As(err, &ue) && ue.Unwrap() != nil {
			reason = ue.Unwrap().Error()
		}
		return nil, &api.ConnError{URL: strings.Split(full, "/setup/")[0], Reason: reason}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, &api.ConnError{URL: strings.Split(full, "/setup/")[0], Reason: err.Error()}
	}
	if res.StatusCode/100 != 2 {
		var p struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &p)
		msg := p.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", res.StatusCode)
		}
		return nil, &api.Error{Status: res.StatusCode, Code: p.Error.Code, Message: msg}
	}
	return body, nil
}
