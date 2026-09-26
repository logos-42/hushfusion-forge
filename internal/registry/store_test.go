package registry

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Open / append basics
// ---------------------------------------------------------------------------

func TestOpenCreatesAnEmptyRegistry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "registry.jsonl")
	reg, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if reg.Path != path {
		t.Errorf("Path = %q, want %q", reg.Path, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Open must create the file: %v", err)
	}
	if reg.Len() != 0 {
		t.Errorf("Len = %d, want 0", reg.Len())
	}
	if id, did := reg.NextIDs(); id != 1 || did != "D0001" {
		t.Errorf("NextIDs = (%d, %q), want (1, \"D0001\")", id, did)
	}
}

func TestAppendAssignsSequentialIDsAndAUTCTimestamp(t *testing.T) {
	reg := openTemp(t)
	for i := 1; i <= 3; i++ {
		if err := reg.Append(recordFixture(float64(i)*0.1, "random")); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if reg.Len() != 3 {
		t.Fatalf("Len = %d, want 3", reg.Len())
	}
	if id, did := reg.NextIDs(); id != 4 || did != "D0004" {
		t.Errorf("NextIDs = (%d, %q), want (4, \"D0004\")", id, did)
	}
	recs, err := reg.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("Records = %d, want 3", len(recs))
	}
	for i, rec := range recs {
		wantID := i + 1
		if rec.ExperimentID != wantID {
			t.Errorf("record %d: ExperimentID = %d, want %d", i, rec.ExperimentID, wantID)
		}
		if wantDesign := formatDesignID(wantID); rec.DesignID != wantDesign {
			t.Errorf("record %d: DesignID = %q, want %q", i, rec.DesignID, wantDesign)
		}
		ts, err := time.Parse(time.RFC3339, rec.Timestamp)
		if err != nil {
			t.Errorf("record %d: Timestamp %q is not RFC3339: %v", i, rec.Timestamp, err)
			continue
		}
		if ts.Location() != time.UTC {
			t.Errorf("record %d: Timestamp %q is not UTC", i, rec.Timestamp)
		}
	}
	if problems := readProblems(t, reg); len(problems) != 0 {
		t.Fatalf("Check after 3 appends: %v", problems)
	}
}

// TestAppendKeepsRecordsThatAlreadyCarryIDs pins the "when they are unset" half
// of the frozen Append doc: an explicitly identified record (an import, a
// re-scored baseline) is written as given.
func TestAppendKeepsRecordsThatAlreadyCarryIDs(t *testing.T) {
	reg := openTemp(t)
	rec := recordFixture(-1.0, "human_baseline")
	rec.ExperimentID = 1
	rec.DesignID = "D0001"
	rec.Timestamp = "2026-01-01T00:00:00Z"
	if err := reg.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}
	recs, err := reg.Records()
	if err != nil || len(recs) != 1 {
		t.Fatalf("Records = %v, %v; want one record", recs, err)
	}
	if recs[0].ExperimentID != 1 || recs[0].DesignID != "D0001" || recs[0].Timestamp != "2026-01-01T00:00:00Z" {
		t.Fatalf("preset identity was overwritten: %+v", recs[0])
	}
}

