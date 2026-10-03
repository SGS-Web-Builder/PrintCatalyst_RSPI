package config

import (
	"path/filepath"
	"testing"
)

func TestLANOriginPreservesHTTP(t *testing.T) {
	dir := t.TempDir()
	v := validEnvironment()
	v["PC_DATA_DIR"] = dir
	v["PC_DATABASE_PATH"] = filepath.Join(dir, "test.db")
	v["PC_BIND_HOST"] = "0.0.0.0"
	v["PC_PUBLIC_ORIGIN"] = "http://192.168.1.10:8080/"
	cfg, err := Load(environment(v), dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicOrigin != "http://192.168.1.10:8080" {
		t.Fatalf("rewritten origin: %s", cfg.PublicOrigin)
	}
	for _, bad := range []string{"http://user:password@192.168.1.10:8080", "http://192.168.1.10/portal/", "http://192.168.1.10?x=1", "http://192.168.1.10:99999"} {
		v["PC_PUBLIC_ORIGIN"] = bad
		if _, err := Load(environment(v), dir); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
