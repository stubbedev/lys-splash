package kmsg

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"
)

type writes struct {
	records [][]byte
}

func (w *writes) Write(b []byte) (int, error) {
	w.records = append(w.records, bytes.Clone(b))
	return len(b), nil
}

func TestRecordsCarryThePriorityAndIdentifierJournaldParses(t *testing.T) {
	t.Parallel()
	var w writes
	log := slog.New(NewHandler(&w, 42, slog.LevelDebug))
	log.Debug("d")
	log.Info("i", "card", "card0")
	log.Warn("falling back to the default theme", "err", "bad colour")
	log.Error("e")
	want := []string{
		"<31>lys-splash[42]: level=DEBUG msg=d\n",
		"<30>lys-splash[42]: level=INFO msg=i card=card0\n",
		"<28>lys-splash[42]: level=WARN msg=\"falling back to the default theme\" err=\"bad colour\"\n",
		"<27>lys-splash[42]: level=ERROR msg=e\n",
	}
	if len(w.records) != len(want) {
		t.Fatalf("got %d writes, want one per record: %q", len(w.records), w.records)
	}
	for i, got := range w.records {
		if string(got) != want[i] {
			t.Errorf("record %d = %q, want %q", i, got, want[i])
		}
	}
}

func TestLevelFiltersRecords(t *testing.T) {
	t.Parallel()
	var w writes
	log := slog.New(NewHandler(&w, 1, slog.LevelInfo))
	log.Debug("hidden")
	if len(w.records) != 0 {
		t.Errorf("a debug record was written at info level: %q", w.records)
	}
}

func TestDerivedHandlersShareTheSink(t *testing.T) {
	t.Parallel()
	var w writes
	log := slog.New(NewHandler(&w, 1, slog.LevelInfo)).With("card", "card0").WithGroup("g")
	log.Info("m", "k", "v")
	want := "<30>lys-splash[1]: level=INFO msg=m card=card0 g.k=v\n"
	if len(w.records) != 1 || string(w.records[0]) != want {
		t.Errorf("records = %q, want %q", w.records, want)
	}
}

func TestLongRecordsFitTheKernelsLimitOnARuneBoundary(t *testing.T) {
	t.Parallel()
	var w writes
	log := slog.New(NewHandler(&w, 1, slog.LevelInfo))
	log.Info("m", "err", strings.Repeat("æ", maxRecord))
	if len(w.records) != 1 {
		t.Fatalf("got %d writes", len(w.records))
	}
	got := w.records[0]
	if len(got) > maxRecord {
		t.Errorf("the record is %d bytes, over the limit of %d", len(got), maxRecord)
	}
	if got[len(got)-1] != '\n' {
		t.Errorf("the record does not end in a newline: %q", got[len(got)-16:])
	}
	if !utf8.Valid(got) {
		t.Errorf("the record was cut inside a character: %q", got[len(got)-16:])
	}
}
