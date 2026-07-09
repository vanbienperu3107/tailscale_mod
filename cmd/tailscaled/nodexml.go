// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"encoding/xml"
	"log"
	"os"
	"path/filepath"
)

// node.xml — per-machine enrollment config, read from the SAME directory as the
// exe (override with TS_NODE_CONFIG). Shape:
//
//	<node>
//	  <autologin>true</autologin>
//	  <apiCenter>https://vpn2.hangocthanh.io.vn/app</apiCenter>
//	  <deviceToken>...</deviceToken>   <!-- written by the client, first enroll -->
//	</node>
//
// Missing file ⇒ a default (autologin=false) one is written next to the exe;
// if that write fails (read-only dir) we simply run with autologin=false.
//
// IMPORTANT: the machine's IDENTITY does not depend on this file — the machine
// key is seeded from the hardware serial (see hwid.go). Losing node.xml costs
// only the cached deviceToken (admin can reset it), never the node/IP.
//
// deviceToken is the ONLY sensitive value ever written here. The auth key
// (short-lived, used immediately) and the hardware serial (it reproduces the
// private machine key) are NEVER persisted to this file.
type nodeXMLConfig struct {
	XMLName xml.Name `xml:"node"`

	// Autologin enables zero-touch enrollment: the client reports itself to the
	// api-center and, once an admin approves it, joins the tailnet unattended.
	// Absent element ⇒ false (manual/interactive login, the legacy flow).
	Autologin bool `xml:"autologin"`

	// APICenter is the dashboard base URL that serves /api/internal/enroll.
	// Empty ⇒ fall back to the build-time nodeMetricsURL.
	APICenter string `xml:"apiCenter"`

	// DeviceToken is issued by the api-center on this device's FIRST successful
	// enroll (first-enroll-wins) and proves identity on later re-enrolls (e.g.
	// after a state wipe). Empty until then.
	DeviceToken string `xml:"deviceToken,omitempty"`
}

// nodeConfigPath returns the node.xml path: TS_NODE_CONFIG when set, otherwise
// node.xml next to the exe.
func nodeConfigPath(exeDir string) string {
	if p := os.Getenv("TS_NODE_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(exeDir, "node.xml")
}

// nodeParseXML parses node.xml content, applying defaults. A parse error is
// reported to the caller; callers treat it as "run with autologin=false" rather
// than failing the node, so a corrupt config can never strand a machine offline.
func nodeParseXML(b []byte, defaultAPICenter string) (nodeXMLConfig, error) {
	var cfg nodeXMLConfig
	if err := xml.Unmarshal(b, &cfg); err != nil {
		return nodeXMLConfig{APICenter: defaultAPICenter}, err
	}
	if cfg.APICenter == "" {
		cfg.APICenter = defaultAPICenter
	}
	return cfg, nil
}

// nodeMarshalXML renders cfg as a standalone node.xml document.
func nodeMarshalXML(cfg nodeXMLConfig) ([]byte, error) {
	out, err := xml.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	body := make([]byte, 0, len(xml.Header)+len(out)+1)
	body = append(body, xml.Header...)
	body = append(body, out...)
	body = append(body, '\n')
	return body, nil
}

// nodeWriteXML writes cfg to path with 0600 perms (it may carry deviceToken).
func nodeWriteXML(path string, cfg nodeXMLConfig) error {
	body, err := nodeMarshalXML(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o600)
}

// nodeLoadOrCreateXML reads node.xml at path, creating a default
// (autologin=false) file when absent. Never fails: every problem degrades to
// "autologin=false" and is logged, because enrollment is an optional
// convenience — a machine must still be able to run and be logged in by hand.
func nodeLoadOrCreateXML(path, defaultAPICenter string) nodeXMLConfig {
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		cfg, perr := nodeParseXML(b, defaultAPICenter)
		if perr != nil {
			log.Printf("node: config %s: parse error (%v); running with autologin=false", path, perr)
			return cfg
		}
		return cfg
	case !os.IsNotExist(err):
		log.Printf("node: config %s: read error (%v); running with autologin=false", path, err)
		return nodeXMLConfig{APICenter: defaultAPICenter}
	}

	cfg := nodeXMLConfig{Autologin: false, APICenter: defaultAPICenter}
	if werr := nodeWriteXML(path, cfg); werr != nil {
		// Read-only directory (e.g. exe on a share): keep going, autologin off.
		log.Printf("node: config: could not create default %s (%v); running with autologin=false", path, werr)
	} else {
		log.Printf("node: config: created default %s (autologin=false)", path)
	}
	return cfg
}

// nodePersistDeviceToken stores the device token issued on first enroll, leaving
// every other field of cfg intact. Best-effort: a write failure only means the
// device must be re-approved (or its token reset) if its state is ever wiped.
func nodePersistDeviceToken(path string, cfg nodeXMLConfig, token string) error {
	cfg.DeviceToken = token
	return nodeWriteXML(path, cfg)
}
