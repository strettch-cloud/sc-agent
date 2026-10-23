package ssh

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

const customerKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMOt2eMNtlWd+XMjAVmgQ/xbdhHAzSD5M+HVBkyuaof5 customer@test"

// newKeyManagerWithCustomerKey manages an authorized_keys in a temporary home
// that holds one key the customer added.
func newKeyManagerWithCustomerKey(t *testing.T) *KeyManager {
	t.Helper()
	sshDir := filepath.Join(t.TempDir(), ".ssh")
	if err := os.Mkdir(sshDir, SSHDirPerm); err != nil {
		t.Fatal(err)
	}
	authorizedKeys := filepath.Join(sshDir, "authorized_keys")
	if err := os.WriteFile(authorizedKeys, []byte(customerKey+"\n"), AuthorizedKeysPerm); err != nil {
		t.Fatal(err)
	}
	return &KeyManager{logger: zap.NewNop(), sshDirPath: sshDir, authorizedKeysPath: authorizedKeys}
}

func installConsoleKey(km *KeyManager) error {
	return km.Install(&ConsoleKeyData{
		PublicKey:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIL9+rlL+7Hj6yo1r12wYllDp32neLN+VG5TBvRDUYaqV console@test",
		Principal:   "root",
		RequesterID: "support@test",
		CreatedAt:   "2026-01-01T00:00:00Z",
		ExpireAt:    "2026-01-01T01:00:00Z",
	})
}

func assertOnlyTheCustomersKey(t *testing.T, km *KeyManager) {
	t.Helper()
	content, err := os.ReadFile(km.authorizedKeysPath)
	if err != nil || string(content) != customerKey+"\n" {
		t.Errorf("authorized_keys = %q, %v, want the customer's key untouched", content, err)
	}
}

// Install writes a temporary file and renames it over authorized_keys, so
// when the temporary file can't be written, the customer's file is untouched.
func TestAnInstallThatCantCreateItsTemporaryFileChangesNothing(t *testing.T) {
	km := newKeyManagerWithCustomerKey(t)
	if err := os.Mkdir(km.authorizedKeysPath+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}

	if err := installConsoleKey(km); err == nil {
		t.Error("Install succeeded with a directory where its temporary file goes")
	}
	assertOnlyTheCustomersKey(t, km)
}

// /dev/full fails every write with "no space left on device", as a full disk
// would.
func TestAnInstallOnAFullDiskChangesNothingAndCleansUp(t *testing.T) {
	km := newKeyManagerWithCustomerKey(t)
	temporaryFile := km.authorizedKeysPath + ".tmp"
	if err := os.Symlink("/dev/full", temporaryFile); err != nil {
		t.Fatal(err)
	}

	if err := installConsoleKey(km); err == nil {
		t.Error("Install succeeded on a full disk")
	}
	assertOnlyTheCustomersKey(t, km)
	if _, err := os.Lstat(temporaryFile); !os.IsNotExist(err) {
		t.Errorf("Install left its temporary file behind: %v", err)
	}
}
