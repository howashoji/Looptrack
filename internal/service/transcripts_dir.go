package service

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/store"
)

// 会話記録の本体の置き場（docs/server/DESIGN.md §2-5「置き場」）。本体の形と書き込みは添付と同じ部品（blobstore.go）で、
// ルートだけを添付と分ける。添付の整合の検査（repair-attachments --apply）は添付の表から指されない本体を消すので、
// 同じルートに会話記録の本体を置くと消してしまう（逆も同じ）。ルートの一方がもう一方の中にある設定では、会話記録だけを使えなくする。

// TranscriptDirName は置き場を既定で決めるときのディレクトリ名（$STATE_DIRECTORY・SQLite の DB の隣・デスクトップの DataDir の下）。
const TranscriptDirName = "transcripts"

// TranscriptDirFromEnv は会話記録の置き場を環境変数から決める（AttachDirFromEnv と同じ順の別の関数）。
// 明示の LOOPTRACK_TRANSCRIPT_DIR → $STATE_DIRECTORY/transcripts → SQLite の DB と同じディレクトリの transcripts。
// どれも無ければ空（会話記録は使えない）。
func TranscriptDirFromEnv(getenv func(string) string) string {
	if d := getenv("LOOPTRACK_TRANSCRIPT_DIR"); strings.TrimSpace(d) != "" {
		return d
	}
	if ds := filepath.SplitList(getenv("STATE_DIRECTORY")); len(ds) > 0 && strings.TrimSpace(ds[0]) != "" {
		return filepath.Join(ds[0], TranscriptDirName)
	}
	if p, ok := store.SQLitePath(getenv("LOOPTRACK_DSN")); ok && p != "" && p != ":memory:" {
		return filepath.Join(filepath.Dir(p), TranscriptDirName)
	}
	return ""
}

// BlobRootsNested は 2 つの置き場の一方がもう一方の中にある（同じ場所も含む）かを返す。どちらかが空なら false。
// 書いたままの形と、シンボリックリンクを解いた形（まだ無い末尾は解ける親に足す）の両方で比べる。
// 大文字と小文字は区別しない（macOS と Windows のファイルシステムは区別しない。区別する OS で別の場所を入れ子と見ても、
// 会話記録が使えなくなる側に倒れるだけで、本体を消す側には倒れない）。
func BlobRootsNested(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	as, bs := rootForms(a), rootForms(b)
	for _, x := range as {
		for _, y := range bs {
			if pathWithin(x, y) || pathWithin(y, x) {
				return true
			}
		}
	}
	return false
}

// rootForms は置き場の比べる形（絶対パスにして整えた形と、シンボリックリンクを解いた形）を返す。
func rootForms(p string) []string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	forms := []string{abs}
	// まだ無い末尾は、解ける親まで遡って解き、残りを足し直す
	rest := ""
	for dir := abs; ; {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			if real := filepath.Join(r, rest); real != abs {
				forms = append(forms, real)
			}
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
	return forms
}

