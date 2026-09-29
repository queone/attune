package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/queone/gkit/color"
	"github.com/queone/gkit/lockbox"
)

// configName is the entry name of the configuration file.
const configName = "attune.yaml"

// storeSource names where the store path came from.
type storeSource string

const (
	sourceFlag    storeSource = "flag"
	sourceEnv     storeSource = "env"
	sourcePointer storeSource = "pointer"
	sourceDefault storeSource = "default"
)

// storeRef is the resolved store path and how it was chosen.
type storeRef struct {
	path   string
	source storeSource
}

// pointerFile is where init -t remembers the store path on this Mac.
func (a *app) pointerFile() string { return filepath.Join(a.env.ConfigHome, "attune", "store") }

// defaultStore is the store path when nothing else names one.
func (a *app) defaultStore() string { return filepath.Join(a.env.DataHome, "attune", "attune.store") }

func absPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// rememberedStore returns the path init -t remembered, or "" when none is
// recorded.
func (a *app) rememberedStore() string {
	b, err := os.ReadFile(a.pointerFile())
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(b))
	if p == "" {
		return ""
	}
	abs, err := absPath(p)
	if err != nil {
		return ""
	}
	return abs
}

// resolveStore applies the order: -t, ATTUNE_STORE, the remembered path, the default.
func (a *app) resolveStore(flag string) (storeRef, error) {
	if flag != "" {
		abs, err := absPath(flag)
		return storeRef{abs, sourceFlag}, err
	}
	if a.storeEnv != "" {
		abs, err := absPath(a.storeEnv)
		return storeRef{abs, sourceEnv}, err
	}
	if p := a.rememberedStore(); p != "" {
		return storeRef{p, sourcePointer}, nil
	}
	return storeRef{a.defaultStore(), sourceDefault}, nil
}

// writePointer records path so later commands find the store without -t.
func (a *app) writePointer(path string) error {
	if err := os.MkdirAll(filepath.Dir(a.pointerFile()), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(a.pointerFile(), []byte(path+"\n"), 0o600); err != nil {
		return err
	}
	return os.Chmod(a.pointerFile(), 0o600)
}

// rememberIfFlagged writes the pointer when the store came from -t.
func (a *app) rememberIfFlagged(ref storeRef) int {
	if ref.source != sourceFlag {
		return 0
	}
	if err := a.writePointer(ref.path); err != nil {
		a.errorf("remember store path: %s", err)
		return 1
	}
	fmt.Fprintf(a.stdout, "store path remembered in %s\n", a.pointerFile())
	return 0
}

type flagSpec struct {
	short string
	long  string
	value bool
}

// parseArgs separates known flags from positionals. A boolean flag maps to
// "true"; a value flag maps to its value, given as the next argument or
// after "=".
func parseArgs(args []string, specs []flagSpec) (map[string]string, []string, error) {
	flags := map[string]string{}
	var pos []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			pos = append(pos, arg)
			continue
		}
		name, inline, hasInline := strings.Cut(arg, "=")
		var spec *flagSpec
		for k := range specs {
			if name == specs[k].short || name == specs[k].long {
				spec = &specs[k]
				break
			}
		}
		if spec == nil {
			return nil, nil, fmt.Errorf("unknown flag %s", name)
		}
		if !spec.value {
			if hasInline {
				return nil, nil, fmt.Errorf("%s takes no value", name)
			}
			flags[spec.long] = "true"
			continue
		}
		if hasInline {
			flags[spec.long] = inline
			continue
		}
		if i+1 >= len(args) {
			return nil, nil, fmt.Errorf("%s needs a value", name)
		}
		flags[spec.long] = args[i+1]
		i++
	}
	return flags, pos, nil
}

// splitStoreFlag pulls -t/--store out of a store verb's arguments wherever
// it appears.
func splitStoreFlag(args []string) (string, []string, error) {
	var store string
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, inline, hasInline := strings.Cut(arg, "=")
		if name != "-t" && name != "--store" {
			rest = append(rest, arg)
			continue
		}
		if hasInline {
			store = inline
			continue
		}
		if i+1 >= len(args) {
			return "", nil, fmt.Errorf("%s needs a PATH", name)
		}
		store = args[i+1]
		i++
	}
	return store, rest, nil
}

