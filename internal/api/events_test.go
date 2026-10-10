package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/runtime"
)

type wireEvent struct {
	Type, Action, Status, ID, From string
	Actor                          struct {
		ID         string
		Attributes map[string]string
	}
	Time     int64
	TimeNano int64
}

func (f *apiFixture) pull(t *testing.T) {
	t.Helper()
	repo := f.image[:strings.LastIndex(f.image, ":")]
	resp, body := f.do(t, "POST", "/v1.41/images/create?fromImage="+repo+"&tag=1", "")
	expect(t, resp, body, 200, "Status:")
}

func (f *apiFixture) createRunning(t *testing.T, name, extra string) string {
	t.Helper()
	f.rt.Program("/bin/sh", func(ctx context.Context, _ runtime.Spec, _ io.Reader) int { <-ctx.Done(); return 0 })
	resp, body := f.do(t, "POST", "/v1.41/containers/create?name="+name,
		`{"Image":"`+f.image+`","Labels":{"com.docker.compose.project":"demo"}`+extra+`}`)
	expect(t, resp, body, 201, "Id")
	var created struct{ Id string }
	json.Unmarshal([]byte(body), &created)
	return created.Id
}

func TestEventsStreamLiveWithFilters(t *testing.T) {
	f := newAPI(t)
	f.pull(t)
	filters := url.QueryEscape(`{"type":["container"],"label":["com.docker.compose.project=demo"]}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", f.srv.URL+"/v1.41/events?filters="+filters, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	id := f.createRunning(t, "evweb", "")
	f.do(t, "POST", "/v1.41/containers/"+id+"/start", "")
	f.do(t, "POST", "/v1.41/containers/"+id+"/stop?t=1", "")
	sc := bufio.NewScanner(resp.Body)
	var actions []string
	for len(actions) < 5 && sc.Scan() {
		var ev wireEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("%v: %s", err, sc.Text())
		}
		if ev.Type != "container" || ev.Actor.ID != id || ev.ID != id || ev.Status != ev.Action || ev.From != f.image ||
			ev.Actor.Attributes["name"] != "evweb" || ev.TimeNano/1e9 != ev.Time {
			t.Fatalf("event %+v", ev)
		}
		actions = append(actions, ev.Action)
	}
	if got := strings.Join(actions, " "); got != "create start kill die stop" {
		t.Fatalf("actions %q", got)
	}
}

func TestEventsReplaySinceUntilAndBadInput(t *testing.T) {
	f := newAPI(t)
	f.pull(t)
	before := time.Now().Add(-time.Second)
	id := f.createRunning(t, "evr", "")
	f.do(t, "POST", "/v1.41/containers/"+id+"/start", "")
	after := time.Now().Add(time.Second)
	// since/until in the CLI's "seconds.nanoseconds" form: a finite replay.
	q := "since=" + strconv.FormatInt(before.Unix(), 10) + ".000000000&until=" + strconv.FormatInt(after.Unix(), 10)
	start := time.Now()
	resp, body := f.do(t, "GET", "/v1.41/events?"+q+"&filters="+url.QueryEscape(`{"event":["start"]}`), "")
	if time.Since(start) > 5*time.Second {
		t.Fatal("until did not end the stream")
	}
	expect(t, resp, body, 200, `"Action":"start"`)
	if strings.Contains(body, `"Action":"create"`) {
		t.Fatalf("event filter ignored: %s", body)
	}
	// The image pull is an event too.
	resp, body = f.do(t, "GET", "/v1.41/events?until="+strconv.FormatInt(after.Unix(), 10)+"&filters="+url.QueryEscape(`{"type":["image"]}`), "")
	expect(t, resp, body, 200, `"Action":"pull"`)
	resp, body = f.do(t, "GET", "/v1.41/events?filters="+url.QueryEscape(`{"bogus":["x"]}`), "")
	expect(t, resp, body, 400, "invalid filter")
	resp, body = f.do(t, "GET", "/v1.41/events?since=yesterday", "")
	expect(t, resp, body, 400, "invalid timestamp")
	f.do(t, "POST", "/v1.41/containers/"+id+"/stop?t=1", "")
}

func TestEventsWithoutSinceAreLiveOnly(t *testing.T) {
	f := newAPI(t)
	f.pull(t)
	old := f.createRunning(t, "old", "")
	f.do(t, "POST", "/v1.41/containers/"+old+"/start", "")
	f.do(t, "POST", "/v1.41/containers/"+old+"/stop?t=1", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", f.srv.URL+"/v1.41/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	fresh := f.createRunning(t, "fresh", "")
	sc := bufio.NewScanner(resp.Body)
	if !sc.Scan() {
		t.Fatal("no event")
	}
	var ev wireEvent
	json.Unmarshal(sc.Bytes(), &ev)
	if ev.Actor.ID != fresh || ev.Action != "create" {
		t.Fatalf("first event without since is %s %s, want the live create of %s", ev.Action, ev.Actor.ID, fresh)
	}
}

func TestUpdateRestartPolicyAndRefusedResources(t *testing.T) {
	f := newAPI(t)
	f.pull(t)
	id := f.createRunning(t, "upd", "")
	resp, body := f.do(t, "POST", "/v1.41/containers/"+id+"/update", `{"RestartPolicy":{"Name":"unless-stopped","MaximumRetryCount":0},"Memory":0}`)
	expect(t, resp, body, 200, "Warnings")
	resp, body = f.do(t, "GET", "/v1.41/containers/"+id+"/json", "")
	expect(t, resp, body, 200, `"RestartPolicy":{"Name":"unless-stopped"`)
	resp, body = f.do(t, "POST", "/v1.41/containers/"+id+"/update", `{"Memory":1048576}`)
	expect(t, resp, body, 501, "memory limits")
	resp, body = f.do(t, "POST", "/v1.41/containers/"+id+"/update", `{"RestartPolicy":{"Name":"always","MaximumRetryCount":2}}`)
	expect(t, resp, body, 400, "maximum retry count")
	resp, body = f.do(t, "POST", "/v1.41/containers/nope/update", `{}`)
	expect(t, resp, body, 404, "No such container")
	// A restart policy is accepted at create now.
	resp, body = f.do(t, "POST", "/v1.41/containers/create", `{"Image":"`+f.image+`","HostConfig":{"RestartPolicy":{"Name":"on-failure","MaximumRetryCount":3}}}`)
	expect(t, resp, body, 201, "Id")
}
