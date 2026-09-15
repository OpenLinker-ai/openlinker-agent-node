package providerprocess

import (
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const descendantLimit = 1024

var errDescendantTracking = errors.New("native provider descendant tracking failed")
var errDescendantCleanup = errors.New("native provider descendant cleanup incomplete")

type processIdentity struct {
	pid, parent int
	uid         uint32
	start       unix.Timeval
	zombie      bool
}

func processRecord(p unix.KinfoProc) processIdentity {
	return processIdentity{pid: int(p.Proc.P_pid), parent: int(p.Eproc.Ppid),
		uid: p.Eproc.Ucred.Uid, start: p.Proc.P_starttime, zombie: p.Proc.P_stat == 5}
}

func sameProcess(a, b processIdentity) bool {
	return a.pid > 1 && a.pid == b.pid && a.uid == b.uid && a.start == b.start
}

func readProcess(pid int) (processIdentity, error) {
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if errors.Is(err, unix.EIO) {
		// x/sys maps a zero-length, successfully read (already reaped) PID
		// record to EIO. Confirm that exact PID is absent, not that an
		// arbitrary lookup failure may be silently treated as completion.
		raw, rawErr := unix.SysctlRaw("kern.proc.pid", pid)
		if rawErr == nil && len(raw) == 0 {
			return processIdentity{}, unix.ESRCH
		}
	}
	if err != nil {
		return processIdentity{}, err
	}
	return processRecord(*p), nil
}

func processSnapshot() ([]processIdentity, error) {
	list, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Geteuid())
	if err != nil {
		return nil, err
	}
	result := make([]processIdentity, 0, len(list))
	for _, p := range list {
		result = append(result, processRecord(p))
	}
	return result, nil
}

type descendantSet struct {
	root   processIdentity
	known  map[int]processIdentity
	lookup func(int) (processIdentity, error)
	signal func(int, unix.Signal) error
}

// capture only follows a live, identity-matched ancestor in this snapshot. The
// retained identity survives reparenting. PID reuse alone never admits a new
// tree; a replacement requires fresh ancestry under another verified owner.
// Neither command names nor UID alone select PIDs.
func (s *descendantSet) capture(list []processIdentity) error {
	current := make(map[int]processIdentity, len(list))
	for _, p := range list {
		current[p.pid] = p
	}
	for changed := true; changed; {
		changed = false
		for _, child := range list {
			previous, knownPID := s.known[child.pid]
			if knownPID && sameProcess(previous, child) || child.pid <= 1 || child.zombie || child.uid != s.root.uid {
				continue
			}
			parent, known := s.known[child.parent]
			if !known || !sameProcess(parent, current[parent.pid]) || current[parent.pid].zombie {
				continue
			}
			if child.start.Sec < parent.start.Sec || child.start.Sec == parent.start.Sec && child.start.Usec < parent.start.Usec {
				continue
			}
			if !knownPID && len(s.known) >= descendantLimit {
				return errDescendantTracking
			}
			s.known[child.pid] = child
			changed = true
		}
	}
	return nil
}

// A PID is not an identity. Read the kernel start time and effective UID again
// immediately before each signal, including retries. ESRCH and replacement are
// completion, never a reason to signal the newly occupying process.
func (s *descendantSet) killMatching(p processIdentity) (bool, error) {
	current, err := s.lookup(p.pid)
	if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, errors.Join(errDescendantCleanup, err)
	}
	if current.zombie || p.pid != current.pid || p.start != current.start {
		return false, nil
	}
	if p.uid != current.uid {
		// Same lifetime, different effective identity: do not signal across
		// that boundary, but do not claim the owned process has exited.
		return false, errDescendantCleanup
	}
	if err := s.signal(p.pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
		return true, errDescendantCleanup
	}
	return true, nil
}

func (s *descendantSet) finish() error {
	deadline := time.Now().Add(time.Second)
	var result error
	for {
		live := false
		for pid, p := range s.known {
			if pid == s.root.pid {
				continue
			} // Cmd owns the original process group.
			active, err := s.killMatching(p)
			if err != nil {
				if result == nil {
					result = err
				}
				continue
			}
			live = live || active
		}
		if !live {
			return result
		}
		if time.Now().After(deadline) {
			return errDescendantCleanup
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TrackDescendants supplements process-group cancellation for one native macOS
// invocation. Call after Start and before provider initialization; invoke its
// returned function after stopping the root, before Wait/resource removal.
//
// This is bounded cleanup of observed ancestry, NOT hostile-process containment:
// a double-fork reparented before its first observation can evade discovery.
// macOS has no atomic identity-bound kill; immediate start-time revalidation
// reduces PID-reuse risk but cannot turn kill(2) into a pidfd operation.
func TrackDescendants(process *os.Process) (func() error, error) {
	if process == nil {
		return nil, errDescendantTracking
	}
	root, err := readProcess(process.Pid)
	if err != nil {
		return nil, errors.Join(errDescendantTracking, err)
	}
	if root.pid != process.Pid || root.pid <= 1 || root.uid != uint32(os.Geteuid()) || root.start.Sec == 0 {
		return nil, errDescendantTracking
	}
	set := &descendantSet{root: root, known: map[int]processIdentity{root.pid: root}, lookup: readProcess, signal: unix.Kill}
	list, err := processSnapshot()
	if err != nil {
		return nil, errors.Join(errDescendantTracking, err)
	}
	if set.capture(list) != nil {
		return nil, errDescendantTracking
	}
	stop, done := make(chan struct{}), make(chan struct{})
	var result error
	go func() {
		defer close(done)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				list, err := processSnapshot()
				if err != nil || set.capture(list) != nil {
					result = errDescendantTracking
				}
			case <-stop:
				list, err := processSnapshot()
				if err != nil || set.capture(list) != nil {
					result = errDescendantTracking
				}
				result = errors.Join(result, set.finish())
				return
			}
		}
	}()
	var once sync.Once
	return func() error { once.Do(func() { close(stop) }); <-done; return result }, nil
}