// readStoreHeader reads the store file at path and parses its header.
func readStoreHeader(path string) ([]byte, lockbox.Header, error) {
	file, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, lockbox.Header{}, fmt.Errorf("no store at %s; run `attune init -N -t PATH` to create one, or `attune init -t PATH` to unlock an existing store", path)
	}
	if err != nil {
		return nil, lockbox.Header{}, err
	}
	hdr, err := lockbox.ParseHeader(file)
	if err != nil {
		return nil, lockbox.Header{}, fmt.Errorf("%s: %w", path, err)
	}
	return file, hdr, nil
}

// openStore loads the store with the key from the keychain.
func (a *app) openStore(path string) (*lockbox.Store, error) {
	_, hdr, err := readStoreHeader(path)
	if err != nil {
		return nil, err
	}
	key, err := a.keys.Get(lockbox.KeyIDString(hdr.KeyID))
	if errors.Is(err, lockbox.ErrKeyNotFound) {
		return nil, fmt.Errorf("no key for %s in the login keychain; run `attune init` and enter the recovery passphrase", path)
	}
	if err != nil {
		return nil, err
	}
	st, err := lockbox.Load(path, key)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if copies := lockbox.ConflictCopies(path); len(copies) > 0 {
		a.errorf("warning: sync conflict copies beside the store: %s", strings.Join(copies, ", "))
	}
	if rec, state, err := st.CheckLastSave(a.lastSaveOf(st)); err == nil && state == lockbox.SaveLost {
		a.errorf("warning: this Mac's last save (%s at %s) is missing from the store; another Mac's copy replaced it. Run it again.",
			rec.Action, rec.At.Local().Format(time.DateTime))
	}
	return st, nil
}

// lastSaveFile is where this Mac records its last save to the store with keyID.
func (a *app) lastSaveFile(keyID string) string {
	return filepath.Join(a.env.StateHome, "attune", "last-save-"+keyID)
}

// lastSaveOf is the last-save record path for the loaded store st.
func (a *app) lastSaveOf(st *lockbox.Store) string {
	return a.lastSaveFile(lockbox.KeyIDString(st.Header.KeyID))
}

// save writes the store with a save-log row naming action, then records the
// save as this Mac's last one. A record that cannot be written only warns.
func (a *app) save(st *lockbox.Store, action string) error {
	r, err := st.SaveLogged(a.host, action, a.now())
	if err != nil {
		return err
	}
	if err := lockbox.RecordLastSave(a.lastSaveOf(st), r); err != nil {
		a.errorf("warning: could not record this Mac's last save: %s", err)
	}
	return nil
}

// storeCommand runs one store verb after the store lookup.
func (a *app) storeCommand(verb string, args []string) int {
	storeFlag, rest, err := splitStoreFlag(args)
	if err != nil {
		a.errorf("%s; run `attune help`", err)
		return 2
	}
	ref, err := a.resolveStore(storeFlag)
	if err != nil {
		a.errorf("resolve store path: %s", err)
		return 1
	}
	switch verb {
	case "init":
		return a.cmdInit(ref, rest)
	case "st":
		return a.cmdSt(ref, rest)
	case "add":
		return a.cmdAdd(ref, rest)
	case "edit":
		return a.cmdEdit(ref, rest)
	case "rename":
		return a.cmdRename(ref, rest)
	case "rm":
		return a.cmdRm(ref, rest)
	case "ls":
		return a.cmdLs(ref, rest)
	case "cat":
		return a.cmdCat(ref, rest)
	case "render":
		return a.cmdRender(ref, rest)
	case "key":
		return a.cmdKey(ref, rest)
	}
	a.errorf("unknown command %q; run `attune help`", verb)
	return 2
}

// isSpecKey reports whether name is a valid entry name: attune.yaml, or a
// relative .yaml/.yml path with forward slashes and no dot segments.
func isSpecKey(name string) bool {
	if name == configName {
		return true
	}
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	ext := strings.ToLower(path.Ext(name))
	return ext == ".yaml" || ext == ".yml"
}

// entryFor selects the entry a name identifies, or nil.
func entryFor(all []lockbox.Entry, name string) *lockbox.Entry {
	for i := range all {
		if all[i].Target == name {
			return &all[i]
		}
	}
	return nil
}

