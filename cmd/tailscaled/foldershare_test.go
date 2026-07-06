// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"reflect"
	"sort"
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
