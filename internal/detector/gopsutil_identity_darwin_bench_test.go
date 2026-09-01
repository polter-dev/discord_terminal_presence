//go:build darwin

package detector

import "testing"

func BenchmarkDarwinListIdentities(b *testing.B) {
	b.Run("Gopsutil", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := listGopsutilIdentitiesForDarwinTest(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Bulk", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := listDarwinIdentities(); err != nil {
				b.Fatal(err)
			}
		}
	})
}