// TestAppendDoesNotAdvanceTheCounterOnFailure keeps the id sequence gap-free when
// a write cannot happen at all (the counter must not burn an id).
func TestAppendDoesNotAdvanceTheCounterOnFailure(t *testing.T) {
	reg := openTemp(t)
	if err := reg.Append(recordFixture(0.1, "random")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Make the registry path unwritable by turning it into a directory.
	if err := os.Remove(reg.Path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := os.Mkdir(reg.Path, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := reg.Append(recordFixture(0.2, "random")); err == nil {
		t.Fatalf("Append to an unusable path must fail")
	}
	if reg.Len() != 1 {
		t.Errorf("Len = %d after a failed append, want 1", reg.Len())
	}
	if id, did := reg.NextIDs(); id != 2 || did != "D0002" {
		t.Errorf("NextIDs = (%d, %q) after a failed append, want (2, \"D0002\")", id, did)
	}
}

// ---------------------------------------------------------------------------
// concurrency (the hard criterion: 100 goroutines -> exactly 100 records,
// contiguous ids, no gaps, no duplicates; run with -race)
// ---------------------------------------------------------------------------

func TestAppendGapFreeUnderConcurrency(t *testing.T) {
	reg := openTemp(t)
	const n = 100

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := reg.Append(recordFixture(float64(i)/100, "random")); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(errs) != 0 {
		t.Fatalf("%d appends failed, first: %v", len(errs), errs[0])
	}

	if got := reg.Len(); got != n {
		t.Fatalf("Len = %d, want %d", got, n)
	}
	recs, err := reg.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != n {
		t.Fatalf("Records = %d, want %d", len(recs), n)
	}
	byID := map[int]int{}
	byDesign := map[string]int{}
	for _, rec := range recs {
		byID[rec.ExperimentID]++
		byDesign[rec.DesignID]++
	}
	for want := 1; want <= n; want++ {
		if byID[want] != 1 {
			t.Errorf("experiment_id %d appears %d times, want exactly 1", want, byID[want])
		}
		if byDesign[formatDesignID(want)] != 1 {
			t.Errorf("design_id %s appears %d times, want exactly 1", formatDesignID(want), byDesign[formatDesignID(want)])
		}
	}
	if problems := readProblems(t, reg); len(problems) != 0 {
		t.Fatalf("Check after %d concurrent appends: %v", n, problems)
	}
	// One record per line, each terminated: the file itself must be clean.
	data, err := os.ReadFile(reg.Path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("registry file does not end with a newline")
	}
	if got := len(strings.Split(strings.TrimRight(string(data), "\n"), "\n")); got != n {
		t.Errorf("file has %d lines, want %d", got, n)
	}
}

// TestAppendAssignReturnsTheWrittenRecord is the criterion runner.Score's
// lineage depends on: the ids handed back are the ids of the record that reached
// the file, even when 100 goroutines are racing.
func TestAppendAssignReturnsTheWrittenRecord(t *testing.T) {
	reg := openTemp(t)
	const n = 100

	assigned := make([]Record, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := recordFixture(float64(i)/100, "evolution")
			got, err := reg.AppendAssign(rec)
			if err != nil {
				t.Errorf("AppendAssign: %v", err)
				return
			}
			assigned[i] = got
		}(i)
	}
	wg.Wait()

	recs, err := reg.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	onDisk := make(map[string]Record, len(recs))
	for _, rec := range recs {
		onDisk[rec.DesignID] = rec
	}
	seen := map[string]int{}
	seenID := map[int]int{}
	for i, got := range assigned {
		if got.ExperimentID <= 0 || got.DesignID == "" {
			t.Fatalf("goroutine %d got an unassigned record: %+v", i, got)
		}
		seen[got.DesignID]++
		seenID[got.ExperimentID]++
		rec, ok := onDisk[got.DesignID]
		if !ok {
			t.Fatalf("goroutine %d was handed design_id %s which is not in the file", i, got.DesignID)
		}
		if rec.ExperimentID != got.ExperimentID {
			t.Errorf("design %s: assigned experiment_id %d, file says %d", got.DesignID, got.ExperimentID, rec.ExperimentID)
		}
		if rec.Score != got.Score || rec.Algorithm != got.Algorithm {
			t.Errorf("design %s: assigned record does not match the file: %+v vs %+v", got.DesignID, got, rec)
		}
	}
	if len(seen) != n || len(seenID) != n {
		t.Fatalf("assigned ids are not unique: %d design ids, %d experiment ids, want %d", len(seen), len(seenID), n)
	}
	if len(recs) != n {
		t.Fatalf("registry holds %d records, want %d", len(recs), n)
	}
	if problems := readProblems(t, reg); len(problems) != 0 {
		t.Fatalf("Check: %v", problems)
	}
}

// TestConcurrentReadsDuringAppends documents that a reader either sees a record
// or does not; it never sees half of one (the registry mutex covers reads too).
func TestConcurrentReadsDuringAppends(t *testing.T) {
	reg := openTemp(t)
	const n = 50

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if err := reg.Append(recordFixture(float64(i), "random")); err != nil {
				t.Errorf("Append: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			recs, err := reg.Records()
			if err != nil {
				t.Errorf("Records: %v", err)
				return
			}
			for _, rec := range recs {
				if rec.DesignID == "" || rec.ExperimentID <= 0 {
					t.Errorf("reader observed a partially written record: %+v", rec)
					return
				}
			}
			_ = reg.Len()
			_ = reg.Summary()
		}
	}()
	wg.Wait()

	recs, err := reg.Records()
	if err != nil || len(recs) != n {
		t.Fatalf("Records = %d, %v; want %d records", len(recs), err, n)
	}
	if problems := readProblems(t, reg); len(problems) != 0 {
		t.Fatalf("Check: %v", problems)
	}
}

// ---------------------------------------------------------------------------
// truncation tolerance
// ---------------------------------------------------------------------------

func TestRecordsSkipsATruncatedFinalLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.jsonl")
	writeLines(t, path, []string{
		rawRecordLine(t, 1, "D0001", "human_baseline", -0.29),
		rawRecordLine(t, 2, "D0002", "random", 0.10),
		rawRecordLine(t, 3, "D0003", "evolution", 0.20),
		`{"experiment_id":4,"design_id":"D0004","algorithm":"evo`,
	}, true)

	reg := &Registry{Path: path}
	recs, err := reg.Records()
	if err != nil {
		t.Fatalf("Records must tolerate a truncated final line, got: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("Records = %d, want the 3 complete records", len(recs))
	}
	if problems := readProblems(t, reg); len(problems) != 0 {
		t.Fatalf("a truncated final line is tolerated, got: %v", problems)
	}
}

// TestOpenRepairsATruncatedTailThenKeepsIDsContiguous is the "process was killed
// mid-write" scenario end to end: the half-written line is dropped, the next
// record reuses the id that would have been used, and the gate stays green.
func TestOpenRepairsATruncatedTailThenKeepsIDsContiguous(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.jsonl")
	fragment := `{"experiment_id":4,"design_id":"D0004","algorithm":"evolution","seed":7,"score":0.3,"params":{},"terms":{},"metrics":{},"ta`
	// No trailing newline: exactly what a write cut short leaves behind.
	writeLines(t, path, []string{
		rawRecordLine(t, 1, "D0001", "human_baseline", -0.29),
		rawRecordLine(t, 2, "D0002", "random", 0.10),
		rawRecordLine(t, 3, "D0003", "evolution", 0.20),
	}, false)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString("\n" + fragment); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reg, err := Open(path)
	if err != nil {
		t.Fatalf("Open must tolerate a truncated final line: %v", err)
	}
	if reg.Len() != 3 {
		t.Fatalf("Len = %d, want 3 (the fragment is not a record)", reg.Len())
	}
	if id, did := reg.NextIDs(); id != 4 || did != "D0004" {
		t.Fatalf("NextIDs = (%d, %q), want (4, \"D0004\") — a phantom line would leave a permanent gap", id, did)
	}
	if problems := readProblems(t, reg); len(problems) != 0 {
		t.Fatalf("Check after repair: %v", problems)
	}

	// The fragment must be gone, not glued to: the next record has to be readable.
	rec, err := reg.AppendAssign(recordFixture(0.3, "evolution"))
	if err != nil {
		t.Fatalf("AppendAssign after repair: %v", err)
	}
	if rec.ExperimentID != 4 || rec.DesignID != "D0004" {
		t.Fatalf("assigned (%d, %q), want (4, \"D0004\")", rec.ExperimentID, rec.DesignID)
	}
	recs, err := reg.Records()
	if err != nil {
		t.Fatalf("Records after repair+append: %v", err)
	}
	if len(recs) != 4 {
		t.Fatalf("Records = %d, want 4 — the fragment corrupted the new record", len(recs))
	}
	if problems := readProblems(t, reg); len(problems) != 0 {
		t.Fatalf("Check after repair+append: %v", problems)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), fragment) {
		t.Fatalf("repair left the truncated fragment in the file:\n%s", data)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("repaired file does not end with a newline:\n%s", data)
	}
	if n := len(strings.Split(strings.TrimRight(string(data), "\n"), "\n")); n != 4 {
		t.Fatalf("file has %d lines, want 4:\n%s", n, data)
	}
}

// TestOpenFixesAMissingTrailingNewline covers the other half of the same hazard:
// a complete last record without its newline would otherwise have the next
// record appended onto it.
func TestOpenFixesAMissingTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.jsonl")
	writeLines(t, path, []string{
		rawRecordLine(t, 1, "D0001", "human_baseline", -0.29),
		rawRecordLine(t, 2, "D0002", "random", 0.10),
	}, false) // no trailing '\n'

	reg, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if reg.Len() != 2 {
		t.Fatalf("Len = %d, want 2", reg.Len())
	}
	if _, err := reg.AppendAssign(recordFixture(0.5, "random")); err != nil {
		t.Fatalf("AppendAssign: %v", err)
	}
	recs, err := reg.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("Records = %d, want 3 (the third record was glued to the second)", len(recs))
	}
	if problems := readProblems(t, reg); len(problems) != 0 {
		t.Fatalf("Check: %v", problems)
	}
}

