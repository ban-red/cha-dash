package sqlitedriver

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

const layoutSize = 1 << 20 // 1 MiB, the upper end of a real board

func forEachB(b *testing.B, f func(b *testing.B, v Variant)) {
	for _, v := range Variants() {
		b.Run(v.Name, func(b *testing.B) { f(b, v) })
	}
}

// Startup on first boot: create the file, open both pools, run the hub's
// migrations, close.
func BenchmarkOpenMigrate(b *testing.B) {
	forEachB(b, func(b *testing.B, v Variant) {
		dir := b.TempDir()
		i := 0
		for b.Loop() {
			i++
			st, err := Open(b.Context(), v, filepath.Join(dir, fmt.Sprintf("db%d", i)), testKEK)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := st.Migrate(b.Context()); err != nil {
				b.Fatal(err)
			}
			st.Close()
		}
	})
}

// Every later boot: open an existing database, find nothing to migrate,
// answer the first request.
func BenchmarkReopen(b *testing.B) {
	forEachB(b, func(b *testing.B, v Variant) {
		st, fx, path := seeded(b, v)
		if _, err := SaveLayout(b.Context(), st, fx.BoardID, 0, LayoutDoc(layoutSize, 1), fx.OwnerID); err != nil {
			b.Fatal(err)
		}
		st.Close()
		for b.Loop() {
			st, err := Open(b.Context(), v, path, testKEK)
			if err != nil {
				b.Fatal(err)
			}
			if n, err := st.Migrate(b.Context()); err != nil || n != 0 {
				b.Fatal(n, err)
			}
			if err := LookupSession(b.Context(), st, fx.TokenHashes[0]); err != nil {
				b.Fatal(err)
			}
			st.Close()
		}
	})
}

// A layout save: 1 MiB JSON, revision check, prune to 100 revisions.
func BenchmarkLayoutSave(b *testing.B) {
	forEachB(b, func(b *testing.B, v Variant) {
		st, fx, _ := seeded(b, v)
		doc := LayoutDoc(layoutSize, 1)
		b.SetBytes(int64(len(doc)))
		var rev int64
		for b.Loop() {
			next, err := SaveLayout(b.Context(), st, fx.BoardID, rev, doc, fx.OwnerID)
			if err != nil {
				b.Fatal(err)
			}
			rev = next
		}
	})
}

// Loading the latest 1 MiB layout, as first paint does.
func BenchmarkLayoutLoad(b *testing.B) {
	forEachB(b, func(b *testing.B, v Variant) {
		st, fx, _ := seeded(b, v)
		doc := LayoutDoc(layoutSize, 1)
		var rev int64
		for range 20 { // some history to skip over
			rev, _ = SaveLayout(b.Context(), st, fx.BoardID, rev, doc, fx.OwnerID)
		}
		b.SetBytes(int64(len(doc)))
		for b.Loop() {
			if _, n, err := LoadLayout(b.Context(), st, fx.BoardID); err != nil || n != len(doc) {
				b.Fatal(n, err)
			}
		}
	})
}

// A session lookup: the point read behind every authenticated request.
func BenchmarkSessionLookup(b *testing.B) {
	forEachB(b, func(b *testing.B, v Variant) {
		st, fx, _ := seeded(b, v)
		i := 0
		for b.Loop() {
			if err := LookupSession(b.Context(), st, fx.TokenHashes[i%len(fx.TokenHashes)]); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
}

// The same, from as many goroutines as -cpu allows, over the 4-reader pool.
func BenchmarkSessionLookupParallel(b *testing.B) {
	forEachB(b, func(b *testing.B, v Variant) {
		st, fx, _ := seeded(b, v)
		var n atomic.Int64
		b.RunParallel(func(pb *testing.PB) {
			i := int(n.Add(1)) * 7919
			for pb.Next() {
				if err := LookupSession(b.Context(), st, fx.TokenHashes[i%len(fx.TokenHashes)]); err != nil {
					b.Error(err)
					return
				}
				i++
			}
		})
	})
}

// One audit row per autocommit transaction.
func BenchmarkAuditAppend(b *testing.B) {
	forEachB(b, func(b *testing.B, v Variant) {
		st, _, _ := seeded(b, v)
		i := 0
		for b.Loop() {
			if err := AppendAudit(b.Context(), st, i); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
}

// One metrics poll of the dogfood home: 300 samples in one transaction.
func BenchmarkMetricsBatch(b *testing.B) {
	forEachB(b, func(b *testing.B, v Variant) {
		st, _, _ := seeded(b, v)
		for b.Loop() {
			if err := InsertMetrics(b.Context(), st, 300); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(b.N*300)/b.Elapsed().Seconds(), "rows/s")
	})
}

// Session lookups (ns/op) while a writer saves 1 MiB layouts back to back.
// saves/s is the writer's rate over the same window.
func BenchmarkReadDuringWrite(b *testing.B) {
	forEachB(b, func(b *testing.B, v Variant) {
		st, fx, _ := seeded(b, v)
		doc := LayoutDoc(layoutSize, 1)
		stop := make(chan struct{})
		var saves atomic.Int64
		var wg sync.WaitGroup
		wg.Go(func() {
			var rev int64
			for {
				select {
				case <-stop:
					return
				default:
				}
				next, err := SaveLayout(b.Context(), st, fx.BoardID, rev, doc, fx.OwnerID)
				if err != nil {
					b.Error(err)
					return
				}
				rev = next
				saves.Add(1)
			}
		})
		var n atomic.Int64
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := int(n.Add(1)) * 7919
			for pb.Next() {
				if err := LookupSession(b.Context(), st, fx.TokenHashes[i%len(fx.TokenHashes)]); err != nil {
					b.Error(err)
					return
				}
				i++
			}
		})
		b.StopTimer()
		close(stop)
		wg.Wait()
		b.ReportMetric(float64(saves.Load())/b.Elapsed().Seconds(), "saves/s")
	})
}
