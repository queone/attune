package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
// unchanged from what it last showed aborts with nothing saved.
func (a *app) cmdEdit(ref storeRef, args []string) int {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 1 {
		a.errorf("edit: usage: attune edit NAME")
		return 2
	}
	if !a.isTerminal() {
		a.errorf("edit needs a terminal")
		return 1
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
		if _, err := st.AddVersion(pick.ID, content, a.host); err != nil {
			a.errorf("edit: %s", err)
			return 1
		}
		if err := a.save(st, "edit "+pos[0]); err != nil {
			a.errorf("edit: %s", err)
			return 1
		}
		fmt.Fprintln(a.stdout, status("updated", pick.Target))
		return 0
	}
}
