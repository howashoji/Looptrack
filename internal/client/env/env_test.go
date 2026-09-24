package env

import "testing"

func TestSettingReadsOnlyLooptrack(t *testing.T) {
	e := FromMap(map[string]string{"LOOPTRACK_API_URL": "https://new", "IM_PROJECT": "im", "LOOPTRACK_TOKEN": "  ", "IM_TOKEN": "imp_x"})
	if v, n := e.Setting(APIURL); v != "https://new" || n != "LOOPTRACK_API_URL" {
		t.Errorf("LOOPTRACK_* を読まない: %s %s", v, n)
	}
	// 旧名 IM_* は読まない（フォールバックを製品に残さない）
	if v, n := e.Setting(Project); v != "" || n != "" {
		t.Errorf("旧名 IM_PROJECT を読んだ: %s %s", v, n)
	}
	// 空白だけの値は無いとみなす（以前の CLI と同じ）
	if v, n := e.Setting(Token); v != "" || n != "" {
		t.Errorf("空白だけの LOOPTRACK_TOKEN か旧名 IM_TOKEN を使った: %q %s", v, n)
	}
	if v, n := e.Setting(Timeout); v != "" || n != "" {
		t.Errorf("無い設定: %q %s", v, n)
	}
	if Name(APIURL) != "LOOPTRACK_API_URL" || Name(SessionID) != "LOOPTRACK_SESSION_ID" {
		t.Error("名前")
	}
	var zero Env
	if zero.Get("PATH") != "" {
		t.Error("ゼロ値の Env が OS の環境変数を読んだ")
	}
}
