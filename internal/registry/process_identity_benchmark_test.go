package registry

import "testing"

var benchmarkMatchCount int

func BenchmarkProcessIdentityDerivation(b *testing.B) {
	reg, err := New()
	if err != nil {
		b.Fatal(err)
	}
	corpus := processIdentityEquivalenceCorpus(reg.tools)

	b.Run("LegacyPerTool", func(b *testing.B) {
		b.ReportAllocs()
		matches := 0
		for b.Loop() {
			for _, process := range corpus {
				for _, tool := range reg.tools {
					if legacyToolMatchesProcess(tool, process) {
						matches++
					}
				}
			}
		}
		benchmarkMatchCount = matches
	})

	b.Run("HoistedPerProcess", func(b *testing.B) {
		b.ReportAllocs()
		matches := 0
		for b.Loop() {
			for _, process := range corpus {
				identity := processIdentityForMatch(process)
				for _, tool := range reg.tools {
					if tool.matchesProcess(identity) {
						matches++
					}
				}
			}
		}
		benchmarkMatchCount = matches
	})
}
