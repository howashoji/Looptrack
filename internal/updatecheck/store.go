package updatecheck

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/howashoji/looptrack/internal/privfile"
)

// Record は控えのファイル（既定の名前は FileName）の中身: 利用者の設定（メニューの切り替え）と、最後の確認の結果。
type Record struct {
	Prefs
	Last *Result `json:"last,omitempty"`
	// LastOK は最後に成功した確認の結果（up_to_date か available）。通信に失敗した回に前の知らせを残すために使う
	// （NoticeFor）。確認を止める（SetEnabled(false)）と消す
	LastOK *Result `json:"last_ok,omitempty"`
}

// Store は控えのファイル。同じプロセスの中の読み書き（確認の結果の記録とメニューの切り替え）はこれを通して直列にする。
type Store struct {
	Path string
	mu   sync.Mutex
}

// Load は控えを読む（無い・壊れているときは空。確認はそのまま既定の設定で続ける）。
func (s *Store) Load() Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) load() Record {
	var r Record
	if b, err := os.ReadFile(s.Path); err == nil {
		if json.Unmarshal(b, &r) != nil {
			return Record{}
		}
	}
	return r
}

func (s *Store) save(r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return privfile.WriteFile(s.Path, append(b, '\n'))
}

// update は読んで f で直して書く（間にほかの書き込みを挟まない）。
func (s *Store) update(f func(*Record)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.load()
	f(&r)
	return s.save(r)
}

// SetEnabled は確認の有無を切り替える（メニューの切り替え。false で "check": "off"）。
// 環境変数 LOOPTRACK_UPDATE_CHECK=off はこの設定に依らず確認を止める。
func (s *Store) SetEnabled(on bool) error {
	return s.update(func(r *Record) {
		if on {
			r.Check = ""
		} else {
			r.Check = "off"
			r.LastOK = nil
		}
	})
}

// SetChannel はチャンネルを決める（空で既定に戻す。環境変数 LOOPTRACK_UPDATE_CHANNEL があればそちらが勝つ）。
func (s *Store) SetChannel(ch Channel) error {
	return s.update(func(r *Record) { r.Channel = ch })
}

// SetAuto は新しい版を自動で置き換えるかを切り替える（デスクトップ版のメニューの切り替え。既定は false）。
func (s *Store) SetAuto(on bool) error {
	return s.update(func(r *Record) { r.Auto = on })
}

// Save は確認の結果を控える（成功した結果は LastOK にも残す）。
func (s *Store) Save(res Result) error {
	return s.update(func(r *Record) {
		r.Last = &res
		if res.Status == StatusUpToDate || res.Status == StatusAvailable {
			r.LastOK = &res
		}
	})
}

// Runner は起動時と Interval ごとに確かめ、結果を控えて OnResult に渡す。
type Runner struct {
	Checker  *Checker
	Store    *Store
	OnResult func(Result) // 任意（知らせの表示の差し替えなど）
	Interval time.Duration

	once sync.Once
	wake chan struct{}
}

func (r *Runner) init() {
	r.once.Do(func() { r.wake = make(chan struct{}, 1) })
}

// Wake は次の間隔を待たずに確かめ直す（メニューで確認を入れ直したときなど）。すでに待ちがあれば何もしない。
func (r *Runner) Wake() {
	r.init()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run は ctx が終わるまで確かめ続ける（呼ぶ側が goroutine で動かす）。設定は毎回控えのファイルと環境変数から読み直すので、
// メニューで止めれば次の回から通信しない。
func (r *Runner) Run(ctx context.Context) {
	r.init()
	iv := r.Interval
	if iv <= 0 {
		iv = Interval
	}
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.wake:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
		}
		res := r.Checker.Check(ctx, r.Store.Load().Prefs)
		if ctx.Err() != nil {
			return
		}
		_ = r.Store.Save(res) // 控えに書けなくても確認は続ける（結果は OnResult で渡る）
		if r.OnResult != nil {
			r.OnResult(res)
		}
		t.Reset(iv)
	}
}
