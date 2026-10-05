package logs

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLinesPartialsAndStreams(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "c.log"), 0)
	if err != nil {
		t.Fatal(err)
	}
	out, errw := l.Writer("stdout"), l.Writer("stderr")
	out.Write([]byte("hel"))
	errw.Write([]byte("oops\n"))
	out.Write([]byte("lo\nno newline"))
	l.EndRun()
	es, _, _ := l.Read(ReadOptions{Tail: -1})
	got := ""
	for _, e := range es {
		got += e.Stream + ":" + e.Log + "|"
	}
	if got != "stderr:oops\n|stdout:hello\n|stdout:no newline|" {
		t.Fatalf("%q", got)
	}
	es, _, _ = l.Read(ReadOptions{Tail: 1})
	if len(es) != 1 || es[0].Log != "no newline" {
		t.Fatalf("tail: %+v", es)
	}
}

func TestRotationBoundsSize(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(filepath.Join(dir, "c.log"), 1024)
	w := l.Writer("stdout")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(w, "line %03d %s\n", i, strings.Repeat("x", 20))
	}
	l.EndRun()
	es, _, _ := l.Read(ReadOptions{Tail: -1})
	if len(es) == 0 || len(es) >= 200 || !strings.HasPrefix(es[len(es)-1].Log, "line 199") {
		t.Fatalf("kept %d entries, last %+v", len(es), es[len(es)-1])
	}
}

func TestFollowAndChunks(t *testing.T) {
	l, _ := Open(filepath.Join(t.TempDir(), "c.log"), 0)
	w := l.Writer("stdout")
	w.Write([]byte("old\n"))
	chunks := l.SubscribeChunks()
	stored, ch, _ := l.Read(ReadOptions{Tail: -1, Follow: true})
	w.Write([]byte("prompt$ "))
	if c := <-chunks; string(c.Data) != "prompt$ " {
		t.Fatalf("chunk %q", c.Data)
	}
	w.Write([]byte("new\n"))
	l.EndRun()
	var followed []string
	for e := range ch {
		followed = append(followed, e.Log)
	}
	if len(stored) != 1 || strings.Join(followed, "") != "prompt$ new\n" {
		t.Fatalf("stored %v followed %q", stored, followed)
	}
	if _, ok := <-chunks; ok {
		// drain the second chunk, then expect closure
		if _, ok := <-chunks; ok {
			t.Fatal("chunk channel not closed by EndRun")
		}
	}
}

func TestSinceFilter(t *testing.T) {
	l, _ := Open(filepath.Join(t.TempDir(), "c.log"), 0)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return t0 }
	l.Writer("stdout").Write([]byte("a\n"))
	l.now = func() time.Time { return t0.Add(time.Hour) }
	l.Writer("stdout").Write([]byte("b\n"))
	es, _, _ := l.Read(ReadOptions{Tail: -1, Since: t0.Add(time.Minute)})
	if len(es) != 1 || es[0].Log != "b\n" {
		t.Fatalf("%+v", es)
	}
}

func TestScanTailIsBoundedAndHandoffIsExact(t *testing.T) {
	l, _ := Open(filepath.Join(t.TempDir(), "c.log"), 0)
	w := l.Writer("stdout")
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(w, "line %04d\n", i)
	}
	var got []string
	live, err := l.Scan(ReadOptions{Tail: 3, Follow: true}, func(e Entry) error { got = append(got, strings.TrimSpace(e.Log)); return nil })
	if err != nil || strings.Join(got, ",") != "line 0997,line 0998,line 0999" {
		t.Fatalf("%v %v", got, err)
	}
	fmt.Fprint(w, "after scan\n")
	l.EndRun()
	var followed []string
	for e := range live {
		followed = append(followed, strings.TrimSpace(e.Log))
	}
	if strings.Join(followed, ",") != "after scan" {
		t.Fatalf("follow must start exactly where the scan ended, got %v", followed)
	}
	// Tail 0 and "all".
	n := 0
	l.Scan(ReadOptions{Tail: 0}, func(Entry) error { n++; return nil })
	if n != 0 {
		t.Fatal("tail 0 emitted entries")
	}
	l.Scan(ReadOptions{Tail: -1}, func(Entry) error { n++; return nil })
	if n != 1001 {
		t.Fatalf("all: %d", n)
	}
}

func TestScanMemoryBoundForHugeTail(t *testing.T) {
	l, _ := Open(filepath.Join(t.TempDir(), "c.log"), 0)
	w := l.Writer("stdout")
	big := strings.Repeat("x", 15000) + "\n"
	for i := 0; i < 2000; i++ { // ~30 MB of text
		fmt.Fprint(w, big)
	}
	l.EndRun()
	n := 0
	l.Scan(ReadOptions{Tail: 1 << 30}, func(e Entry) error { n++; return nil })
	if n*15000 > maxTailBytes+2*15000 || n == 0 {
		t.Fatalf("a tail request kept %d entries (%d bytes), cap %d", n, n*15000, maxTailBytes)
	}
}

func TestScanSlowConsumerDoesNotBlockWriters(t *testing.T) {
	l, _ := Open(filepath.Join(t.TempDir(), "c.log"), 0)
	w := l.Writer("stdout")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(w, "l%d\n", i)
	}
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		l.Scan(ReadOptions{Tail: -1}, func(Entry) error { <-release; return nil })
		close(done)
	}()
	time.Sleep(100 * time.Millisecond) // the consumer is now stuck in emit
	wrote := make(chan struct{})
	go func() { fmt.Fprint(w, "must not block\n"); close(wrote) }()
	select {
	case <-wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("a stalled reader blocked the container's output")
	}
	close(release)
	<-done
}
