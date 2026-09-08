package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAttributeDeepAndMixedLabel(t *testing.T) {
	home := t.TempDir()
	cacheDir := filepath.Join(home, ".cache")
	write(t, filepath.Join(cacheDir, "uv", "archive", "env", "f"), 3<<20)
	write(t, filepath.Join(cacheDir, "pip", "http", "f"), 1<<20)
	write(t, filepath.Join(cacheDir, "mystery", "f"), 1<<20)
	cfg := &Config{Policy: Policy{Owners: []Owner{
		{Match: filepath.Join(cacheDir, "uv"), UseCase: "uv"},
		{Match: filepath.Join(cacheDir, "pip"), UseCase: "pip"},
	}}}
	if l, _ := cfg.useCaseFor(cacheDir); l != "" {
		t.Fatalf("top level should be unattributed, got %q", l)
	}
	b := attributeDeep(cfg, cacheDir, 2, time.Now())
	got := map[string]int64{}
	for _, x := range b {
		got[x.UseCase] = x.Bytes
	}
	if got["uv"] < 3<<20 || got["pip"] < 1<<20 || got["unattributed"] < 1<<20 {
		t.Errorf("breakdown = %+v", b)
	}
	if b[0].UseCase != "uv" {
		t.Errorf("largest first, got %s", b[0].UseCase)
	}
	label := mixedLabel(b)
	if !strings.HasPrefix(label, "mixed:") || !strings.Contains(label, "% uv") {
		t.Errorf("label = %q", label)
	}
	if mixedLabel([]useCaseTotal{{UseCase: "only", Bytes: 5}}) != "only" {
		t.Error("single labelled component is not mixed")
	}
	if mixedLabel(nil) != "" {
		t.Error("empty breakdown has no label")
	}
}

func TestAuditUsesBreakdownForUnattributedDirs(t *testing.T) {
	home := t.TempDir()
	cacheDir := filepath.Join(home, ".cache")
	write(t, filepath.Join(cacheDir, "uv", "f"), 4<<20)
	write(t, filepath.Join(cacheDir, "other", "f"), 1<<20)
	p := policyFor(home)
	p.Owners = []Owner{{Match: filepath.Join(cacheDir, "uv"), UseCase: "uv"}}
	cfg := &Config{Version: 1, Volume: home, Policy: p, home: home}
	rows, err := auditHome(cfg, home, 1<<20, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var row *AuditRow
	for i := range rows {
		if rows[i].Path == cacheDir {
			row = &rows[i]
		}
	}
	if row == nil {
		t.Fatal(".cache row missing")
	}
	if len(row.Breakdown) == 0 || !strings.HasPrefix(row.UseCase, "mixed:") {
		t.Errorf("expected a mixed breakdown, got use_case=%q breakdown=%+v", row.UseCase, row.Breakdown)
	}
	totals := groupByUseCaseDeep(cfg, rows)
	var uv int64
	for _, x := range totals {
		if x.UseCase == "uv" {
			uv = x.Bytes
		}
	}
	if uv < 4<<20 {
		t.Errorf("deep grouping should credit uv with its bytes: %+v", totals)
	}
}