// TestOpenLeavesAHealthyFileByteIdentical guards against a "repair" that rewrites
// history it has no business touching.
func TestOpenLeavesAHealthyFileByteIdentical(t *testing.T) {
	reg := openTemp(t)
	for i := 0; i < 3; i++ {
		if err := reg.Append(recordFixture(float64(i), "random")); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	before, err := os.ReadFile(reg.Path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if _, err := Open(reg.Path); err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	after, err := os.ReadFile(reg.Path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("Open rewrote a healthy registry:\nbefore: %s\nafter:  %s", before, after)
	}
	if _, err := os.Stat(reg.Path + ".repair.tmp"); !os.IsNotExist(err) {
		t.Errorf("repair left a temp file behind")
	}
}

func TestReopenCountsExistingRecordsAndContinuesNumberOfIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.jsonl")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := first.Append(recordFixture(float64(i), "random")); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	if second.Len() != 3 {
		t.Fatalf("Len after reopen = %d, want 3", second.Len())
	}
	rec, err := second.AppendAssign(recordFixture(9, "random"))
	if err != nil {
		t.Fatalf("AppendAssign: %v", err)
	}
	if rec.ExperimentID != 4 || rec.DesignID != "D0004" {
		t.Fatalf("reopened registry assigned (%d, %q), want (4, \"D0004\")", rec.ExperimentID, rec.DesignID)
	}
	if problems := readProblems(t, second); len(problems) != 0 {
		t.Fatalf("Check: %v", problems)
	}
}

// TestMissingRequiredKeysGuard covers the defensive guard inside AppendAssign.
// It is unreachable through Record today (every required key is a non-omitempty
// field), which is exactly why Check() validates the RAW line instead of a typed
// decode, and why the guard is a unit-tested function rather than a branch.
func TestMissingRequiredKeysGuard(t *testing.T) {
	full := `{"experiment_id":1,"design_id":"D0001","algorithm":"random","seed":7,"score":0.1,"params":{},"terms":{},"metrics":{}}`
	if missing := missingRequiredKeys([]byte(full)); len(missing) != 0 {
		t.Fatalf("complete record reported missing fields: %v", missing)
	}
	partial := `{"experiment_id":1,"design_id":"D0001","algorithm":"random","score":0.1,"params":{},"terms":{},"metrics":{}}`
	if missing := missingRequiredKeys([]byte(partial)); strings.Join(missing, ",") != "seed" {
		t.Fatalf("missing = %v, want [seed]", missing)
	}
	if missing := missingRequiredKeys([]byte(`not json`)); len(missing) != len(RequiredFields) {
		t.Fatalf("unreadable line should report every required field, got %v", missing)
	}
}
