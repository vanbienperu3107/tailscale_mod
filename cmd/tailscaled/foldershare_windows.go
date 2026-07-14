// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build windows

// Windows backend for the grantee-side Taildrive auto-mount (see foldershare.go).
//
// The node daemon runs ELEVATED (requireAdministrator manifest), so a plain
// `net use` maps the drive in the admin logon token's namespace — invisible to
// the interactive non-elevated Explorer (UAC "linked connections" isolation:
// the "net use shows Z: but Explorer doesn't" bug). To make the drive appear
// immediately, without a reboot and regardless of whether `<exe> install` ever
// ran, we run `net use` in the USER's session instead:
//
//   - Linked (normal case): the elevated token has a non-elevated linked
//     sibling in the SAME logon session as Explorer. We duplicate it to a
//     primary token and launch net.exe under it with CreateProcessWithTokenW
//     (needs only SeImpersonatePrivilege, which elevated admins hold — unlike
//     CreateProcessAsUser, which needs SeAssignPrimaryTokenPrivilege that a
//     non-SYSTEM admin lacks).
//   - ActiveSession: if we were ever run as SYSTEM, reach the console user's
//     token via WTSQueryUserToken.
//   - Current: fall back to in-process net use (already non-elevated, or token
//     acquisition failed) — the reported status then flags it as maybe-hidden.
//
// Fail-open throughout: any token/spawn failure degrades to in-process net use
// and is retried on the next 20s poll, never fatal.
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procCreateProcessWithTokenW      = syscall.NewLazyDLL("advapi32.dll").NewProc("CreateProcessWithTokenW")
	procWTSQueryUserToken            = syscall.NewLazyDLL("wtsapi32.dll").NewProc("WTSQueryUserToken")
	procWTSGetActiveConsoleSessionId = syscall.NewLazyDLL("kernel32.dll").NewProc("WTSGetActiveConsoleSessionId")
)

// nodeCurrentTokenEnv observes this process's security context for
// nodeSelectMountTokenSource / honest status. Best-effort: a failed probe just
// leaves that field false.
func nodeCurrentTokenEnv() nodeTokenEnv {
	var e nodeTokenEnv
	t := windows.GetCurrentProcessToken() // pseudo handle, no Close needed
	e.Elevated = t.IsElevated()
	if linked, err := t.GetLinkedToken(); err == nil {
		e.HaveLinkedToken = true
		e.LinkedElevated = linked.IsElevated()
		linked.Close()
	}
	if u, err := t.GetTokenUser(); err == nil && u.User.Sid != nil {
		e.IsSystem = u.User.Sid.String() == "S-1-5-18" // LocalSystem
	}
	e.HaveConsoleUser = nodeWTSGetActiveConsoleSessionId() != 0xFFFFFFFF
	return e
}

// nodeCurrentMountSource decides which context to run `net use` in this pass.
// TS_MOUNT_TOKEN_SOURCE (current|linked|active) forces one, for verifying the
// degrade path on a real box.
func nodeCurrentMountSource() nodeMountTokenSource {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TS_MOUNT_TOKEN_SOURCE"))) {
	case "current":
		return nodeMountTokenCurrent
	case "linked":
		return nodeMountTokenLinked
	case "active", "activesession":
		return nodeMountTokenActiveSession
	}
	return nodeSelectMountTokenSource(nodeCurrentTokenEnv())
}

// nodeMountEnv reports (userIsolated, linkedConnEffective). We deliberately do
// NOT infer linkedConnEffective from the registry: EnableLinkedConnections set
// on this boot is not in force for the current session, so trusting a reg read
// would report a false green on the in-process fallback path. Conservative:
// only the Linked/ActiveSession paths are treated as visible.
func nodeMountEnv() (userIsolated, linkedConnEffective bool) {
	e := nodeCurrentTokenEnv()
	return e.Elevated && e.HaveLinkedToken && !e.LinkedElevated, false
}

// nodeRunNetUseVia runs `net <args...>` in the security context src and returns
// the combined output. Fail-open: if the user token can't be acquired it logs
// and degrades to an in-process net use (whose visibility the caller reports
// honestly).
func nodeRunNetUseVia(src nodeMountTokenSource, args []string) (string, error) {
	if src == nodeMountTokenCurrent {
		return nodeRunNetInProcess(args)
	}
	tok, closeTok, err := nodeInteractiveUserToken(src)
	if err != nil {
		log.Printf("node: folder-mount: %s token unavailable (%v); using in-process net use (may not be visible to user)", nodeTokenSourceName(src), err)
		return nodeRunNetInProcess(args)
	}
	defer closeTok()
	return nodeRunNetAsUserToken(tok, args)
}

