package procguard

import "testing"

// BenchmarkCountRealProc measures one on-demand observation on this machine.
func BenchmarkCountRealProc(b *testing.B) {
	c := NewProcCounter()
	if _, err := c.Count(); err != nil {
		b.Skip(err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.Count()
	}
}
