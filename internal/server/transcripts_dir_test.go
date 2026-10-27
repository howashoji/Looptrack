package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/howashoji/looptrack/internal/store"
)

// syncBuffer はサーバのログを並行に書かれても読めるように受ける。
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// 受け入れ条件: 会話記録の置き場が添付の置き場と入れ子なら、サーバは起動時に会話記録を使えなくして理由をログに残し、添付は動く。
// 対照: 別の場所なら会話記録は使え、ログも残さない。
func TestServerDisablesNestedTranscriptDir(t *testing.T) {
	for _, c := range []struct {
		name   string
		nested bool
		trDir  func(attach string) string
	}{
		{"会話記録が添付の中", true, func(a string) string { return filepath.Join(a, "transcripts") }},
		{"添付が会話記録の中", true, func(a string) string { return filepath.Dir(a) }},
		{"別の場所（対照）", false, func(a string) string { return filepath.Join(filepath.Dir(a), "transcripts") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			attach := filepath.Join(t.TempDir(), "attachments")
			tr := c.trDir(attach)
			var logs syncBuffer
			e := newEnvWith(t, func(cfg *Config) {
				cfg.AllowNoAdmin = true
				cfg.AttachDir, cfg.TranscriptDir = attach, tr
				cfg.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			})
			if got := e.s.svc.TranscriptsAvailable(); got == c.nested {
				t.Errorf("TranscriptsAvailable = %v（入れ子 %v）", got, c.nested)
			}
			logged := strings.Contains(logs.String(), "transcripts disabled")
			if logged != c.nested || c.nested && !strings.Contains(logs.String(), tr) {
				t.Errorf("起動時のログ（入れ子 %v）:\n%s", c.nested, logs.String())
			}
			if c.nested && e.s.svc.TranscriptDir != "" {
				t.Errorf("入れ子なのに会話記録の置き場が残った: %q", e.s.svc.TranscriptDir)
			}
			// 添付は動く（API で添付し、本体が添付の置き場に置かれる）
			p := e.project("tr-dir")
			u := e.user("tr-ed", "tr-ed-password-1", "member")
			if err := store.SetMember(context.Background(), e.db, p.ID, u.ID, "editor"); err != nil {
				t.Fatal(err)
			}
			api := e.apiAs(u)
			var created struct {
				Issue issueDetailJSON `json:"issue"`
			}
			api.json(201, "POST", "/projects/"+p.Slug+"/issues", map[string]any{"title": "x"}, &created)
			req, err := http.NewRequest("POST", e.srv.URL+"/im/api/v1/issues/"+created.Issue.ID+"/attachments", strings.NewReader("evidence"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+api.token)
			req.Header.Set("X-Looptrack-Filename", "log.txt")
			req.Header.Set("Content-Type", "text/plain")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != 201 {
				t.Errorf("添付の POST = %d, want 201", res.StatusCode)
			}
			if c, err := e.s.svc.CheckAttachments(context.Background()); err != nil || len(c.Missing) != 0 || len(c.Orphans) != 0 {
				t.Errorf("添付の置き場: %+v %v", c, err)
			}
		})
	}
}