// validateEntry checks content the way validate would once stored: the
// configuration must parse, and a spec must load together with every other
// stored spec. excludeID leaves out the entry being replaced.
func validateEntry(st *lockbox.Store, name string, content []byte, excludeID int64) error {
	if name == configName {
		_, err := ParseConfig(content)
		return err
	}
	all, err := st.Entries()
	if err != nil {
		return err
	}
	var sources []specSource
	for _, e := range all {
		if e.ID == excludeID || e.Target == configName || !isSpecKey(e.Target) {
			continue
		}
		latest, ok, err := st.Latest(e.ID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		sources = append(sources, specSource{name: e.Target, content: latest.Content})
	}
	sources = append(sources, specSource{name: name, content: content})
	_, err = loadSources(sources)
	return err
}

func (a *app) cmdInit(ref storeRef, args []string) int {
	flags, pos, err := parseArgs(args, []flagSpec{{"-N", "--new", false}})
	if err != nil || len(pos) > 0 {
		a.errorf("init: usage: attune init [-N] [-t PATH]")
		return 2
	}
	if flags["--new"] == "true" {
		return a.initNew(ref)
	}
	return a.initUnlock(ref)
}

// initUnlock puts an existing store's key into this Mac's keychain.
func (a *app) initUnlock(ref storeRef) int {
	_, hdr, err := readStoreHeader(ref.path)
	if err != nil {
		a.errorf("init: %s", err)
		return 1
	}
	keyID := lockbox.KeyIDString(hdr.KeyID)
	_, err = a.keys.Get(keyID)
	switch {
	case err == nil:
		fmt.Fprintf(a.stdout, "store at %s is already unlocked on this Mac\n", ref.path)
		return a.rememberIfFlagged(ref)
	case !errors.Is(err, lockbox.ErrKeyNotFound):
		a.errorf("init: %s", err)
		return 1
	}
	if code := a.restoreKey("init", ref.path, hdr); code != 0 {
		return code
	}
	return a.rememberIfFlagged(ref)
}

// restoreKey asks for the recovery passphrase, unwraps the key, and saves it.
func (a *app) restoreKey(verb, path string, hdr lockbox.Header) int {
	if !a.isTerminal() {
		a.errorf("%s needs a terminal", verb)
		return 1
	}
	pass, err := a.readSecret("Recovery passphrase for " + path + ": ")
	if err != nil {
		a.errorf("%s: read passphrase: %s", verb, err)
		return 1
	}
	key, err := hdr.UnwrapKey(pass)
	if err != nil {
		a.errorf("%s: %s", verb, err)
		return 1
	}
	if err := a.keys.Put(lockbox.KeyIDString(hdr.KeyID), key); err != nil {
		a.errorf("%s: %s", verb, err)
		return 1
	}
	fmt.Fprintf(a.stdout, "key for %s saved to the login keychain\n", path)
	return 0
}

// newPassphrase asks for a passphrase twice and returns it.
func (a *app) newPassphrase(verb string) ([]byte, int) {
	pass, err := a.readSecret("New recovery passphrase: ")
	if err != nil {
		a.errorf("%s: read passphrase: %s", verb, err)
		return nil, 1
	}
	if len(bytes.TrimSpace(pass)) == 0 {
		a.errorf("%s: passphrase must not be empty", verb)
		return nil, 1
	}
	again, err := a.readSecret("Repeat passphrase: ")
	if err != nil {
		a.errorf("%s: read passphrase: %s", verb, err)
		return nil, 1
	}
	if !bytes.Equal(pass, again) {
		a.errorf("%s: passphrases do not match", verb)
		return nil, 1
	}
	return pass, 0
}

// initNew creates a store, its key, and the passphrase-wrapped recovery copy.
func (a *app) initNew(ref storeRef) int {
	if _, err := os.Stat(ref.path); err == nil {
		a.errorf("init: a store already exists at %s", ref.path)
		return 1
	}
	parent := filepath.Dir(ref.path)
	if ref.source == sourceDefault {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			a.errorf("init: %s", err)
			return 1
		}
	} else if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		a.errorf("init: directory %s does not exist; create the synced folder first, or pass -t PATH to keep the store elsewhere", parent)
		return 1
	}
	if !a.isTerminal() {
		a.errorf("init needs a terminal")
		return 1
	}
	pass, code := a.newPassphrase("init")
	if code != 0 {
		return code
	}
	key, err := lockbox.NewKey()
	if err != nil {
		a.errorf("init: %s", err)
		return 1
	}
	id, err := lockbox.NewKeyID()
	if err != nil {
		a.errorf("init: %s", err)
		return 1
	}
	hdr := lockbox.Header{KeyID: id}
	if err := hdr.WrapKey(key, pass, a.kdf); err != nil {
		a.errorf("init: %s", err)
		return 1
	}
	st, err := lockbox.Create(ref.path, hdr, key)
	if err != nil {
		a.errorf("init: %s", err)
		return 1
	}
	defer st.Close()
	if err := a.save(st, "init"); err != nil {
		a.errorf("init: write %s: %s", ref.path, err)
		return 1
	}
	if err := a.keys.Put(lockbox.KeyIDString(id), key); err != nil {
		a.errorf("init: store written but the key was not saved: %s; run `attune init` and enter the passphrase", err)
		return 1
	}
	fmt.Fprintf(a.stdout, "store created at %s\nkey saved to the login keychain\nkeep the recovery passphrase safe: another Mac needs it once\n", ref.path)
	return a.rememberIfFlagged(ref)
}