// pathWithin は child が parent と同じか、その中にあるかを返す（大文字と小文字は区別しない）。
func pathWithin(child, parent string) bool {
	rel, err := filepath.Rel(strings.ToLower(parent), strings.ToLower(child))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// errTranscriptUnavailable は置き場が無い（または添付の置き場と入れ子の）ときの拒否（ほかの操作は動く）。
func errTranscriptUnavailable() *Error {
	return errm(Rejected, "transcripts_unavailable", i18n.M("service.err.transcript.unavailable"))
}

// transcriptRoot は会話記録の本体の置き場を返す。空か、添付の置き場と入れ子なら「置き場が設定されていない」を返す
// （会話記録の操作はすべてここを通す。サーバの起動時にも同じ判定で置き場を外してログに残すが、ここでも確かめる）。
func (s *Service) transcriptRoot() (string, error) {
	if s.TranscriptDir == "" || BlobRootsNested(s.AttachDir, s.TranscriptDir) {
		return "", errTranscriptUnavailable()
	}
	return s.TranscriptDir, nil
}

// TranscriptsAvailable は会話記録の置き場が使えるか（置き場があり、添付の置き場と入れ子でない）を返す。
func (s *Service) TranscriptsAvailable() bool {
	_, err := s.transcriptRoot()
	return err == nil
}

// TranscriptBodyPath は会話記録の本体のパス（<dir>/ab/cd/<sha256>。添付と同じ形）。
func TranscriptBodyPath(dir, sum string) string { return blobBodyPath(dir, sum) }

// TranscriptCheck は会話記録の DB と置き場の突き合わせの結果。
type TranscriptCheck struct {
	Missing []store.LiveTranscriptChunkRef // 消去していないのに本体が置き場に無いチャンク
	Orphans []OrphanBody                   // どこからも指されない本体
}

// CheckTranscripts は会話記録の DB と置き場を突き合わせる（何も変えない。CheckAttachments と同じ形）。
// 本体の欠けと、どこからも（消去していないチャンクからも添付からも）指されない本体を返す。
func (s *Service) CheckTranscripts(ctx context.Context) (*TranscriptCheck, error) {
	root, err := s.transcriptRoot()
	if err != nil {
		return nil, err
	}
	live, err := store.LiveTranscriptChunkRefs(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	res := &TranscriptCheck{}
	referenced := map[string]bool{}
	exists := map[string]bool{}
	for _, c := range live {
		if !referenced[c.SHA256] {
			referenced[c.SHA256] = true
			_, err := os.Stat(TranscriptBodyPath(root, c.SHA256))
			exists[c.SHA256] = err == nil
		}
		if !exists[c.SHA256] {
			res.Missing = append(res.Missing, c)
		}
	}
	now := time.Now()
	err = walkBlobs(root, func(path, sum string, info fs.FileInfo) error {
		if referenced[sum] {
			return nil
		}
		// 添付が指す本体は会話記録の側からは数えない（置き場を取り違えた設定でも消さない）
		if n, err := store.LiveAttachmentCount(ctx, s.DB, sum); err != nil || n > 0 {
			return err
		}
		res.Orphans = append(res.Orphans, OrphanBody{Path: path, SHA256: sum, ModTime: info.ModTime(), Recent: now.Sub(info.ModTime()) < OrphanGrace})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// TranscriptLeftovers は、途中で止まった削除と追記が会話記録の置き場の .tmp に残したものの数え。
type TranscriptLeftovers = blobLeftovers

// RecoverTranscriptLeftovers は、途中で止まった削除（removeBlobGuarded が本体を .tmp へ外した後に落ちた）が残した本体と、
// 落ちた追記が残した古い印を数え、apply なら片付ける（外した本体は本体のパスが空いていれば戻し、空いていなければ捨てる。
// 古い印は消す）。CheckTranscripts より先に呼ぶ（戻した本体を、指す行が無ければふつうの規則で消すため）。
func (s *Service) RecoverTranscriptLeftovers(ctx context.Context, apply bool) (TranscriptLeftovers, error) {
	root, err := s.transcriptRoot()
	if err != nil {
		return TranscriptLeftovers{}, err
	}
	return recoverBlobLeftovers(root, apply, s.transcriptBodies.lock)
}

// RemoveTranscriptOrphans は CheckTranscripts が見つけた、どこからも指されない本体を消す（Recent のものは消さない）。
// 消す直前に、その本体を指す消去していないチャンクも添付も無いこと・触れられて OrphanGrace が経っていること・
// 別のプロセスで同じ本体を置いて記録する前の追記が無いことを確かめ直す（removeBlobGuarded。消去と同じやり方）。
// 本体の欠けは直さない。消したパスを返す。
func (s *Service) RemoveTranscriptOrphans(ctx context.Context, c *TranscriptCheck) ([]string, error) {
	root, err := s.transcriptRoot()
	if err != nil {
		return nil, err
	}
	return removeOrphanBlobs(root, repairTranscriptsCmd, c.Orphans, s.transcriptBodies.lock, func(sum string) (int, error) { return s.transcriptBodyRefs(ctx, sum) })
}
