// Package env は looptrack（クライアント側）が読む環境変数を 1 か所にまとめる。
//
// 設定の環境変数は LOOPTRACK_* だけを読む（旧名 IM_* のフォールバックは製品に残さないと決めた。
// 旧名を読んでいた以前の CLI は撤去した）。
// 変えるときは Prefix と下の設定名（APIURL など）だけを直す（他のパッケージは名前を直接書かない）。
//
// テストから本番に書き込まないよう、読み取りは Env（参照の関数）を通す。OS の環境変数を読むのは OS() だけ。
package env

import (
	"os"
	"strings"
)

// Prefix は設定の環境変数の接頭辞。
const Prefix = "LOOPTRACK_"

// 設定名（接頭辞を除いた部分）。
const (
	APIURL    = "API_URL"    // サーバの URL（例 https://example.com/looptrack）
	Project   = "PROJECT"    // プロジェクトの slug
	Token     = "TOKEN"      // アクセストークン（無ければ資格情報のファイル）
	Timeout   = "TIMEOUT"    // API の待ち時間（秒。既定 30）
	SessionID = "SESSION_ID" // AI のセッション ID の明示（X-Looptrack-Session）
)

// Env は環境変数の参照。ゼロ値は何も無い環境。
type Env struct {
	lookup func(string) (string, bool)
}

// OS はこのプロセスの環境変数。
func OS() Env { return Env{lookup: os.LookupEnv} }

// FromMap は与えた値だけを持つ環境（テスト用。OS の環境変数を読まない）。
func FromMap(m map[string]string) Env {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return Env{lookup: func(k string) (string, bool) { v, ok := c[k]; return v, ok }}
}

// FromFunc は読み取りの関数（os.Getenv の形）から作る環境。値が "" の名前は無いものとする
// （hook の Getenv から CLI の環境を作るとき。Copilot 向けの SessionStart の合成で使う）。
func FromFunc(get func(string) string) Env {
	return Env{lookup: func(k string) (string, bool) {
		v := get(k)
		return v, v != ""
	}}
}

// Lookup は名前そのままで読む。
func (e Env) Lookup(name string) (string, bool) {
	if e.lookup == nil {
		return "", false
	}
	return e.lookup(name)
}

// Get は名前そのままで読む（無ければ ""）。
func (e Env) Get(name string) string {
	v, _ := e.Lookup(name)
	return v
}

// Setting は設定名（APIURL など）の値と、値を取った環境変数の名前を返す。空白だけのものは無いとみなす
// （以前の CLI と同じ扱い）。値は前後の空白を除かずに返す。
func (e Env) Setting(name string) (value, varName string) {
	if v := e.Get(Name(name)); strings.TrimSpace(v) != "" {
		return v, Name(name)
	}
	return "", ""
}

// Value は Setting の値だけ。
func (e Env) Value(name string) string {
	v, _ := e.Setting(name)
	return v
}

// Name は設定名に対応する環境変数の名前（LOOPTRACK_API_URL など）。案内の文面にもこの名前を出す。
func Name(name string) string { return Prefix + name }

// Overlay は e に上書きを重ねた環境（値が "" の名前は無いものとする）。引数の --url を環境変数より優先するときに使う。
func Overlay(e Env, vars map[string]string) Env {
	c := make(map[string]string, len(vars))
	for k, v := range vars {
		c[k] = v
	}
	return Env{lookup: func(k string) (string, bool) {
		if v, ok := c[k]; ok {
			return v, v != ""
		}
		return e.Lookup(k)
	}}
}
