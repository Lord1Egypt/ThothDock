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
