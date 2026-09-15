package providerprocess

import (
	"bufio"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

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

// The bound applies to records that may still need cleanup. Exited or reused
// PIDs leave the set; alive-but-unlisted and unreadable records are retained.
func TestDescendantBoundCountsOnlyRecordsThatMayNeedCleanup(t *testing.T) {
	root := processFixture(10, 1, 100)
	s := descendantSet{root: root, known: map[int]processIdentity{root.pid: root}}
	for i := 0; i < descendantLimit-4; i++ {
		s.known[1000+i] = processFixture(1000+i, 10, 101) // exited short-lived commands
	}
	changedUID, unreadable := processFixture(50, 10, 101), processFixture(51, 10, 101)
	reusedUnlisted, reusedListed := processFixture(52, 10, 101), processFixture(53, 10, 101)
	for _, p := range []processIdentity{changedUID, unreadable, reusedUnlisted, reusedListed} {
		s.known[p.pid] = p
	}
	lookups := map[int]int{}
	s.lookup = func(pid int) (processIdentity, error) {
		lookups[pid]++
		switch pid {
		case changedUID.pid:
			alive := changedUID
			alive.uid++
			return alive, nil
		case unreadable.pid:
			return processIdentity{}, unix.EPERM
		case reusedUnlisted.pid:
			other := reusedUnlisted
			other.start.Sec++
			other.uid++
			return other, nil
		}
		return processIdentity{}, unix.ESRCH
	}
	replacement := reusedListed
	replacement.start.Sec++
	replacement.parent = 1
	list := []processIdentity{root, replacement}
	for i := 0; i < 10; i++ {
		list = append(list, processFixture(5000+i, 10, 103))
	}
	if err := s.capture(list); err != nil {
		t.Fatalf("exited records still consumed the bound: %v", err)
	}
	if _, ok := s.known[reusedListed.pid]; ok {
		t.Fatal("listed PID with a new birth time was retained or adopted")
	}
	if lookups[reusedListed.pid] != 0 || lookups[root.pid] != 0 {
		t.Fatal("snapshot-proven or root records required a separate lookup")
	}
	for _, pid := range []int{reusedUnlisted.pid, 1000, 1000 + descendantLimit - 5} {
		if _, ok := s.known[pid]; ok {
			t.Fatalf("gone or reused record %d was retained", pid)
		}
	}
	for _, pid := range []int{changedUID.pid, unreadable.pid, root.pid, 5000, 5009} {
		if _, ok := s.known[pid]; !ok {
			t.Fatalf("record %d that may still need cleanup was dropped", pid)
		}
	}
	if len(s.known) != 13 {
		t.Fatalf("unexpected retained records: %d", len(s.known))
	}
	// Without an exact lookup, unlisted records are never assumed gone.
	blind := descendantSet{root: root, known: map[int]processIdentity{root.pid: root, 60: processFixture(60, 10, 101)}}
	if err := blind.capture([]processIdentity{root}); err != nil || len(blind.known) != 2 {
		t.Fatalf("unverified absence dropped a record: %d %v", len(blind.known), err)
	}
}

// A long successful turn may run far more than descendantLimit short-lived
// commands. It must not fail tracking, and a lingering descendant must still
// be reaped after its parent exits.
func TestTrackDescendantsAcrossManyShortLivedCommands(t *testing.T) {
	script := `i=0; while [ $i -lt 1100 ]; do /bin/sleep 0.05 & i=$((i+1)); if [ $((i % 50)) -eq 0 ]; then wait; fi; done; wait
/bin/sleep 30 &
echo $!
/bin/sleep 0.2`
	command := exec.Command("/bin/sh", "-c", script)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	finish, err := TrackDescendants(command.Process)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("tracking start: %v", err)
	}
	line, readErr := bufio.NewReader(stdout).ReadString('\n')
	lingeringPID, parseErr := strconv.Atoi(strings.TrimSpace(line))
	if readErr != nil || parseErr != nil || lingeringPID <= 1 {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = finish()
		t.Fatalf("lingering descendant PID unavailable: %q %v %v", line, readErr, parseErr)
	}
	lingering, err := readProcess(lingeringPID)
	if err != nil {
		t.Fatalf("lingering descendant not observable before cleanup: %v", err)
	}
	t.Cleanup(func() {
		if current, err := readProcess(lingeringPID); err == nil && current.start == lingering.start {
			_ = unix.Kill(lingeringPID, unix.SIGKILL)
		}
	})
	if err := command.Wait(); err != nil {
		t.Fatalf("root command failed: %v", err)
	}
	if err := finish(); err != nil {
		t.Fatalf("short-lived commands exhausted descendant tracking: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, err := readProcess(lingeringPID)
		if errors.Is(err, unix.ESRCH) || err == nil && (current.start != lingering.start || current.zombie) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lingering descendant survived cleanup: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
