// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !windows

// Non-Windows stubs for the grantee-side auto-mount platform seam. Drive-letter
// mounting is a Windows concept, so nodeReconcileMounts short-circuits on
// nodeMountsSupported (false here) and none of these run in practice — they
// exist only so foldershare.go's cross-platform default var bodies and the
// nodemode.go wire-up compile everywhere.
package main

import (
	"fmt"
	"os/exec"
	"strings"
)

func nodeCurrentMountSource() nodeMountTokenSource { return nodeMountTokenCurrent }

func nodeMountEnv() (userIsolated, linkedConnEffective bool) { return false, false }

func nodeEnsureLinkedConnections() {}

func nodeEnsureWebClient() {}

func nodeRunNetUseVia(_ nodeMountTokenSource, args []string) (nodeMountTokenSource, string, error) {
	c := exec.Command("net", args...)
	out, err := c.CombinedOutput()
	if err != nil {
		return nodeMountTokenCurrent, string(out), fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nodeMountTokenCurrent, string(out), nil
}
