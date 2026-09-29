package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/queone/gkit/lockbox"
)

// cmdKey maintains the keychain item that holds the store's data key.
func (a *app) cmdKey(ref storeRef, args []string) int {
	if len(args) == 0 {
		a.errorf("key: usage: attune key (show|restore|rm [-f]|passphrase)")
		return 2
	}
	switch args[0] {
	case "show":
		return a.keyShow(ref, args[1:])
	case "restore":
		return a.keyRestore(ref, args[1:])
	case "rm":
		return a.keyRm(ref, args[1:])
	case "passphrase":
		return a.keyPassphrase(ref, args[1:])
	default:
		a.errorf("key: unknown action %q; use show, restore, rm, or passphrase", args[0])
		return 2
	}
}

// keyShow reports the store path, the key id, and whether the keychain key
// opens the store. The secret is never printed.
func (a *app) keyShow(ref storeRef, args []string) int {
	if len(args) > 0 {
		a.errorf("key show takes no arguments")
		return 2
	}
	fmt.Fprintf(a.stdout, "store: %s (%s)\n", ref.path, ref.source)
	file, hdr, err := readStoreHeader(ref.path)
	if err != nil {
		fmt.Fprintf(a.stdout, "store file: %s\n", err)
		return 1
	}
	keyID := lockbox.KeyIDString(hdr.KeyID)
	fmt.Fprintf(a.stdout, "key id: %s\n", keyID)
	key, err := a.keys.Get(keyID)
	if errors.Is(err, lockbox.ErrKeyNotFound) {
		fmt.Fprintln(a.stdout, "login keychain: missing (run `attune key restore`)")
		return 1
	}
	if err != nil {
		fmt.Fprintf(a.stdout, "login keychain: %s\n", err)
		return 1
	}
	fmt.Fprintln(a.stdout, "login keychain: present")
	if _, _, err := lockbox.Open(file, key); err != nil {
		fmt.Fprintf(a.stdout, "store opens: no (%s)\n", err)
		return 1
	}
	fmt.Fprintln(a.stdout, "store opens: yes")
	return 0
}

// keyRestore puts the key back into the keychain with the recovery passphrase.
func (a *app) keyRestore(ref storeRef, args []string) int {
	if len(args) > 0 {
		a.errorf("key restore takes no arguments")
		return 2
	}
	_, hdr, err := readStoreHeader(ref.path)
	if err != nil {
		a.errorf("key restore: %s", err)
		return 1
	}
	if _, err := a.keys.Get(lockbox.KeyIDString(hdr.KeyID)); err == nil {
		a.errorf("key restore: the key for %s is already in the login keychain", ref.path)
		return 1
	}
	return a.restoreKey("key restore", ref.path, hdr)
}

// keyRm deletes the keychain item after a confirmation prompt.
func (a *app) keyRm(ref storeRef, args []string) int {
	flags, pos, err := parseArgs(args, []flagSpec{{"-f", "--force", false}})
	if err != nil || len(pos) > 0 {
		a.errorf("key rm: usage: attune key rm [-f]")
		return 2
	}
	_, hdr, err := readStoreHeader(ref.path)
	if err != nil {
		a.errorf("key rm: %s", err)
		return 1
	}
	if flags["--force"] != "true" {
		if !a.isTerminal() {
			a.errorf("key rm needs a terminal, or -f to skip the prompt")
			return 1
		}
		answer, err := a.readLine("Delete the keychain item for " + ref.path + "? [y/N] ")
		if err != nil {
			a.errorf("key rm: read answer: %s", err)
			return 1
		}
		if s := strings.ToLower(strings.TrimSpace(answer)); s != "y" && s != "yes" {
			fmt.Fprintln(a.stdout, "nothing deleted")
			return 1
		}
	}
	err = a.keys.Delete(lockbox.KeyIDString(hdr.KeyID))
	if errors.Is(err, lockbox.ErrKeyNotFound) {
		a.errorf("key rm: no keychain item for %s", ref.path)
		return 1
	}
	if err != nil {
		a.errorf("key rm: %s", err)
		return 1
	}
	fmt.Fprintln(a.stdout, "keychain item deleted; the recovery passphrase is now the only way back in")
	return 0
}

// keyPassphrase rewraps the key under a new recovery passphrase.
func (a *app) keyPassphrase(ref storeRef, args []string) int {
	if len(args) > 0 {
		a.errorf("key passphrase takes no arguments")
		return 2
	}
	if !a.isTerminal() {
		a.errorf("key passphrase needs a terminal")
		return 1
	}
	_, hdr, err := readStoreHeader(ref.path)
	if err != nil {
		a.errorf("key passphrase: %s", err)
		return 1
	}
	key, err := a.keys.Get(lockbox.KeyIDString(hdr.KeyID))
	if errors.Is(err, lockbox.ErrKeyNotFound) {
		a.errorf("key passphrase: no key in the login keychain; run `attune key restore` first")
		return 1
	}
	if err != nil {
		a.errorf("key passphrase: %s", err)
		return 1
	}
	st, err := lockbox.Load(ref.path, key)
	if err != nil {
		a.errorf("key passphrase: open %s: %s", ref.path, err)
		return 1
	}
	defer st.Close()
	pass, code := a.newPassphrase("key passphrase")
	if code != 0 {
		return code
	}
	if err := st.Header.WrapKey(key, pass, a.kdf); err != nil {
		a.errorf("key passphrase: %s", err)
		return 1
	}
	if err := a.save(st, "key passphrase"); err != nil {
		a.errorf("key passphrase: %s", err)
		return 1
	}
	fmt.Fprintln(a.stdout, "recovery passphrase changed")
	return 0
}
