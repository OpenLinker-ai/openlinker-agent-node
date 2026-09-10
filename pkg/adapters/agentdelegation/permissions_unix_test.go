//go:build !windows

package agentdelegation

import (
	"os"
	"testing"
)

func TestDelegationSharedRootRequiresExplicitOwnerControlledGroup(t *testing.T) {
	for _, test := range []struct {
		name    string
		mode    os.FileMode
		allowed bool
		socket  os.FileMode
	}{
		{"private", 0o700, true, 0o600},
		{"provider-group", os.ModeSetgid | 0o710, true, 0o660},
		{"group-writable", os.ModeSetgid | 0o770, false, 0},
		{"publicly-accessible", os.ModeSetgid | 0o711, false, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, test.mode); err != nil {
				t.Fatal(err)
			}
			directory, err := os.MkdirTemp(root, "attempt-")
			if err != nil {
				t.Fatal(err)
			}
			mode, err := delegationSocketMode(root, directory)
			if test.allowed != (err == nil) || mode != test.socket {
				t.Fatalf("socket mode=%o err=%v", mode, err)
			}
		})
	}
}
