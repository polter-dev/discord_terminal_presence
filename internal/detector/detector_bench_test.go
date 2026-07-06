package detector

import (
	"fmt"
	"testing"
	"time"
)

type benchmarkProcessLister struct {
	identities []Process
	enriched   []Process
	byPID      map[int32]Process
}

func newBenchmarkProcessLister(size int) *benchmarkProcessLister {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	matches := map[int]string{
		73:  "claude",
		241: "codex",
		517: "nvim",
		699: "htop",
	}

	lister := &benchmarkProcessLister{
		identities: make([]Process, 0, size),
		enriched:   make([]Process, 0, size),
		byPID:      make(map[int32]Process, size),
	}
	for i := 0; i < size; i++ {
		pid := int32(10_000 + i)
		name := fmt.Sprintf("worker-%03d", i)
		if match, ok := matches[i]; ok {
			name = match
		}
		identity := Process{
			Pid:     pid,
			Name:    name,
			Exe:     "/usr/local/bin/" + name,
			Cmdline: name + " --flag value",
			Argv0:   name,
		}
		enriched := identity
		enriched.Cwd = fmt.Sprintf("/workspace/project-%03d", i%17)
		enriched.CreateTime = base.Add(time.Duration(i) * time.Second)
		enriched.CPUTime = float64(i%31) + 0.25

		lister.identities = append(lister.identities, identity)
		lister.enriched = append(lister.enriched, enriched)
		lister.byPID[pid] = enriched
	}
	return lister
}

func (l *benchmarkProcessLister) List() ([]Process, error) {
	out := make([]Process, 0, len(l.enriched))
	for _, proc := range l.enriched {
		out = append(out, benchmarkEnrich(proc))
	}
	return out, nil
}

func (l *benchmarkProcessLister) ListIdentities() ([]Process, error) {
	return append([]Process(nil), l.identities...), nil
}

func (l *benchmarkProcessLister) Enrich(proc Process) Process {
	return benchmarkEnrich(l.byPID[proc.Pid])
}

func benchmarkEnrich(proc Process) Process {
	proc.Cwd = string([]byte(proc.Cwd))
	proc.Cmdline = string([]byte(proc.Cmdline))
	return proc
}

func BenchmarkScan(b *testing.B) {
	reg := testRegistry(b)
	lister := newBenchmarkProcessLister(800)
	selector := NewSelector(reg, Config{ActivitySwitching: true}, systemClock{})
	enricher := processEnricher(lister)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		processes, err := listProcesses(lister)
		if err != nil {
			b.Fatal(err)
		}
		detection := selector.SelectWithEnricher(processes, enricher)
		if detection.None {
			b.Fatal("expected active detection")
		}
	}
}

func BenchmarkActiveDetection(b *testing.B) {
	reg := testRegistry(b)
	processes, err := newBenchmarkProcessLister(800).List()
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detection := ActiveDetection(reg, processes)
		if detection.None {
			b.Fatal("expected active detection")
		}
	}
}
