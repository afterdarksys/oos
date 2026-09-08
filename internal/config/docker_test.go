package config

import (
	"strings"
	"testing"
)

func TestConfigDockerFields(t *testing.T) {
	home := t.TempDir()
	base := `{"version":1,"volume":"~","policy":{"min_free_gb":1,"warn_free_gb":2,"require_yes":true,"max_delete_gb_per_run":1,
	  "min_path_depth":3,"log_file":"~/l","state_file":"~/s","big_file_min_mb":1,"scan_top_n":1,"docker":false,"docker_timeout_seconds":%d},
	  "known_dirs":[],"known_files":[]}`
	cfg, err := Parse([]byte(strings.Replace(base, "%d", "30", 1)), home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Policy.Docker == nil || *cfg.Policy.Docker || cfg.Policy.DockerTimeoutSeconds != 30 {
		t.Errorf("docker fields not read: %+v", cfg.Policy)
	}
	if _, err := Parse([]byte(strings.Replace(base, "%d", "-1", 1)), home); err == nil || !strings.Contains(err.Error(), "docker_timeout_seconds") {
		t.Errorf("negative timeout must be rejected: %v", err)
	}
}
