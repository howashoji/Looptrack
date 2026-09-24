//go:build !unix && !windows

package proc

import "context"

// list はプロセスの一覧を取れない OS（js/wasm・plan9 など）。runaway の検知は何もしない（fail-open）。
func list(context.Context) ([]Proc, error) { return nil, ErrUnsupported }
