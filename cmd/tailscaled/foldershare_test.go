// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestFolderShareDiff(t *testing.T) {
	tests := []struct {
		name           string
		current, want  map[string]string
		wantAdd, wantR []string
	}{
		{
			name:    "nothing changes",
			current: map[string]string{"docs": "/a"},
			want:    map[string]string{"docs": "/a"},
		},
		{
			name:    "new share",
			current: map[string]string{},
			want:    map[string]string{"docs": "/a"},
			wantAdd: []string{"docs"},
		},
		{
			name:    "removed share",
			current: map[string]string{"docs": "/a"},
			want:    map[string]string{},
			wantR:   []string{"docs"},
		},
		{
			name:    "path changed -> re-share",
			current: map[string]string{"docs": "/a"},
			want:    map[string]string{"docs": "/b"},
			wantAdd: []string{"docs"},
		},
		{
			name:    "one added, one removed, one unchanged",
			current: map[string]string{"old": "/x", "same": "/y"},
			want:    map[string]string{"new": "/z", "same": "/y"},
			wantAdd: []string{"new"},
			wantR:   []string{"old"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			add, rm := nodeDiffShares(tt.current, tt.want)
			sort.Strings(add)
			sort.Strings(rm)
			sort.Strings(tt.wantAdd)
			sort.Strings(tt.wantR)
			if !reflect.DeepEqual(add, tt.wantAdd) {
				t.Errorf("toAdd = %v, want %v", add, tt.wantAdd)
			}
			if !reflect.DeepEqual(rm, tt.wantR) {
				t.Errorf("toRemove = %v, want %v", rm, tt.wantR)
			}
		})
	}
}

