package transcript

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/howashoji/looptrack/internal/client/jsonorder"
	"github.com/howashoji/looptrack/internal/i18n"
)

// Target は取り込む端末の値（プレースホルダを戻す先。DESIGN.md §9-8 の種類の表）。
type Target struct {
	// Root は取り込む端末の作業ツリーのルートの realpath（ROOT0・ROOT1… はすべてここに戻す）。
	Root string
	// SessionDir は取り込む端末の <設定の置き場>/projects/<新しい slug>（SDIR）。
	SessionDir string
	// VendorDir は取り込む端末の AI の設定の置き場（VDIR）。
	VendorDir string
	// Home は取り込む端末のホーム（HOME）。
	Home string
}

var (
	nonceRe = regexp.MustCompile(`^[0-9a-f]{12}$`)
	kindRe  = regexp.MustCompile(`^(ROOT[0-9]+|SDIR|VDIR|HOME)(J?)$`)
	// safeKindRe はエラーに出してよい種類の形（それ以外は中身の一部かもしれないので出さない）。
	safeKindRe = regexp.MustCompile(`^[A-Z0-9]{1,12}$`)
)

func (t Target) value(kind string) (string, bool, bool) {
	m := kindRe.FindStringSubmatch(kind)
	if m == nil {
		return "", false, false
	}
	var v string
	switch {
	case strings.HasPrefix(m[1], "ROOT"):
		v = t.Root
	case m[1] == "SDIR":
		v = t.SessionDir
	case m[1] == "VDIR":
		v = t.VendorDir
	case m[1] == "HOME":
		v = t.Home
	}
	v = strings.TrimRight(v, `/\`)
	return v, m[2] == "J", v != ""
}

// Restore はチャンクの中身のプレースホルダを、取り込む端末の値に戻す（pull）。
// プレースホルダは JSON の文字列の中に生の字面のまま現れる（ASCII で逃がしが要らない）ので、字面の上で置き換え、
// 値は JSON の文字列の中身として逃がして入れる（J の種類は、JSON の文字列の中の JSON の形にもう 1 段逃がす）。
// 送る端末の値で戻せば、マスクに当たらなかった行は元のバイトに戻る。
// 戻せない種類（知らない種類・値が無い）があれば何も返さずにエラーにする。戻した後に JSON として読めない行があってもエラー。
func Restore(data []byte, kind Kind, nonce string, t Target) ([]byte, error) {
	if kind == KindText || nonce == "" {
		return data, nil
	}
	if kind != KindJSON && kind != KindJSONL {
		return nil, i18n.Errorf("transcript.err.kind", "kind", string(kind))
	}
	if !nonceRe.MatchString(nonce) {
		return nil, i18n.Errorf("transcript.err.nonce_format")
	}
	head := []byte(placeholderHead(nonce))
	var out bytes.Buffer
	out.Grow(len(data))
	for i := 0; ; {
		k := bytes.Index(data[i:], head)
		if k < 0 {
			out.Write(data[i:])
			break
		}
		out.Write(data[i : i+k])
		at := i + k + len(head)
		end := bytes.Index(data[at:], []byte("}}"))
		if end < 0 {
			return nil, i18n.Errorf("transcript.err.restore_kind", "kind", "?")
		}
		kd := string(data[at : at+end])
		v, escaped, ok := t.value(kd)
		if !ok {
			if !safeKindRe.MatchString(kd) {
				kd = "?"
			}
			return nil, i18n.Errorf("transcript.err.restore_kind", "kind", kd)
		}
		v = jsonInner(v)
		if escaped {
			v = jsonInner(v)
		}
		out.WriteString(v)
		i = at + end + 2
	}
	res := out.Bytes()
	bad := 0
	if kind == KindJSON {
		if _, err := jsonorder.Decode(res); err != nil {
			bad = 1
		}
	} else {
		ls, _ := splitLines(res)
		for _, l := range ls {
			if _, err := jsonorder.Decode(l.body); err != nil {
				bad++
			}
		}
	}
	if bad > 0 {
		return nil, i18n.Errorf("transcript.err.restore_unreadable", "count", bad)
	}
	return res, nil
}
