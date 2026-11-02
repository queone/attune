package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/queone/gkit/lockbox"
)

// terminalEditor runs $EDITOR, vi by default, on path with the terminal
// attached. The command goes through sh so an EDITOR with arguments works.
func terminalEditor(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command("sh", "-c", editor+` "$1"`, "attune-edit", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// cmdEdit opens one stored entry in the editor and saves a validated new
// version. An invalid result reopens the editor; quitting with the content
// unchanged from what it last showed aborts with nothing saved. When
// standard input is not a terminal, the new content comes from it instead.
func (a *app) cmdEdit(ref storeRef, args []string) int {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 1 {
		a.errorf("edit: usage: attune edit NAME")
		return 2
	}
	st, err := a.openStore(ref.path)
	if err != nil {
		a.errorf("edit: %s", err)
		return 1
	}
	defer st.Close()
	all, err := st.Entries()
	if err != nil {
		a.errorf("edit: %s", err)
		return 1
	}
	pick := entryFor(all, pos[0])
	if pick == nil {
		a.errorf("edit: %s is not registered; use `attune add` for a new spec", pos[0])
		return 1
	}
	latest, ok, err := st.Latest(pick.ID)
	if err != nil {
		a.errorf("edit: %s", err)
		return 1
	}
	if !ok {
		a.errorf("edit: %s has nothing stored yet", pick.Target)
		return 1
	}
	if !a.isTerminal() {
		return a.editFromStdin(st, pick, latest.Content, pos[0])
	}
	dir, err := os.MkdirTemp("", "attune-edit-")
	if err != nil {
		a.errorf("edit: %s", err)
		return 1
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o700); err != nil {
		a.errorf("edit: %s", err)
		return 1
	}
	path := filepath.Join(dir, filepath.Base(pick.Target))
	if err := os.WriteFile(path, latest.Content, 0o600); err != nil {
		a.errorf("edit: %s", err)
		return 1
	}
	shown := latest.Content
	for {
		if err := a.editor(path); err != nil {
			a.errorf("edit: editor: %s", err)
			return 1
		}
		content, err := os.ReadFile(path)
		if err != nil {
			a.errorf("edit: %s", err)
			return 1
		}
		if bytes.Equal(content, shown) {
			if bytes.Equal(content, latest.Content) {
				fmt.Fprintln(a.stdout, status("unchanged", pick.Target))
				return 0
			}
			fmt.Fprintln(a.stdout, "edit aborted; nothing saved")
			return 1
		}
		if err := validateEntry(st, pick.Target, content, pick.ID); err != nil {
			a.errorf("edit: %s; fix it, or quit without changes to abort", err)
			shown = content
			continue
		}
		return a.saveEdit(st, pick, content, pos[0])
	}
}

// editFromStdin saves standard input as the entry's new version when it
// validates. Input equal to the stored version saves nothing; empty or
// invalid input is refused with nothing saved.
func (a *app) editFromStdin(st *lockbox.Store, pick *lockbox.Entry, stored []byte, name string) int {
	content, err := io.ReadAll(a.stdin)
	if err != nil {
		a.errorf("edit: read standard input: %s", err)
		return 1
	}
	if len(bytes.TrimSpace(content)) == 0 {
		a.errorf("edit: standard input is empty; nothing saved")
		return 1
	}
	if bytes.Equal(content, stored) {
		fmt.Fprintln(a.stdout, status("unchanged", pick.Target))
		return 0
	}
	if err := validateEntry(st, pick.Target, content, pick.ID); err != nil {
		a.errorf("edit: %s; nothing saved", err)
		return 1
	}
	return a.saveEdit(st, pick, content, name)
}

// saveEdit stores content as the entry's new version and writes the store.
func (a *app) saveEdit(st *lockbox.Store, pick *lockbox.Entry, content []byte, name string) int {
	if _, err := st.AddVersion(pick.ID, content, a.host); err != nil {
		a.errorf("edit: %s", err)
		return 1
	}
	if err := a.save(st, "edit "+name); err != nil {
		a.errorf("edit: %s", err)
		return 1
	}
	fmt.Fprintln(a.stdout, status("updated", pick.Target))
	return 0
}
