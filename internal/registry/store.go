// store.go — file plumbing for the append-only JSONL registry.
//
// Kept out of api.go so that the frozen file shows exactly what is frozen: the
// types and the documented behaviour. Everything here is an implementation
// choice; the semantics it implements are the ones in api.go's doc comments (plus
// the Python reference in the dropped engine, forge/registry.py).
package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// rawLine is one non-empty physical line of the registry file.
type rawLine struct {
	no   int // 1-based physical line number, for human-readable problems
	text string
}

// formatDesignID is the design id of the n-th record ("D0001"), matching the
// Python reference (f"D{n:04d}"). Records and lineage edges are keyed by it.
func formatDesignID(n int) string { return fmt.Sprintf("D%04d", n) }

// nowUTC is the timestamp written into fresh records. The frozen doc requires
// UTC (the Python reference wrote local time with an offset; a record's
// timestamp is display metadata, not part of the score or of the parity schema).
func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// openRegistryFile prepares path for appending and returns the number of record
// lines it holds. The file (and its parent directory) is created when missing.
//
// Truncation repair: a killed process leaves a half-written JSON object at the
// end of the file, usually without its trailing newline. That fragment is
// dropped — the file is rewritten up to the last complete line, via a temp file
// and a rename so a crash mid-repair cannot destroy good history. Dropping it
// matters twice over: appending after it would glue a new record onto the
// fragment (making the new record unreadable), and counting it as a line would
// leave a permanent hole in experiment_id, turning the integrity gate red for a
// reason nobody can fix afterwards. Only bytes after the last complete line are
// ever touched; no complete record is rewritten, so the registry stays
// append-only where it matters.
func openRegistryFile(path string) (int, error) {
	if strings.TrimSpace(path) == "" {
		return 0, fmt.Errorf("registry: empty path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, fmt.Errorf("registry: create dir %s: %w", dir, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return 0, fmt.Errorf("registry: read %s: %w", path, err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			return 0, fmt.Errorf("registry: create %s: %w", path, err)
		}
		return 0, nil
	}

	lines := strings.Split(string(data), "\n")
	last := -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if json.Valid([]byte(strings.TrimSpace(ln))) {
			last = i
		}
	}
	kept := lines[:last+1] // last == -1 keeps nothing
	repaired := ""
	if last >= 0 {
		repaired = strings.Join(kept, "\n") + "\n"
	}
	if repaired != string(data) {
		if err := rewriteFile(path, []byte(repaired)); err != nil {
			return 0, err
		}
	}
	n := 0
	for _, ln := range kept {
		if strings.TrimSpace(ln) != "" {
			n++
		}
	}
	return n, nil
}

// rewriteFile replaces path's contents through a temp file + rename.
func rewriteFile(path string, content []byte) error {
	tmp := path + ".repair.tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return fmt.Errorf("registry: write repair tempfile %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("registry: repair %s: %w", path, err)
	}
	return nil
}

// readLines returns every non-empty line with its physical line number. The
// registry mutex is held for the duration so that an in-process Append cannot be
// observed half-written; a missing file is an empty registry.
func (r *Registry) readLines() ([]rawLine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, err := os.ReadFile(r.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("registry: read %s: %w", r.Path, err)
	}
	var out []rawLine
	for i, ln := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		out = append(out, rawLine{no: i + 1, text: ln})
	}
	return out, nil
}

// decodeRecord decodes one line; ok is false when the line is not a decodable
// record (a truncated tail, or a record whose types do not match the schema).
// Such a line is skipped rather than allowed to fail the whole read, exactly as
// the Python reference does on json.JSONDecodeError.
func decodeRecord(line string) (Record, bool) {
	var rec Record
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &rec); err != nil {
		return Record{}, false
	}
	return rec, true
}

// decodeObject decodes one line into raw keys, which is how Check() can see a
// MISSING field (a typed decode would silently yield a zero value instead).
func decodeObject(line string) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("not a JSON object")
	}
	return raw, nil
}

// encodeRecord renders one record as a single compact JSON line, with the same
// settings a Python writer would use (no HTML escaping, sorted map keys,
// trailing newline). Go marshals struct fields in declaration order and maps in
// key order, so the bytes are deterministic for a given record.
func encodeRecord(rec Record) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rec); err != nil { // Encode appends the '\n'
		return nil, fmt.Errorf("registry: encode record: %w", err)
	}
	return buf.Bytes(), nil
}

// missingRequiredKeys reports which of RequiredFields are absent from an encoded
// record. This is the Go side of the Python reference's append() guard
// (ValueError("registry record missing fields: ...")).
func missingRequiredKeys(line []byte) []string {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimRight(line, "\n"), &keys); err != nil {
		return append([]string(nil), RequiredFields...)
	}
	var missing []string
	for _, f := range RequiredFields {
		if _, ok := keys[f]; !ok {
			missing = append(missing, f)
		}
	}
	return missing
}

// appendLine appends one already-terminated line with a single write to a file
// opened O_APPEND, so a reader never sees a torn record.
func appendLine(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("registry: open %s: %w", path, err)
	}
	_, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("registry: append %s: %w", path, werr)
	}
	if cerr != nil {
		return fmt.Errorf("registry: close %s: %w", path, cerr)
	}
	return nil
}

// AppendAssign is Append plus the ids it assigned.
//
// WHY THIS EXISTS (addition to the frozen interface, flagged to the parent):
// runner.Score must return an EvalResult whose ExperimentID/DesignID are those of
// the record that was actually written — the search layer builds the lineage tree
// from DesignID (see search/api.go, "children are recorded with their parent's
// design_id"). The frozen Append(Record) error takes the record by value and so
// cannot hand the assigned ids back, and the only alternative — ask the counter
// with NextIDs() and then call Append() — is a data race: two goroutines would be
// given the same id. Allocating and writing under one lock is the only correct
// implementation. Append() delegates here, so there is exactly one write path.
func (r *Registry) AppendAssign(rec Record) (Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := r.n + 1
	if rec.ExperimentID == 0 {
		rec.ExperimentID = next
	}
	if rec.DesignID == "" {
		rec.DesignID = formatDesignID(next)
	}
	if rec.Timestamp == "" {
		rec.Timestamp = nowUTC()
	}
	line, err := encodeRecord(rec)
	if err != nil {
		return Record{}, err
	}
	if missing := missingRequiredKeys(line); len(missing) > 0 {
		return Record{}, fmt.Errorf("registry record missing fields: %v", missing)
	}
	if err := appendLine(r.Path, line); err != nil {
		return Record{}, err
	}
	r.n = next // only after a successful write: ids stay gap-free
	return rec, nil
}
