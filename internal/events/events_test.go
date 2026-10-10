package events

import (
	"fmt"
	"testing"
	"time"
)

func TestReplayThenLiveWithoutGapOrDuplicate(t *testing.T) {
	b := New()
	for i := 0; i < 3; i++ {
		b.Publish(Event{Type: "container", Action: fmt.Sprint(i)})
	}
	past, sub := b.Subscribe(time.Time{})
	defer sub.Close()
	if len(past) != 3 || past[0].Action != "0" || past[2].Action != "2" {
		t.Fatalf("replay = %+v", past)
	}
	b.Publish(Event{Type: "container", Action: "3"})
	if e := <-sub.C; e.Action != "3" {
		t.Fatalf("live = %+v", e)
	}
}

func TestSinceFiltersReplay(t *testing.T) {
	b := New()
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 5; i++ {
		b.Publish(Event{Action: fmt.Sprint(i), Time: base.Add(time.Duration(i) * time.Second)})
	}
	past, sub := b.Subscribe(base.Add(2 * time.Second))
	sub.Close()
	if len(past) != 2 || past[0].Action != "3" {
		t.Fatalf("since replay = %+v", past)
	}
}

func TestRingIsBounded(t *testing.T) {
	b := New()
	for i := 0; i < RingSize+10; i++ {
		b.Publish(Event{Action: fmt.Sprint(i)})
	}
	past, sub := b.Subscribe(time.Time{})
	sub.Close()
	if len(past) != RingSize || past[0].Action != "10" {
		t.Fatalf("ring kept %d events, first %q", len(past), past[0].Action)
	}
}

func TestEqualTimestampsKeepOrder(t *testing.T) {
	b := New()
	fixed := time.Unix(1_700_000_000, 0)
	b.now = func() time.Time { return fixed }
	b.Publish(Event{Action: "a"})
	b.Publish(Event{Action: "b"})
	past, sub := b.Subscribe(time.Time{})
	sub.Close()
	if !past[1].Time.After(past[0].Time) {
		t.Fatalf("timestamps not strictly increasing: %v %v", past[0].Time, past[1].Time)
	}
	// since = the first event's time must replay exactly the second.
	again, sub2 := b.Subscribe(past[0].Time)
	sub2.Close()
	if len(again) != 1 || again[0].Action != "b" {
		t.Fatalf("since replay = %+v", again)
	}
}

func TestSlowSubscriberIsDisconnectedNotBlocking(t *testing.T) {
	b := New()
	_, slow := b.Subscribe(time.Time{})
	_, fast := b.Subscribe(time.Time{})
	defer fast.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The fast subscriber takes each event before the next is
		// published; the slow one never reads.
		for i := 0; i < SubscriberBuffer+50; i++ {
			b.Publish(Event{Action: "x"})
			if _, ok := <-fast.C; !ok {
				t.Error("fast subscriber was disconnected")
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
	n := 0
	for range slow.C { // drains, then sees the close
		n++
	}
	if n != SubscriberBuffer || !slow.Dropped() {
		t.Fatalf("slow subscriber got %d events, dropped=%v", n, slow.Dropped())
	}
	if b.Subscribers() != 1 {
		t.Fatalf("subscribers = %d, want 1", b.Subscribers())
	}
}

func TestCloseTwiceIsSafe(t *testing.T) {
	b := New()
	_, s := b.Subscribe(time.Time{})
	s.Close()
	s.Close()
	if _, ok := <-s.C; ok {
		t.Fatal("channel open after Close")
	}
	b.Publish(Event{}) // no subscriber left: must not panic
}
