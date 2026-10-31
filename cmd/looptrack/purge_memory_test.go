package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/howashoji/looptrack/internal/i18n"
	"github.com/howashoji/looptrack/internal/service"
	"github.com/howashoji/looptrack/internal/store"
	"github.com/howashoji/looptrack/internal/testutil"
)

// looptrack purge-memory: --yes が無ければ数と大きさを報告するだけで何も変えず、--yes で消す。報告に本文は出ない。
func TestPurgeMemoryCmd(t *testing.T) {
	db := testutil.MigratedDB(t)
	ctx := context.Background()
	pid, err := store.CreateProject(ctx, db, store.Project{Slug: "pm", Prefix: "PM", Width: 4, Name: "pm"})
	if err != nil {
		t.Fatal(err)
	}
	uid, err := store.CreateUser(ctx, db, "ed", "ed", "x", "member")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetMember(ctx, db, pid, uid, "editor"); err != nil {
		t.Fatal(err)
	}
	svc := service.New(db, nil)
	if _, err := svc.WriteMemory(ctx, service.Actor{UserID: uid, Via: "cli", Lang: i18n.JA},
		service.MemoryWriteInput{MemoryKey: service.MemoryKey{Project: "pm", Kind: "handoff"}, Op: "append", Content: "合成の本文"}); err != nil {
		t.Fatal(err)
	}
	bodies := func() (n int) {
		if err := db.QueryRow("SELECT COUNT(*) FROM memory_bodies").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	target := service.OperatorMemoryTarget{Project: "pm", Kind: "handoff", MemoryPurgeSelect: service.MemoryPurgeSelect{AllVersions: true}}
	var out bytes.Buffer
	if err := purgeMemory(ctx, db, i18n.JA, &out, target, "", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "本体 1 件") || strings.Contains(out.String(), "合成の本文") || bodies() != 1 {
		t.Fatalf("dry run: %q（本体 %d）", out.String(), bodies())
	}
	if err := purgeMemory(ctx, db, i18n.JA, &out, target, "", true); err == nil || bodies() != 1 {
		t.Fatalf("理由なしの --yes: %v（本体 %d）", err, bodies())
	}
	out.Reset()
	if err := purgeMemory(ctx, db, i18n.JA, &out, target, "秘密が混ざった", true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 件") || !strings.Contains(out.String(), "消した印") || bodies() != 0 {
		t.Fatalf("--yes: %q（本体 %d）", out.String(), bodies())
	}
	var cause, via string
	if err := db.QueryRow("SELECT cause, via FROM memory_purges").Scan(&cause, &via); err != nil || cause != "operator" || via != "admin" {
		t.Errorf("記録: %s %s %v", cause, via, err)
	}
}
