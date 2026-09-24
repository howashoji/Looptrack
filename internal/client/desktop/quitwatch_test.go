package desktop

import (
	"strconv"
	"strings"
	"testing"
)

// TestQuitEventName は終了を頼む印の名前が、同じセッションの中で pid ごとに分かれることを確かめる。
// 別のデータの置き場で動くインスタンスに、間違って終了を頼まないための決まり。
func TestQuitEventName(t *testing.T) {
	a, b := quitEventName(1234), quitEventName(5678)
	if a == b {
		t.Errorf("pid が違えば名前も違うはず: %q", a)
	}
	if a != quitEventName(1234) {
		t.Errorf("同じ pid なら同じ名前のはず: %q と %q", a, quitEventName(1234))
	}
	if !strings.HasPrefix(a, `Local\`) {
		t.Errorf("同じログオンセッションの中だけで見える名前空間（Local\\）にするはず: %q", a)
	}
	if !strings.HasSuffix(a, strconv.Itoa(1234)) {
		t.Errorf("名前に pid が入るはず: %q", a)
	}
}