func nodeRunNetInProcess(args []string) (string, error) {
	c := exec.Command(nodeNetExe(), args...)
	c.Env = append(os.Environ(), "TS_BE_CLI=1")
	out, err := c.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// nodeInteractiveUserToken returns a PRIMARY token to run net.exe under for the
// chosen source, plus a closer. The linked token from GetLinkedToken is an
// IMPERSONATION token, so it MUST be duplicated to a primary token (with
// TOKEN_ASSIGN_PRIMARY) before CreateProcessWithTokenW will accept it.
func nodeInteractiveUserToken(src nodeMountTokenSource) (windows.Token, func(), error) {
	switch src {
	case nodeMountTokenLinked:
		linked, err := windows.GetCurrentProcessToken().GetLinkedToken()
		if err != nil {
			return 0, nil, fmt.Errorf("GetLinkedToken: %w", err)
		}
		// desiredAccess 0 => duplicate with the same access the linked token
		// carries (matches util/winutil/s4u's proven idiom); TokenPrimary is
		// required because the linked token is an impersonation token and
		// CreateProcessWithTokenW needs a primary one.
		var primary windows.Token
		err = windows.DuplicateTokenEx(linked, 0, nil, windows.SecurityImpersonation, windows.TokenPrimary, &primary)
		linked.Close()
		if err != nil {
			return 0, nil, fmt.Errorf("DuplicateTokenEx: %w", err)
		}
		return primary, func() { primary.Close() }, nil
	case nodeMountTokenActiveSession:
		sess := nodeWTSGetActiveConsoleSessionId()
		if sess == 0xFFFFFFFF {
			return 0, nil, fmt.Errorf("no active console session")
		}
		var tok windows.Token
		if err := nodeWTSQueryUserToken(sess, &tok); err != nil {
			return 0, nil, fmt.Errorf("WTSQueryUserToken: %w", err)
		}
		return tok, func() { tok.Close() }, nil
	default:
		return 0, nil, fmt.Errorf("unsupported token source %v", src)
	}
}

// nodeRunNetAsUserToken runs `net <args...>` under tok. Listing (no user args)
// goes through cmd.exe redirection to a file BY PATH, so adoption gets reliable
// output without depending on CreateProcessWithTokenW handle inheritance;
// mount/unmount run net.exe directly (argv — no shell, so a share name can't
// inject) and rely on the exit code, with best-effort captured text.
func nodeRunNetAsUserToken(tok windows.Token, args []string) (string, error) {
	if len(args) == 1 && args[0] == "use" {
		return nodeRunListViaCmd(tok)
	}
	return nodeRunNetDirect(tok, args)
}

// nodeRunListViaCmd runs `cmd /c net use > <tmp> 2>&1` under tok. The command
// line is fixed (no user input), so the shell is safe here, and the child
// writes the file by path — no inherited-handle dependency.
func nodeRunListViaCmd(tok windows.Token) (string, error) {
	tmp, err := os.CreateTemp("", "tsmount-*.out")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	cmdExe := filepath.Join(nodeSystemDir(), "cmd.exe")
	cmdLine := fmt.Sprintf(`%s /c net use > "%s" 2>&1`, syscall.EscapeArg(cmdExe), tmpPath)
	if _, err := nodeCreateProcessWithToken(tok, cmdExe, cmdLine, nil); err != nil {
		return "", err
	}
	out, _ := os.ReadFile(tmpPath)
	return string(out), nil
}

// nodeRunNetDirect runs net.exe directly under tok (no shell). stdout/stderr go
// to an inheritable temp file for best-effort error text; correctness rests on
// the exit code, so a build where CreateProcessWithTokenW doesn't inherit the
// handle still mounts/unmounts correctly (just without the "System error NN"
// text — adoption already guards the already-mapped case).
func nodeRunNetDirect(tok windows.Token, args []string) (string, error) {
	tmp, err := os.CreateTemp("", "tsmount-*.out")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	p16, err := windows.UTF16PtrFromString(tmpPath)
	if err != nil {
		return "", err
	}
	sa := &windows.SecurityAttributes{InheritHandle: 1}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	fh, err := windows.CreateFile(p16, windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, sa,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_TEMPORARY, 0)
	if err != nil {
		return "", fmt.Errorf("open capture file: %w", err)
	}
	defer windows.CloseHandle(fh)

	netExe := nodeNetExe()
	cmdLine := nodeComposeCmdLine(append([]string{netExe}, args...))
	code, err := nodeCreateProcessWithToken(tok, netExe, cmdLine, &fh)
	if err != nil {
		return "", err
	}
	out, _ := os.ReadFile(tmpPath)
	if code != 0 {
		return string(out), fmt.Errorf("net %s exited %d: %s", strings.Join(args, " "), code, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// nodeCreateProcessWithToken launches appName/cmdLine under tok via
// CreateProcessWithTokenW, waits for it, and returns the exit code. If stdHandle
// is non-nil it's wired as stdout+stderr (STARTF_USESTDHANDLES); lpDesktop is
// pinned to winsta0\default so WNetAddConnection2's SHCNE_DRIVEADD broadcast
// reaches the user's Explorer (auto-refreshing This PC).
func nodeCreateProcessWithToken(tok windows.Token, appName, cmdLine string, stdHandle *windows.Handle) (uint32, error) {
	appName16, err := windows.UTF16PtrFromString(appName)
	if err != nil {
		return 1, err
	}
	cmdLine16, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		return 1, err
	}
	desktop16, err := windows.UTF16PtrFromString(`winsta0\default`)
	if err != nil {
		return 1, err
	}

	si := windows.StartupInfo{
		Cb:         uint32(unsafe.Sizeof(windows.StartupInfo{})),
		Desktop:    desktop16,
		Flags:      windows.STARTF_USESHOWWINDOW,
		ShowWindow: windows.SW_HIDE,
	}
	if stdHandle != nil {
		si.Flags |= windows.STARTF_USESTDHANDLES
		si.StdOutput = *stdHandle
		si.StdErr = *stdHandle
	}
	var pi windows.ProcessInformation

	r1, _, e := procCreateProcessWithTokenW.Call(
		uintptr(tok),
		0, // dwLogonFlags
		uintptr(unsafe.Pointer(appName16)),
		uintptr(unsafe.Pointer(cmdLine16)),
		uintptr(windows.CREATE_NO_WINDOW),
		0, // lpEnvironment = NULL: inherit caller env (net.exe needs none)
		0, // lpCurrentDirectory = NULL
		uintptr(unsafe.Pointer(&si)),
		uintptr(unsafe.Pointer(&pi)),
	)
	if r1 == 0 {
		return 1, fmt.Errorf("CreateProcessWithTokenW: %w", e)
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)

	if _, err := windows.WaitForSingleObject(pi.Process, windows.INFINITE); err != nil {
		return 1, fmt.Errorf("wait: %w", err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return 1, fmt.Errorf("exit code: %w", err)
	}
	return code, nil
}

// nodeEnsureLinkedConnections is the belt-and-suspenders backstop (approach B):
// set EnableLinkedConnections=1 so that even the in-process fallback mount
// becomes visible to the user — after one reboot. Set from the daemon (already
// elevated), not only from `install`, so enrolled machines get it too. It does
// NOT flip any "effective" flag: the setting only applies to sessions started
// after a reboot, and claiming otherwise would produce a false green.
func nodeEnsureLinkedConnections() {
	const path = `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`
	q := exec.Command("reg", "query", path, "/v", "EnableLinkedConnections")
	if out, err := q.CombinedOutput(); err == nil && strings.Contains(strings.ToLower(string(out)), "0x1") {
		return // already set on a prior boot; nothing to do
	}
	a := exec.Command("reg", "add", path, "/v", "EnableLinkedConnections", "/t", "REG_DWORD", "/d", "1", "/f")
	if err := a.Run(); err != nil {
		log.Printf("node: folder-mount: could not set EnableLinkedConnections (%v); the in-process fallback mount may stay hidden until it is set manually", err)
		return
	}
	nodeLinkedConnNeedsReboot = true
	log.Printf("node: folder-mount: EnableLinkedConnections=1 set (backstop); reboot once so an elevated-session-mapped drive also shows in normal Explorer")
}

// nodeEnsureWebClient makes sure the WebClient (WebDAV redirector) service is
// enabled and running — without it, `net use \\host@8080\...` fails with System
// error 67 no matter which session it runs in. WebClient ships Manual/Trigger-
// start and is commonly found STOPPED (never started this boot). The elevated
// daemon can enable + start it. Best-effort: any failure is logged and retried
// next poll (the mount that follows will surface the real error).
func nodeEnsureWebClient() {
	out, _ := exec.Command("sc", "query", "webclient").CombinedOutput()
	if strings.Contains(string(out), "RUNNING") {
		return
	}
	// Set it Automatic (not Disabled) so it survives the WebDAV idle-stop that
	// would otherwise drop the mapping every ~minute, then start it now. `sc
	// config start= auto` requires the space after "start=" (separate argv here).
	_ = exec.Command("sc", "config", "webclient", "start=", "auto").Run()
	if err := exec.Command("net", "start", "webclient").Run(); err != nil {
		log.Printf("node: folder-mount: could not start WebClient service (%v); WebDAV drive mounts will fail with System error 67 until it runs", err)
		return
	}
	log.Printf("node: folder-mount: started WebClient service (required for WebDAV drive mounts)")
}

// nodeNetExe / nodeSystemDir resolve net.exe / cmd.exe from the real system
// directory rather than trusting PATH in the target token's environment.
func nodeNetExe() string {
	return filepath.Join(nodeSystemDir(), "net.exe")
}

func nodeSystemDir() string {
	if d, err := windows.GetSystemDirectory(); err == nil && d != "" {
		return d
	}
	return `C:\Windows\System32`
}

// nodeComposeCmdLine builds a CreateProcess command line from argv, quoting each
// element so a share name with spaces (or shell metacharacters) can't break the
// parse. Uses the stdlib escaper.
func nodeComposeCmdLine(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = syscall.EscapeArg(a)
	}
	return strings.Join(parts, " ")
}

func nodeWTSGetActiveConsoleSessionId() uint32 {
	r1, _, _ := procWTSGetActiveConsoleSessionId.Call()
	return uint32(r1)
}

func nodeWTSQueryUserToken(session uint32, token *windows.Token) error {
	r1, _, e := procWTSQueryUserToken.Call(uintptr(session), uintptr(unsafe.Pointer(token)))
	if r1 == 0 {
		return e
	}
	return nil
}
