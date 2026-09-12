// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// nodeDaemonJob is a Job Object with KILL_ON_JOB_CLOSE that holds the daemon
// child. Its only handle lives in this launcher, so when the launcher exits for
// ANY reason (console window closed, taskkill, crash) Windows closes the handle
// and kills the daemon too. Before this, closing the launcher window orphaned
// the daemon: it kept tailscale0 up with its subnet routes (10.121.0.0/16 ...),
// so traffic still went over the VPN after the user "stopped" it, while its
// logs went silent (they are piped through the dead launcher).
var (
	nodeDaemonJobOnce sync.Once
	nodeDaemonJob     windows.Handle
	nodeDaemonJobErr  error
)

// nodeBindDaemonToLauncher puts the daemon process into nodeDaemonJob. The
// handle is deliberately never closed. Best-effort: on failure the daemon just
// runs unbound, as before.
func nodeBindDaemonToLauncher(p *os.Process) {
	nodeDaemonJobOnce.Do(func() {
		nodeDaemonJob, nodeDaemonJobErr = nodeNewKillOnCloseJob()
	})
	if nodeDaemonJobErr != nil {
		log.Printf("node: daemon job object unavailable (%v); daemon will outlive the launcher", nodeDaemonJobErr)
		return
	}
	if err := nodeAssignToJob(nodeDaemonJob, p); err != nil {
		log.Printf("node: bind daemon pid %d to launcher job: %v", p.Pid, err)
	}
}

// nodeNewKillOnCloseJob creates a Job Object that terminates every process in
// it when its last handle closes.
func nodeNewKillOnCloseJob() (windows.Handle, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return 0, err
	}
	return h, nil
}

// nodeAssignToJob adds process p to job.
func nodeAssignToJob(job windows.Handle, p *os.Process) error {
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ph)
	return windows.AssignProcessToJobObject(job, ph)
}

// nodeHideChildWindow starts the child daemon without its own console window.
func nodeHideChildWindow(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}

// Elevation is handled at launch by the embedded requireAdministrator manifest
// (node.manifest, compiled over manifest_windows_amd64.syso in CI), so Windows
// shows one UAC prompt and runs this process — and its daemon child — elevated.
// No runtime self-relaunch is needed.

// nodeStaleTunInstance is the PnP instance of the wintun adapter the daemon
// creates: tstun pins the GUID ({37217669-...}, net/tstun/tun_windows.go), so
// every run reuses this one devnode ID.
const nodeStaleTunInstance = `SWD\Wintun\{37217669-42DA-4657-A55B-0D995D328250}`

// nodeRemoveStaleTun deletes a wintun devnode left behind by an earlier daemon
// run. Called only while no daemon of ours is running, so anything at that
// instance ID is stale. Without this, wintun's create collides with the old
// node (ntstatus 0xC0000035), waits out its 15s device-query timeout, then
// succeeds on retry — measured as "got LocalBackend in 17.5s" vs 0.7s on a
// clean start, i.e. most of a slow zero-touch join. Best-effort: pnputil
// /remove-device needs Windows 10 2004+; a missing devnode is the normal case.
func nodeRemoveStaleTun() {
	c := exec.Command("pnputil", "/remove-device", nodeStaleTunInstance)
	nodeHideChildWindow(c)
	_ = c.Run()
}

// nodeKillConflicting stops processes that would fight this launcher's daemon
// for the LocalAPI pipe and the SOCKS5 / peer-HTTP-proxy ports. Runs once at
// launcher startup so the newest instance cleanly takes over. Best-effort.
//
// Two classes are killed:
//  1. stock tailscaled.exe (owns the LocalAPI pipe);
//  2. OTHER instances of THIS node exe — leftover launchers and their daemon
//     children from earlier double-clicks. Those share our exe name and our
//     ports, so a second copy's daemon can never bind and crash-loops forever
//     ("daemon exited... restarting" every ~2s). Excluding our own PID makes
//     this "newest instance wins" instead of a mutual-kill loop.
func nodeKillConflicting() {
	_ = exec.Command("taskkill", "/IM", "tailscaled.exe", "/F").Run()

	exe, err := os.Executable()
	if err != nil {
		return
	}
	name := filepath.Base(exe)
	if name == "" || strings.EqualFold(name, "tailscaled.exe") {
		return // already handled above; avoid a redundant self-targeting kill
	}
	// /FI "PID ne <self>" spares this launcher; every other same-named process
	// (old launcher + its daemon) is terminated.
	_ = exec.Command("taskkill", "/F", "/IM", name,
		"/FI", fmt.Sprintf("PID ne %d", os.Getpid())).Run()
}
