package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// writeRendered writes one rendered file with mode inside 0700 directories.
func writeRendered(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// renderDir resolves the destination: a fresh private temp directory, or -o DIR.
func renderDir(out string, force bool) (string, error) {
	if out == "" {
		return os.MkdirTemp("", "attune-render-")
	}
	dir, err := absPath(out)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		return dir, os.MkdirAll(dir, 0o700)
	case err != nil:
		return "", err
	case !info.IsDir():
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	names, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if len(names) > 0 && !force {
		return "", fmt.Errorf("%s is not empty; pass -f to write into it", dir)
	}
	return dir, nil
}

// cmdRender writes every entry's latest content at its name inside a
// browsable directory tree, so add DIR on that tree restores the same names.
func (a *app) cmdRender(ref storeRef, args []string) int {
	flags, pos, err := parseArgs(args, []flagSpec{{"-o", "--output", true}, {"-a", "--all", false}, {"-f", "--force", false}})
	if err != nil || len(pos) > 0 {
		a.errorf("render: usage: attune render [-o DIR] [-a] [-f]")
		return 2
	}
	all, force := flags["--all"] == "true", flags["--force"] == "true"
	st, err := a.openStore(ref.path)
	if err != nil {
		a.errorf("render: %s", err)
		return 1
	}
	defer st.Close()
	entries, err := st.Entries()
	if err != nil {
		a.errorf("render: %s", err)
		return 1
	}
	dir, err := renderDir(flags["--output"], force)
	if err != nil {
		a.errorf("render: %s", err)
		return 1
	}
	var rows []string
	n := 0
	for _, e := range entries {
		latest, ok, err := st.Latest(e.ID)
		if err != nil {
			a.errorf("render: %s: %s", e.Target, err)
			return 1
		}
		if !ok {
			fmt.Fprintln(a.stdout, status("empty", e.Target+" (nothing stored yet)"))
			continue
		}
		rel := filepath.FromSlash(e.Target)
		if err := writeRendered(filepath.Join(dir, rel), latest.Content, e.Mode); err != nil {
			a.errorf("render: %s: %s", rel, err)
			return 1
		}
		rows = append(rows, fmt.Sprintf("%s\t%d\t%s\t%s",
			e.Target, latest.Generation, latest.CapturedAt.UTC().Format(time.RFC3339), latest.SHA256))
		fmt.Fprintln(a.stdout, status("rendered", e.Target))
		n++
		if !all {
			continue
		}
		versions, err := st.Versions(e.ID)
		if err != nil {
			a.errorf("render: %s: %s", e.Target, err)
			return 1
		}
		for _, v := range versions {
			name := fmt.Sprintf("%d-%s", v.Generation, v.CapturedAt.UTC().Format("20060102T150405Z"))
			p := filepath.Join(dir, "versions", rel, name)
			if err := writeRendered(p, v.Content, e.Mode); err != nil {
				a.errorf("render: %s: %s", p, err)
				return 1
			}
		}
	}
	manifest := fmt.Sprintf("store: %s\ngeneration: %d\nrendered: %s\nnote: plaintext copies of the store; delete this directory when done\n\n",
		ref.path, st.Header.Generation, time.Now().UTC().Format(time.RFC3339))
	if len(rows) > 0 {
		manifest += strings.Join(rows, "\n") + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST.txt"), []byte(manifest), 0o600); err != nil {
		a.errorf("render: manifest: %s", err)
		return 1
	}
	fmt.Fprintf(a.stdout, "rendered %d file(s) into %s\n", n, dir)
	return 0
}

// cmdCat prints one entry's latest stored content to stdout, byte for byte.
func (a *app) cmdCat(ref storeRef, args []string) int {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 1 {
		a.errorf("cat: usage: attune cat NAME")
		return 2
	}
	st, err := a.openStore(ref.path)
	if err != nil {
		a.errorf("cat: %s", err)
		return 1
	}
	defer st.Close()
	all, err := st.Entries()
	if err != nil {
		a.errorf("cat: %s", err)
		return 1
	}
	pick := entryFor(all, pos[0])
	if pick == nil {
		a.errorf("cat: %s is not registered", pos[0])
		return 1
	}
	latest, ok, err := st.Latest(pick.ID)
	if err != nil {
		a.errorf("cat: %s", err)
		return 1
	}
	if !ok {
		a.errorf("cat: %s has nothing stored yet", pick.Target)
		return 1
	}
	if _, err := a.stdout.Write(latest.Content); err != nil {
		a.errorf("cat: %s", err)
		return 1
	}
	return 0
}