// addEntry validates content, registers it under name, and captures it.
func (a *app) addEntry(st *lockbox.Store, name string, content []byte) error {
	if !isSpecKey(name) {
		return fmt.Errorf("%s is not a spec name; use attune.yaml or a relative .yaml/.yml path", name)
	}
	all, err := st.Entries()
	if err != nil {
		return err
	}
	if entryFor(all, name) != nil {
		return fmt.Errorf("%s is already registered; use `attune edit` to change it", name)
	}
	if err := validateEntry(st, name, content, 0); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	e, err := st.AddEntry(name, 0o600, "", lockbox.UnknownOwnership)
	if err != nil {
		return err
	}
	if _, err := st.AddVersion(e.ID, content, a.host); err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, status("added", name))
	return nil
}

func (a *app) cmdAdd(ref storeRef, args []string) int {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) == 0 || len(pos) > 2 {
		a.errorf("add: usage: attune add DIR, or attune add NAME [FILE]")
		return 2
	}
	st, err := a.openStore(ref.path)
	if err != nil {
		a.errorf("add: %s", err)
		return 1
	}
	defer st.Close()
	action := "add " + strings.Join(pos, " ")
	if len(pos) == 2 {
		content, err := os.ReadFile(pos[1])
		if err != nil {
			a.errorf("add: %s", err)
			return 1
		}
		return a.addAndSave(st, pos[0], content, action)
	}
	arg := pos[0]
	info, err := os.Stat(arg)
	switch {
	case err == nil && info.IsDir():
		return a.addDir(st, arg, action)
	case err == nil:
		a.errorf("add: %s is a file; use `attune add NAME FILE` to store it under a name, or `attune add DIR` to import a directory", arg)
		return 2
	}
	if !isSpecKey(arg) {
		a.errorf("add: %s is not a spec name; use attune.yaml or a relative .yaml/.yml path", arg)
		return 2
	}
	if a.isTerminal() {
		a.errorf("add: %s: give a FILE, or pipe the content on standard input", arg)
		return 2
	}
	content, err := io.ReadAll(a.stdin)
	if err != nil {
		a.errorf("add: read standard input: %s", err)
		return 1
	}
	return a.addAndSave(st, arg, content, action)
}

// addAndSave stores one entry and writes the store, logging action.
func (a *app) addAndSave(st *lockbox.Store, name string, content []byte, action string) int {
	if err := a.addEntry(st, name, content); err != nil {
		a.errorf("add: %s", err)
		return 1
	}
	if err := a.save(st, action); err != nil {
		a.errorf("add: %s", err)
		return 1
	}
	return 0
}

// addDir imports every .yaml/.yml file under dir, named relative to it, and
// logs the save as action.
func (a *app) addDir(st *lockbox.Store, dir, action string) int {
	var paths []string
	if err := collectSpecPaths(dir, &paths); err != nil {
		a.errorf("add: %s", err)
		return 1
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		a.errorf("add: %s holds no .yaml or .yml file", dir)
		return 1
	}
	rc, added := 0, false
	for _, p := range paths {
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			a.errorf("add: %s", err)
			rc = 1
			continue
		}
		content, err := os.ReadFile(p)
		if err != nil {
			a.errorf("add: %s", err)
			rc = 1
			continue
		}
		if err := a.addEntry(st, filepath.ToSlash(rel), content); err != nil {
			a.errorf("add: %s", err)
			rc = 1
			continue
		}
		added = true
	}
	if added {
		if err := a.save(st, action); err != nil {
			a.errorf("add: %s", err)
			return 1
		}
	}
	return rc
}