func TestFolderShareNormalizeDriveLetter(t *testing.T) {
	tests := []struct{ in, want string }{
		{"z", "Z:"},
		{"Z", "Z:"},
		{"z:", "Z:"},
		{"Z:", "Z:"},
		{" z ", "Z:"},
		{"", ""},
		{"ZZ", ""},
		{"1", ""},
		{"z:z", ""},
	}
	for _, tt := range tests {
		if got := nodeNormalizeDriveLetter(tt.in); got != tt.want {
			t.Errorf("nodeNormalizeDriveLetter(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFolderShareDNSShortName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"mylaptop.tailxyz.ts.net.", "mylaptop"},
		{"mylaptop.tailxyz.ts.net", "mylaptop"},
		{"mylaptop.", "mylaptop"},
		{"mylaptop", "mylaptop"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := nodeDNSShortName(tt.in); got != tt.want {
			t.Errorf("nodeDNSShortName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFolderShareMountUNC(t *testing.T) {
	got := nodeDriveMountUNC("mydomain.com", "mylaptop", "docs")
	want := `\\100.100.100.100@8080\mydomain.com\mylaptop\docs`
	if got != want {
		t.Errorf("nodeDriveMountUNC() = %q, want %q", got, want)
	}
}

func TestFolderSharePlanMounts(t *testing.T) {
	// freeSeq ignores the exclude argument on purpose — it exists to prove
	// nodePlanMounts consumes freeLetters correctly (one call per entry
	// needing an auto-pick, in order), not to re-test the real
	// nodeNextFreeDriveLetter's exclude-handling (that's covered separately
	// by TestFolderShareNextFreeDriveLetterExcludes and
	// TestFolderSharePlanMountsWithRealFreeLetterFunc below).
	freeSeq := func(letters ...string) func(map[string]bool) string {
		i := 0
		return func(map[string]bool) string {
			if i >= len(letters) {
				return ""
			}
			l := letters[i]
			i++
			return l
		}
	}

	t.Run("new mount with explicit drive", func(t *testing.T) {
		desired := []nodeMountDesired{{Machine: "pc-a", OwnerIP: "100.64.0.11", Share: "docs", Drive: "z"}}
		toMount, toUnmount := nodePlanMounts(desired, map[string]string{}, freeSeq())
		if len(toUnmount) != 0 {
			t.Errorf("toUnmount = %v, want empty", toUnmount)
		}
		if len(toMount) != 1 || toMount[0].Drive != "Z:" || toMount[0].Key != "pc-a|docs" {
			t.Errorf("toMount = %+v", toMount)
		}
	})

	t.Run("already correctly mounted -> no-op", func(t *testing.T) {
		desired := []nodeMountDesired{{Machine: "pc-a", OwnerIP: "100.64.0.11", Share: "docs", Drive: "z"}}
		mounted := map[string]string{"Z:": "pc-a|docs"}
		toMount, toUnmount := nodePlanMounts(desired, mounted, freeSeq())
		if len(toMount) != 0 || len(toUnmount) != 0 {
			t.Errorf("toMount=%v toUnmount=%v, want both empty", toMount, toUnmount)
		}
	})

	t.Run("revoked grant -> unmount", func(t *testing.T) {
		mounted := map[string]string{"Z:": "pc-a|docs"}
		toMount, toUnmount := nodePlanMounts(nil, mounted, freeSeq())
		if len(toMount) != 0 {
			t.Errorf("toMount = %v, want empty", toMount)
		}
		if !reflect.DeepEqual(toUnmount, []string{"Z:"}) {
			t.Errorf("toUnmount = %v, want [Z:]", toUnmount)
		}
	})

	t.Run("auto-pick drive letter for new mount", func(t *testing.T) {
		desired := []nodeMountDesired{{Machine: "pc-a", OwnerIP: "100.64.0.11", Share: "docs"}}
		toMount, _ := nodePlanMounts(desired, map[string]string{}, freeSeq("Y:"))
		if len(toMount) != 1 || toMount[0].Drive != "Y:" {
			t.Errorf("toMount = %+v, want drive Y:", toMount)
		}
	})

	t.Run("auto-picked drive is remembered across polls (no explicit drive)", func(t *testing.T) {
		desired := []nodeMountDesired{{Machine: "pc-a", OwnerIP: "100.64.0.11", Share: "docs"}}
		mounted := map[string]string{"Y:": "pc-a|docs"}
		// freeSeq would return "" (exhausted) if called — proves the existing
		// assignment is reused instead of re-picking.
		toMount, toUnmount := nodePlanMounts(desired, mounted, freeSeq())
		if len(toMount) != 0 || len(toUnmount) != 0 {
			t.Errorf("toMount=%v toUnmount=%v, want both empty (already assigned Y:)", toMount, toUnmount)
		}
	})

	t.Run("skips entries missing OwnerIP or Share", func(t *testing.T) {
		desired := []nodeMountDesired{
			{Machine: "pc-a", Share: "docs"},        // no OwnerIP
			{Machine: "pc-b", OwnerIP: "100.64.0.1"}, // no Share
		}
		toMount, toUnmount := nodePlanMounts(desired, map[string]string{}, freeSeq("Z:", "Y:"))
		if len(toMount) != 0 || len(toUnmount) != 0 {
			t.Errorf("toMount=%v toUnmount=%v, want both empty", toMount, toUnmount)
		}
	})

	t.Run("two new mounts don't collide on the same free letter", func(t *testing.T) {
		desired := []nodeMountDesired{
			{Machine: "pc-a", OwnerIP: "100.64.0.11", Share: "docs"},
			{Machine: "pc-b", OwnerIP: "100.64.0.12", Share: "photos"},
		}
		toMount, _ := nodePlanMounts(desired, map[string]string{}, freeSeq("Z:", "Y:"))
		if len(toMount) != 2 {
			t.Fatalf("toMount = %+v, want 2 entries", toMount)
		}
		if toMount[0].Drive == toMount[1].Drive {
			t.Errorf("both mounts got drive %q, want distinct letters", toMount[0].Drive)
		}
	})

	t.Run("changing explicit drive releases the old one instead of leaking it", func(t *testing.T) {
		// Regression: previously mounted at Z: (auto-picked or explicit),
		// admin now pins Y: for the same machine|share — Z: must be released,
		// not left mounted alongside the new Y:.
		desired := []nodeMountDesired{{Machine: "pc-a", OwnerIP: "100.64.0.11", Share: "docs", Drive: "y"}}
		mounted := map[string]string{"Z:": "pc-a|docs"}
		toMount, toUnmount := nodePlanMounts(desired, mounted, freeSeq())
		if len(toMount) != 1 || toMount[0].Drive != "Y:" {
			t.Fatalf("toMount = %+v, want exactly [Y:]", toMount)
		}
		if !reflect.DeepEqual(toUnmount, []string{"Z:"}) {
			t.Errorf("toUnmount = %v, want [Z:] (the stale letter must be released)", toUnmount)
		}
	})

	t.Run("a desired-but-unmountable entry is never torn down (want set before drive resolution)", func(t *testing.T) {
		// pc-a|docs is already mounted at Y: and reuses it via nodeDriveForKey
		// (no freeLetters call needed); pc-b|photos is brand new and gets NO
		// letter (freeLetters exhausted). Neither should cause pc-a|docs to
		// be unmounted — want[key]=true is set for BOTH before either's drive
		// is resolved, so a sibling entry's "no free letter" failure can't
		// retroactively revoke an unrelated, already-correctly-mounted share.
		desired := []nodeMountDesired{
			{Machine: "pc-a", OwnerIP: "100.64.0.11", Share: "docs"},
			{Machine: "pc-b", OwnerIP: "100.64.0.12", Share: "photos"},
		}
		mounted := map[string]string{"Y:": "pc-a|docs"}
		toMount, toUnmount := nodePlanMounts(desired, mounted, freeSeq() /* exhausted immediately */)
		if len(toMount) != 0 {
			t.Errorf("toMount = %+v, want empty (pc-b|photos can't get a letter, pc-a|docs needs none)", toMount)
		}
		if len(toUnmount) != 0 {
			t.Errorf("toUnmount = %v, want empty (pc-a|docs must stay mounted at Y:)", toUnmount)
		}
	})
}

// TestFolderShareNextFreeDriveLetterExcludes can't assert a specific letter
// (depends on real drive state on whatever machine runs this test), but CAN
// assert the exclude set is honored: whatever letter comes back must not be
// one just excluded. This is the property the historical infinite-loop bug
// violated (see TestFolderSharePlanMountsWithRealFreeLetterFunc).
func TestFolderShareNextFreeDriveLetterExcludes(t *testing.T) {
	first := nodeNextFreeDriveLetter(nil)
	if first == "" {
		t.Skip("no free drive letter available on this runner")
	}
	got := nodeNextFreeDriveLetter(map[string]bool{first: true})
	if got == first {
		t.Errorf("nodeNextFreeDriveLetter(%v) = %q, want a different letter than the excluded one", map[string]bool{first: true}, got)
	}
}

// TestFolderSharePlanMountsWithRealFreeLetterFunc is the regression test for
// the historical bug: nodeNextFreeDriveLetter used to take no exclude
// argument at all and just re-check os.Stat, which doesn't change until
// nodeReconcileMounts actually runs `net use` later — so two simultaneous
// auto-pick entries in one nodePlanMounts call would retry against the SAME
// letter forever. Exercises the REAL function (not a stub); asserts
// termination within a timeout and two distinct assigned letters.
func TestFolderSharePlanMountsWithRealFreeLetterFunc(t *testing.T) {
	desired := []nodeMountDesired{
		{Machine: "pc-a", OwnerIP: "100.64.0.11", Share: "docs"},
		{Machine: "pc-b", OwnerIP: "100.64.0.12", Share: "photos"},
	}

	type result struct {
		toMount []driveMountPlan
	}
	done := make(chan result, 1)
	go func() {
		toMount, _ := nodePlanMounts(desired, map[string]string{}, nodeNextFreeDriveLetter)
		done <- result{toMount}
	}()

	select {
	case r := <-done:
		if len(r.toMount) != 2 {
			t.Fatalf("got %d mount plans, want 2: %+v", len(r.toMount), r.toMount)
		}
		if r.toMount[0].Drive == r.toMount[1].Drive {
			t.Errorf("both mounts got the same drive %q, want distinct letters", r.toMount[0].Drive)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nodePlanMounts did not terminate within 5s — infinite-loop regression")
	}
}

func TestFolderShareValidMountShare(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"docs", true},
		{"my_share", true},
		{"", false},
		{`a\b`, false},
		{"a/b", false},
		{`..\otherpeer\othershare`, false},
	}
	for _, tt := range tests {
		if got := nodeValidMountShare(tt.in); got != tt.want {
			t.Errorf("nodeValidMountShare(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestNodeReportFolderShareStatusNoop(t *testing.T) {
	// Empty base or mac must be a silent no-op (nil), never a request.
	if err := nodeReportFolderShareStatus("", "dc:4a", "h", nil, nil); err != nil {
		t.Errorf("empty base: want nil, got %v", err)
	}
	if err := nodeReportFolderShareStatus("http://127.0.0.1:0", "", "h", nil, nil); err != nil {
		t.Errorf("empty mac: want nil, got %v", err)
	}
}

func TestNodeReportFolderShareStatusPost(t *testing.T) {
	type payload struct {
		Mac      string            `json:"mac"`
		Hostname string            `json:"hostname"`
		Shares   []nodeShareStatus `json:"shares"`
		Mounts   []nodeMountStatus `json:"mounts"`
	}
	var got payload
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Trailing slash on base must not double-up the path.
	err := nodeReportFolderShareStatus(
		srv.URL+"/",
		"dc:4a:3e:3d:54:70",
		"ITOP-THANHHN5",
		[]nodeShareStatus{{Name: "tool", Path: "E:\\Tool", OK: true}},
		[]nodeMountStatus{{Share: "tool", Drive: "Z:", OK: false, Error: "System error 67"}},
	)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/internal/foldershare-status" {
		t.Errorf("path = %q, want /api/internal/foldershare-status", gotPath)
	}
	if got.Mac != "dc:4a:3e:3d:54:70" || got.Hostname != "ITOP-THANHHN5" {
		t.Errorf("mac/hostname mismatch: %+v", got)
	}
	if len(got.Shares) != 1 || !got.Shares[0].OK || got.Shares[0].Name != "tool" {
		t.Errorf("shares mismatch: %+v", got.Shares)
	}
	if len(got.Mounts) != 1 || got.Mounts[0].OK || got.Mounts[0].Error != "System error 67" {
		t.Errorf("mounts mismatch: %+v", got.Mounts)
	}
}

func TestNodeReportFolderShareStatusNilMarshalsEmpty(t *testing.T) {
	// nil shares/mounts must serialize as [] (not null) so the server sees an
	// explicit empty report rather than a decode edge case.
	var rawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rawBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := nodeReportFolderShareStatus(srv.URL, "dc:4a", "h", nil, nil); err != nil {
		t.Fatalf("report: %v", err)
	}
	if !contains(rawBody, `"shares":[]`) || !contains(rawBody, `"mounts":[]`) {
		t.Errorf("nil should marshal as []: body=%s", rawBody)
	}
}

func TestNodeReportFolderShareStatusHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := nodeReportFolderShareStatus(srv.URL, "dc:4a", "h", nil, nil); err == nil {
		t.Error("want error on non-200, got nil")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestNodeShareSig(t *testing.T) {
	// Order-independent: same shares in any map iteration order → same sig, so
	// a poll returning the same set never needlessly restarts the file server.
	a := nodeShareSig(map[string]string{"docs": `E:\Docs`, "tool": `E:\Tool`})
	b := nodeShareSig(map[string]string{"tool": `E:\Tool`, "docs": `E:\Docs`})
	if a != b {
		t.Errorf("sig must be order-independent: %q vs %q", a, b)
	}
	// Empty set is stable and distinct from any non-empty set.
	if nodeShareSig(map[string]string{}) != "" {
		t.Errorf("empty share set should sig to empty string, got %q", nodeShareSig(map[string]string{}))
	}
	// A changed path (same name) must change the signature (triggers restart).
	if nodeShareSig(map[string]string{"docs": `E:\Docs`}) == nodeShareSig(map[string]string{"docs": `F:\Docs`}) {
		t.Error("different path for same share name must change the signature")
	}
	// An added share must change the signature.
	if a == nodeShareSig(map[string]string{"docs": `E:\Docs`}) {
		t.Error("adding a share must change the signature")
	}
	// A share name that is a prefix of another must not collide (NUL delimiter).
	if nodeShareSig(map[string]string{"ab": "x", "c": "y"}) == nodeShareSig(map[string]string{"a": "bxc", "": "y"}) {
		t.Error("delimiter must prevent name/path concatenation collisions")
	}
}

func TestFolderShareMountArgv(t *testing.T) {
	if got := nodeMountArgv("Z:", `\\100.100.100.100@8080\tn\itop\test`); !reflect.DeepEqual(
		got, []string{"use", "Z:", `\\100.100.100.100@8080\tn\itop\test`, "/PERSISTENT:NO"}) {
		t.Errorf("nodeMountArgv = %v", got)
	}
	if got := nodeUnmountArgv("Z:"); !reflect.DeepEqual(got, []string{"use", "Z:", "/delete", "/y"}) {
		t.Errorf("nodeUnmountArgv = %v", got)
	}
	if got := nodeListMountsArgv(); !reflect.DeepEqual(got, []string{"use"}) {
		t.Errorf("nodeListMountsArgv = %v", got)
	}
}

func TestFolderShareParseNetUseTable(t *testing.T) {
	// Realistic `net use` output: header, an OK row, a Disconnected row, a
	// no-status row, and trailer. Only drive-lettered rows with a \\ remote count.
	out := "New connections will be remembered.\r\n\r\n" +
		"Status       Local     Remote                    Network\r\n" +
		"-------------------------------------------------------------------------------\r\n" +
		`OK           Z:        \\100.100.100.100@8080\tn\itop\test` + "  Web Client Network\r\n" +
		`Disconnected Y:        \\100.100.100.100@8080\tn\pc\docs` + "     Web Client Network\r\n" +
		`             X:        \\srv\share` + "                          Microsoft Windows Network\r\n" +
		"The command completed successfully.\r\n"
	got := nodeParseNetUseTable(out)
	want := map[string]string{
		"Z:": `\\100.100.100.100@8080\tn\itop\test`,
		"Y:": `\\100.100.100.100@8080\tn\pc\docs`,
		"X:": `\\srv\share`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nodeParseNetUseTable = %#v, want %#v", got, want)
	}
	if len(nodeParseNetUseTable("There are no entries in the list.\r\n")) != 0 {
		t.Error("empty listing must parse to no entries")
	}
}

func TestFolderShareUNCMatch(t *testing.T) {
	a := `\\100.100.100.100@8080\tn\itop\test`
	if !nodeUNCMatch(a, a) {
		t.Error("identical UNC must match")
	}
	// Case-insensitive + trailing-slash tolerant (net use may echo either).
	if !nodeUNCMatch(strings.ToUpper(a), a+`\`) {
		t.Error("case/trailing-slash differences must still match")
	}
	if nodeUNCMatch(a, `\\100.100.100.100@8080\tn\itop\other`) {
		t.Error("different share must not match")
	}
	if nodeUNCMatch("", "") {
		t.Error("empty must not match (nothing to adopt)")
	}
}

func TestFolderShareIsAlreadyMappedErr(t *testing.T) {
	yes := []error{
		errors.New("exit status 2: System error 85 has occurred."),
		errors.New("net use exited 2: System error 1219 has occurred."),
		errors.New("The local device name is already in use."),
	}
	for _, e := range yes {
		if !nodeIsAlreadyMappedErr(e) {
			t.Errorf("want already-mapped for %v", e)
		}
	}
	no := []error{nil, errors.New("System error 67 has occurred."), errors.New("access denied")}
	for _, e := range no {
		if nodeIsAlreadyMappedErr(e) {
			t.Errorf("want NOT already-mapped for %v", e)
		}
	}
}

func TestFolderShareSelectMountTokenSource(t *testing.T) {
	tests := []struct {
		name string
		env  nodeTokenEnv
		want nodeMountTokenSource
	}{
		{"elevated user with linked", nodeTokenEnv{Elevated: true, HaveLinkedToken: true}, nodeMountTokenLinked},
		{"elevated user no linked (UAC off/RID500)", nodeTokenEnv{Elevated: true}, nodeMountTokenCurrent},
		{"non-elevated (linked is the elevated one)", nodeTokenEnv{HaveLinkedToken: true, LinkedElevated: true}, nodeMountTokenCurrent},
		{"system with console user", nodeTokenEnv{IsSystem: true, HaveConsoleUser: true}, nodeMountTokenActiveSession},
		{"system no console", nodeTokenEnv{IsSystem: true}, nodeMountTokenCurrent},
	}
	for _, tt := range tests {
		if got := nodeSelectMountTokenSource(tt.env); got != tt.want {
			t.Errorf("%s: nodeSelectMountTokenSource = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestFolderShareMountVisibleToUser(t *testing.T) {
	tests := []struct {
		src                            nodeMountTokenSource
		userIsolated, linkedConnEffect bool
		want                           bool
	}{
		{nodeMountTokenLinked, true, false, true},        // mapped into user session
		{nodeMountTokenActiveSession, true, false, true}, // mapped into console session
		{nodeMountTokenCurrent, false, false, true},      // not isolated -> visible
		{nodeMountTokenCurrent, true, false, false},      // isolated, no linked-conn -> hidden
		{nodeMountTokenCurrent, true, true, true},        // isolated but linked-conn effective
	}
	for _, tt := range tests {
		if got := nodeMountVisibleToUser(tt.src, tt.userIsolated, tt.linkedConnEffect); got != tt.want {
			t.Errorf("nodeMountVisibleToUser(%v,%v,%v) = %v, want %v",
				tt.src, tt.userIsolated, tt.linkedConnEffect, got, tt.want)
		}
	}
}

func TestFolderShareMountStatusFor(t *testing.T) {
	m := nodeMountDesired{Machine: "itop", Share: "test", Drive: "Z:"}

	// Success visible to user -> clean OK.
	if st := nodeMountStatusFor(m, "Z:", "", nodeMountTokenLinked, true, false); !st.OK || st.Error != "" || st.Warning != "" {
		t.Errorf("linked success: %+v, want OK with no error/warning", st)
	}
	// Mapped only in elevated session -> NOT OK, but never a blank error.
	st := nodeMountStatusFor(m, "Z:", "", nodeMountTokenCurrent, true, false)
	if st.OK || st.Error == "" || st.Warning == "" {
		t.Errorf("hidden mount: %+v, want OK=false with non-empty error AND warning", st)
	}
	// A real mount error dominates.
	if st := nodeMountStatusFor(m, "", "System error 67", nodeMountTokenCurrent, true, false); st.OK || st.Error != "System error 67" {
		t.Errorf("mount error: %+v, want OK=false Error=System error 67", st)
	}
	// No free letter.
	if st := nodeMountStatusFor(m, "", "", nodeMountTokenCurrent, false, false); st.OK || st.Error != "not mounted" {
		t.Errorf("no letter: %+v, want OK=false Error=not mounted", st)
	}
}

func TestFolderShareAdoptExistingMounts(t *testing.T) {
	defer resetMountState()()
	unc := nodeDriveMountUNC("ts.net", "itop", "test")
	nodeListUserMounts = func() (map[string]string, nodeMountTokenSource, error) {
		return map[string]string{"Z:": unc}, nodeMountTokenLinked, nil
	}
	desired := []nodeMountDesired{{Machine: "itop", OwnerIP: "100.64.0.19", Share: "test", Drive: "Z:"}}
	uncFor := func(ownerIP, share string) string { return unc }

	nodeAdoptExistingMounts(desired, uncFor)
	if nodeMountedDrives["Z:"] != "itop|test" {
		t.Errorf("expected Z: adopted to itop|test, got %q", nodeMountedDrives["Z:"])
	}
	if nodeMountSource["Z:"] != nodeMountTokenLinked {
		t.Errorf("expected adopted source linked, got %v", nodeMountSource["Z:"])
	}

	// A UNC mismatch must NOT adopt.
	resetMaps()
	uncMismatch := func(ownerIP, share string) string { return nodeDriveMountUNC("ts.net", "itop", "other") }
	nodeAdoptExistingMounts(desired, uncMismatch)
	if len(nodeMountedDrives) != 0 {
		t.Errorf("mismatched UNC must not adopt, got %v", nodeMountedDrives)
	}
}

func TestFolderShareReconcile(t *testing.T) {
	defer resetMountState()()
	nodeMountsSupported = true
	nodeResolveDrivePeers = func(string) (map[string]string, string, error) {
		return map[string]string{"100.64.0.19": "itop"}, "ts.net", nil
	}
	nodeMountEnvFn = func() (bool, bool) { return true, false } // elevated+isolated
	unc := nodeDriveMountUNC("ts.net", "itop", "test")
	desired := []nodeMountDesired{{Machine: "itop", OwnerIP: "100.64.0.19", Share: "test", Drive: "Z:"}}

	t.Run("adopt existing -> no remount, OK", func(t *testing.T) {
		resetMaps()
		nodeListUserMounts = func() (map[string]string, nodeMountTokenSource, error) {
			return map[string]string{"Z:": unc}, nodeMountTokenLinked, nil
		}
		mounted := false
		nodeMountDrive = func(string, string) (nodeMountTokenSource, error) { mounted = true; return nodeMountTokenLinked, nil }
		st := nodeReconcileMounts("exe", desired)
		if mounted {
			t.Error("must not re-mount an adopted drive")
		}
		if len(st) != 1 || !st[0].OK || st[0].Drive != "Z:" {
			t.Errorf("status = %+v, want one OK Z:", st)
		}
	})

	t.Run("fresh mount via linked -> OK", func(t *testing.T) {
		resetMaps()
		nodeListUserMounts = func() (map[string]string, nodeMountTokenSource, error) { return map[string]string{}, nodeMountTokenLinked, nil }
		var gotDrive, gotUNC string
		nodeMountDrive = func(d, u string) (nodeMountTokenSource, error) { gotDrive, gotUNC = d, u; return nodeMountTokenLinked, nil }
		st := nodeReconcileMounts("exe", desired)
		if gotDrive != "Z:" || gotUNC != unc {
			t.Errorf("mounted %q -> %q, want Z: -> %q", gotDrive, gotUNC, unc)
		}
		if len(st) != 1 || !st[0].OK {
			t.Errorf("status = %+v, want one OK", st)
		}
	})

	t.Run("mount via current while isolated -> honest not-OK", func(t *testing.T) {
		resetMaps()
		nodeListUserMounts = func() (map[string]string, nodeMountTokenSource, error) { return map[string]string{}, nodeMountTokenCurrent, nil }
		nodeMountDrive = func(string, string) (nodeMountTokenSource, error) { return nodeMountTokenCurrent, nil }
		st := nodeReconcileMounts("exe", desired)
		if len(st) != 1 || st[0].OK || st[0].Error == "" {
			t.Errorf("status = %+v, want one not-OK with a non-empty error", st)
		}
	})
}

func TestFolderShareMountPoints2Key(t *testing.T) {
	got := nodeMountPoints2Key(`\\100.100.100.100@8080\tail.example.ts.net\itop\test2`)
	want := "##100.100.100.100@8080#tail.example.ts.net#itop#test2"
	if got != want {
		t.Errorf("nodeMountPoints2Key = %q, want %q", got, want)
	}
}

// resetMaps clears the in-memory mount tracking between subtests.
func resetMaps() {
	nodeMountedDrives = map[string]string{}
	nodeMountSource = map[string]nodeMountTokenSource{}
	nodeDriveLabeled = map[string]string{}
}

// resetMountState snapshots the injectable mount seams + tracking maps and
// returns a restorer, so a test can override them without leaking into others.
func resetMountState() func() {
	origSupported := nodeMountsSupported
	origResolve := nodeResolveDrivePeers
	origList := nodeListUserMounts
	origMount := nodeMountDrive
	origUnmount := nodeUnmountDrive
	origEnv := nodeMountEnvFn
	origMounted := nodeMountedDrives
	origSource := nodeMountSource
	resetMaps()
	nodeUnmountDrive = func(string, nodeMountTokenSource) error { return nil }
	return func() {
		nodeMountsSupported = origSupported
		nodeResolveDrivePeers = origResolve
		nodeListUserMounts = origList
		nodeMountDrive = origMount
		nodeUnmountDrive = origUnmount
		nodeMountEnvFn = origEnv
		nodeMountedDrives = origMounted
		nodeMountSource = origSource
	}
}
