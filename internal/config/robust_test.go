package config

import (
	"strings"
	"testing"
)

func TestUnknownKeyNamesTheKeyAndTheLikelyCause(t *testing.T) {
	b := strings.Replace(string(Default), `"version": 1,`, `"version": 1, "future_knob": true,`, 1)
	if b == string(Default) {
		t.Fatal("test setup: could not inject a key")
	}
	_, err := Parse([]byte(b), "/Users/x")
	if err == nil {
		t.Fatal("unknown keys must still fail loudly")
	}
	for _, want := range []string{`unknown key "future_knob"`, "older than the config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestDegradedIsDefaultWithAutoActOff(t *testing.T) {
	cfg, err := Degraded("/Users/x")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Policy.Daemon.AutoAct {
		t.Error("degraded mode must never act")
	}
	if cfg.Policy.MinFreeGB <= 0 || cfg.Policy.WarnFreeGB < cfg.Policy.MinFreeGB {
		t.Errorf("degraded thresholds come from the default: %+v", cfg.Policy)
	}
}