func (a *app) cmdRename(ref storeRef, args []string) int {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 2 {
		a.errorf("rename: usage: attune rename OLD NEW")
		return 2
	}
	newName := pos[1]
	if !isSpecKey(newName) {
		a.errorf("rename: %s is not a spec name; use attune.yaml or a relative .yaml/.yml path", newName)
		return 2
	}
	st, err := a.openStore(ref.path)
	if err != nil {
		a.errorf("rename: %s", err)
		return 1
	}
	defer st.Close()
	all, err := st.Entries()
	if err != nil {
		a.errorf("rename: %s", err)
		return 1
	}
	pick := entryFor(all, pos[0])
	if pick == nil {
		a.errorf("rename: %s is not registered", pos[0])
		return 1
	}
	err = st.SetTarget(pick.ID, newName)
	if errors.Is(err, lockbox.ErrExists) {
		a.errorf("rename: %s is already registered", newName)
		return 1
	}
	if err != nil {
		a.errorf("rename: %s", err)
		return 1
	}
	if err := a.save(st, "rename "+strings.Join(pos, " ")); err != nil {
		a.errorf("rename: %s", err)
		return 1
	}
	fmt.Fprintln(a.stdout, status("renamed", pick.Target+" -> "+newName))
	return 0
}

func (a *app) cmdRm(ref storeRef, args []string) int {
	_, pos, err := parseArgs(args, nil)
	if err != nil || len(pos) != 1 {
		a.errorf("rm: usage: attune rm NAME")
		return 2
	}
	st, err := a.openStore(ref.path)
	if err != nil {
		a.errorf("rm: %s", err)
		return 1
	}
	defer st.Close()
	all, err := st.Entries()
	if err != nil {
		a.errorf("rm: %s", err)
		return 1
	}
	pick := entryFor(all, pos[0])
	if pick == nil {
		a.errorf("rm: %s is not registered", pos[0])
		return 1
	}
	if err := st.RemoveEntry(pick.ID); err != nil {
		a.errorf("rm: %s", err)
		return 1
	}
	if err := a.save(st, "rm "+pos[0]); err != nil {
		a.errorf("rm: %s", err)
		return 1
	}
	fmt.Fprintf(a.stdout, "removed %s\n", pick.Target)
	return 0
}

// lsSorts names the fields ls can sort by.
var lsSorts = []string{"name", "captured"}

func (a *app) cmdLs(ref storeRef, args []string) int {
	flags, pos, err := parseArgs(args, []flagSpec{{"-b", "--by", true}})
	field := "name"
	if v, ok := flags["--by"]; ok {
		field = v
	}
	if err != nil || len(pos) > 0 || !slices.Contains(lsSorts, field) {
		a.errorf("ls: usage: attune ls [-b name|captured]")
		return 2
	}
	st, err := a.openStore(ref.path)
	if err != nil {
		a.errorf("ls: %s", err)
		return 1
	}
	defer st.Close()
	all, err := st.Entries()
	if err != nil {
		a.errorf("ls: %s", err)
		return 1
	}
	type lsRow struct {
		e        lockbox.Entry
		captured time.Time
	}
	items := make([]lsRow, 0, len(all))
	for _, e := range all {
		row := lsRow{e: e}
		if v, ok, err := st.Latest(e.ID); err == nil && ok {
			row.captured = v.CapturedAt
		}
		items = append(items, row)
	}
	slices.SortStableFunc(items, func(x, y lsRow) int {
		if field == "captured" {
			if c := y.captured.Compare(x.captured); c != 0 {
				return c
			}
		}
		return strings.Compare(x.e.Target, y.e.Target)
	})
	rows := [][]string{{"CAPTURED", "NAME"}}
	for _, it := range items {
		captured := "-"
		if !it.captured.IsZero() {
			captured = it.captured.Local().Format(time.DateTime)
		}
		rows = append(rows, []string{captured, it.e.Target})
	}
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], len(c))
		}
	}
	for ri, r := range rows {
		cells := make([]string, len(r))
		for i, c := range r {
			cell := c
			if i < len(r)-1 {
				cell = fmt.Sprintf("%-*s", widths[i], c)
			}
			if ri > 0 {
				cell = color.Gra4(cell)
			}
			cells[i] = cell
		}
		fmt.Fprintln(a.stdout, strings.Join(cells, "  "))
	}
	return 0
}

// statusWidth is the column where names start: the longest status word,
// "unchanged", plus two spaces.
const statusWidth = len("unchanged") + 2

