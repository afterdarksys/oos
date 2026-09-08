//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestSystemAgentUnits(t *testing.T) {
	files := agentUnits("/etc/systemd/system", "/usr/local/bin/oos", true)
	svc := files["/etc/systemd/system/oos.service"]
	if !strings.Contains(svc, "--agent-tick") || !strings.Contains(svc, "ProtectSystem=strict") {
		t.Errorf("system service missing hardening or tick: %s", svc)
	}
	user := agentUnits("/home/x/.config/systemd/user", "/usr/local/bin/oos", false)
	if strings.Contains(user["/home/x/.config/systemd/user/oos.service"], "ProtectSystem") {
		t.Error("user units must not carry the root hardening block")
	}
}
