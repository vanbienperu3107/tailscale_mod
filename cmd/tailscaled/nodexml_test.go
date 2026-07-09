// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testAPICenter = "https://example.test/app"

func TestNodeXMLParse(t *testing.T) {
	t.Run("full document", func(t *testing.T) {
		cfg, err := nodeParseXML([]byte(`<node>
  <autologin>true</autologin>
  <apiCenter>https://dash.test/app</apiCenter>
  <deviceToken>dt123</deviceToken>
</node>`), testAPICenter)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !cfg.Autologin {
			t.Error("autologin should be true")
		}
		if cfg.APICenter != "https://dash.test/app" {
			t.Errorf("apiCenter = %q", cfg.APICenter)
		}
		if cfg.DeviceToken != "dt123" {
			t.Errorf("deviceToken = %q", cfg.DeviceToken)
		}
	})

	t.Run("missing autologin defaults to false", func(t *testing.T) {
		cfg, err := nodeParseXML([]byte(`<node><apiCenter>https://d.test</apiCenter></node>`), testAPICenter)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if cfg.Autologin {
			t.Error("absent <autologin> must default to false (manual login)")
		}
	})

	t.Run("empty apiCenter falls back to default", func(t *testing.T) {
		cfg, err := nodeParseXML([]byte(`<node><autologin>true</autologin></node>`), testAPICenter)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if cfg.APICenter != testAPICenter {
			t.Errorf("apiCenter = %q, want fallback %q", cfg.APICenter, testAPICenter)
		}
	})

	t.Run("malformed xml errors and yields autologin=false", func(t *testing.T) {
		cfg, err := nodeParseXML([]byte(`<node><autologin>true`), testAPICenter)
		if err == nil {
			t.Fatal("expected a parse error")
		}
		if cfg.Autologin {
			t.Error("a corrupt config must never enable autologin")
		}
		if cfg.APICenter != testAPICenter {
			t.Errorf("apiCenter = %q, want fallback", cfg.APICenter)
		}
	})
}

func TestNodeXMLLoadOrCreate(t *testing.T) {
	t.Run("creates default when absent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "node.xml")
		cfg := nodeLoadOrCreateXML(path, testAPICenter)
		if cfg.Autologin {
			t.Error("generated default must be autologin=false")
		}
		if cfg.APICenter != testAPICenter {
			t.Errorf("apiCenter = %q", cfg.APICenter)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("default file not written: %v", err)
		}
		if !strings.Contains(string(b), "<autologin>false</autologin>") {
			t.Errorf("generated file lacks autologin=false:\n%s", b)
		}
	})

	t.Run("reads an existing file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "node.xml")
		if err := os.WriteFile(path, []byte(`<node><autologin>true</autologin></node>`), 0o600); err != nil {
			t.Fatal(err)
		}
		if cfg := nodeLoadOrCreateXML(path, testAPICenter); !cfg.Autologin {
			t.Error("existing autologin=true not honored")
		}
	})

	t.Run("unusable path degrades to autologin=false", func(t *testing.T) {
		// A path nested under a regular file can be neither read nor created —
		// it stands in for a read-only exe directory. Depending on the OS this
		// surfaces as a read error (ENOTDIR) or a create failure; BOTH paths must
		// end at autologin=false, and neither may panic. Enrollment is optional,
		// so a config we cannot touch must never strand the machine.
		dir := t.TempDir()
		blocker := filepath.Join(dir, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := nodeLoadOrCreateXML(filepath.Join(blocker, "node.xml"), testAPICenter)
		if cfg.Autologin {
			t.Error("must stay autologin=false when the config cannot be read or created")
		}
		if cfg.APICenter != testAPICenter {
			t.Errorf("apiCenter = %q, want fallback %q", cfg.APICenter, testAPICenter)
		}
	})
}

func TestNodeXMLPersistDeviceToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.xml")
	cfg := nodeXMLConfig{Autologin: true, APICenter: testAPICenter}
	if err := nodePersistDeviceToken(path, cfg, "dt456"); err != nil {
		t.Fatalf("persist: %v", err)
	}

	// Round-trips: the token is saved, and the other fields survive untouched.
	got := nodeLoadOrCreateXML(path, "https://other.test")
	if got.DeviceToken != "dt456" {
		t.Errorf("deviceToken = %q, want dt456", got.DeviceToken)
	}
	if !got.Autologin {
		t.Error("autologin lost on persist")
	}
	if got.APICenter != testAPICenter {
		t.Errorf("apiCenter = %q, want %q", got.APICenter, testAPICenter)
	}

	// The file must NEVER carry the auth key or the hardware serial: the serial
	// reproduces the private machine key, and the auth key is single-use.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"authKey", "auth-key", "serial", "salt"} {
		if strings.Contains(strings.ToLower(string(b)), strings.ToLower(forbidden)) {
			t.Errorf("node.xml must not contain %q:\n%s", forbidden, b)
		}
	}
}