// paint colors a whole line by the outcome its first word reports.
func paint(word, line string) string {
	switch word {
	case "unchanged":
		return color.Gra5(line)
	case "empty":
		return color.Yel5(line)
	case "added", "updated", "renamed":
		return color.Grn5(line)
	}
	return line
}

// status renders a padded, colored status line for an entry.
func status(word, name string) string {
	return paint(word, fmt.Sprintf("%-*s%s", statusWidth, word, name))
}

// cmdSt prints one status screen for the store, the key, and the entries.
// Drift against Azure is plan's job, so st never contacts Azure.
func (a *app) cmdSt(ref storeRef, args []string) int {
	if len(args) > 0 {
		a.errorf("st takes no arguments; run `attune help`")
		return 2
	}
	rc := 0
	// kv prints a plain label with a value; grey prints it in dark grey.
	kv := func(label, value string) { fmt.Fprintf(a.stdout, "%s: %s\n", label, value) }
	grey := func(label, value string) { kv(label, color.Gra4(value)) }
	grey("checked", a.now().Format(time.DateTime))
	grey("store", fmt.Sprintf("%s (%s)", ref.path, ref.source))
	if p := a.rememberedStore(); p != "" {
		grey("remembered", a.pointerFile()+" -> "+p)
	} else {
		grey("remembered", "none")
	}
	var st *lockbox.Store
	opens := "yes"
	file, err := os.ReadFile(ref.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		grey("store file", "missing")
		grey("key id", "unknown")
		grey("login keychain", "unknown")
		opens = "no (store file missing)"
	case err != nil:
		grey("store file", err.Error())
		grey("key id", "unknown")
		grey("login keychain", "unknown")
		opens = "no (" + err.Error() + ")"
	default:
		hdr, perr := lockbox.ParseHeader(file)
		if perr != nil {
			grey("store file", fmt.Sprintf("present, %d bytes, %s", len(file), perr))
			grey("key id", "unknown")
			grey("login keychain", "unknown")
			opens = "no (" + perr.Error() + ")"
			break
		}
		modified := "unknown"
		if info, serr := os.Stat(ref.path); serr == nil {
			modified = info.ModTime().Local().Format(time.DateTime)
		}
		grey("store file", fmt.Sprintf("present, %d bytes, generation %d, modified %s", len(file), hdr.Generation, modified))
		keyID := lockbox.KeyIDString(hdr.KeyID)
		grey("key id", keyID)
		key, kerr := a.keys.Get(keyID)
		switch {
		case errors.Is(kerr, lockbox.ErrKeyNotFound):
			grey("login keychain", "missing")
			opens = "no (key missing)"
		case kerr != nil:
			grey("login keychain", kerr.Error())
			opens = "no (" + kerr.Error() + ")"
		default:
			grey("login keychain", "present")
			loaded, lerr := lockbox.Load(ref.path, key)
			if lerr != nil {
				opens = "no (" + lerr.Error() + ")"
			} else {
				st = loaded
				defer st.Close()
			}
		}
	}
	if st == nil {
		rc = 1
		kv("store opens", color.Red5(opens))
	} else {
		kv("store opens", color.Grn5(opens))
	}
	if st == nil {
		grey("entries", "unknown")
	} else {
		all, err := st.Entries()
		if err != nil {
			a.errorf("st: %s", err)
			return 1
		}
		grey("entries", fmt.Sprintf("%d", len(all)))
	}
	if copies := lockbox.ConflictCopies(ref.path); len(copies) > 0 {
		kv("conflict copies", color.Yel5(strings.Join(copies, ", ")))
	} else {
		grey("conflict copies", "none")
	}
	if st == nil {
		grey("last save here", "unknown")
	} else {
		rec, state, err := st.CheckLastSave(a.lastSaveOf(st))
		when := rec.At.Local().Format(time.DateTime)
		switch {
		case err != nil:
			grey("last save here", "unknown ("+err.Error()+")")
		case state == lockbox.SaveNone:
			grey("last save here", "none")
		case state == lockbox.SaveInStore:
			kv("last save here", color.Grn5(fmt.Sprintf("in store (generation %d, %s)", rec.Generation, when)))
		case state == lockbox.SaveTooOld:
			grey("last save here", "unknown (older than the save log)")
		default:
			rc = 1
			kv("last save here", color.Red5(fmt.Sprintf("lost: %s at %s (generation %d); run it again", rec.Action, when, rec.Generation)))
		}
	}
	grey("drift", "run attune plan to compare with Azure")
	return rc
}
