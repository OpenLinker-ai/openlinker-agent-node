package providerprocess

import (
	"errors"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

func processFixture(pid, parent int, birth int64) processIdentity {
	return processIdentity{pid: pid, parent: parent, uid: 501, start: unix.Timeval{Sec: birth}}
}

func TestDescendantCaptureRequiresLiveMatchingAncestry(t *testing.T) {
	root := processFixture(10, 1, 100)
	child, leaf := processFixture(11, 10, 101), processFixture(12, 11, 102)
	unrelated, itsChild := processFixture(20, 1, 100), processFixture(21, 20, 101)
	s := descendantSet{root: root, known: map[int]processIdentity{10: root}}
	if err := s.capture([]processIdentity{leaf, unrelated, itsChild, child, root}); err != nil {
		t.Fatal(err)
	}
	if len(s.known) != 3 {
		t.Fatalf("scope expanded beyond ancestry: %d", len(s.known))
	}
	// A known leaf remains owned after reparenting; a reused ancestor PID is
	// not sufficient evidence to adopt that replacement's new child.
	replacement := child
	replacement.start.Sec++
	replacement.parent = unrelated.pid
	newChild := processFixture(13, 11, 104)
	leaf.parent = 1
	if err := s.capture([]processIdentity{root, replacement, leaf, newChild}); err != nil {
		t.Fatal(err)
	}
	if len(s.known) != 3 || !sameProcess(s.known[12], leaf) {
		t.Fatal("reparent/reuse ownership changed")
	}
}

func TestDescendantCaptureCanAdmitReusedPIDOnlyWithFreshOwnedAncestry(t *testing.T) {
	root, original := processFixture(10, 1, 100), processFixture(11, 10, 101)
	s := descendantSet{root: root, known: map[int]processIdentity{10: root, 11: original}}
	newOwnedChild := original
	newOwnedChild.start.Usec++
	if err := s.capture([]processIdentity{root, newOwnedChild}); err != nil {
		t.Fatal(err)
	}
	if !sameProcess(s.known[11], newOwnedChild) {
		t.Fatal("fresh ownership of a replacement child was not established")
	}
}

func TestReadProcessDistinguishesAReapedOwnedChildFromLookupFailure(t *testing.T) {
	command := exec.Command("/usr/bin/true")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pid := command.Process.Pid
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := readProcess(pid); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("reaped PID not confirmed absent: %v", err)
	}
}

func TestDescendantSignalRejectsChangedIdentity(t *testing.T) {
	original := processFixture(11, 10, 101)
	for _, change := range []string{"birth", "uid", "pid", "zombie", "gone", "unreadable"} {
		t.Run(change, func(t *testing.T) {
			current := original
			var lookupErr error
			switch change {
			case "birth":
				current.start.Usec++
			case "uid":
				current.uid++
			case "pid":
				current.pid++
			case "zombie":
				current.zombie = true
			case "gone":
				lookupErr = unix.ESRCH
			case "unreadable":
				lookupErr = unix.EPERM
			}
			signals := 0
			s := descendantSet{lookup: func(int) (processIdentity, error) { return current, lookupErr },
				signal: func(int, unix.Signal) error { signals++; return nil }}
			_, err := s.killMatching(original)
			if signals != 0 {
				t.Fatal("signaled a replacement or unverified process")
			}
			if (err != nil) != (change == "unreadable" || change == "uid") {
				t.Fatalf("wrong failure boundary: %v", err)
			}
		})
	}
}

func TestDescendantCleanupRevalidatesAfterSignal(t *testing.T) {
	root, child := processFixture(10, 1, 100), processFixture(11, 10, 101)
	current := child
	signals := 0
	s := descendantSet{root: root, known: map[int]processIdentity{root.pid: root, child.pid: child},
		lookup: func(int) (processIdentity, error) { return current, nil },
		signal: func(pid int, signal unix.Signal) error {
			if pid != child.pid || signal != unix.SIGKILL {
				t.Fatal("wrong signal target")
			}
			signals++
			current.start.Usec++ // replacement between cleanup rounds
			return nil
		}}
	if err := s.finish(); err != nil || signals != 1 {
		t.Fatalf("replacement was signaled again: %d %v", signals, err)
	}
}

func TestDescendantCaptureFailsAtBoundWithoutDroppingExistingOwnership(t *testing.T) {
	root := processFixture(10, 1, 100)
	s := descendantSet{root: root, known: map[int]processIdentity{10: root}}
	list := []processIdentity{root}
	for i := 0; i < descendantLimit; i++ {
		list = append(list, processFixture(100+i, 10, 101))
	}
	if err := s.capture(list); !errors.Is(err, errDescendantTracking) || len(s.known) != descendantLimit {
		t.Fatalf("unbounded or silently incomplete capture: %d %v", len(s.known), err)
	}
}
