package crayd

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var (
	slowStarted     = make(chan struct{}, 4)
	slowFinished    atomic.Bool
	hangRelease     = make(chan struct{})
	hangStarted     = make(chan struct{}, 4)
	hookSawFinished atomic.Bool
)

type shutCtl struct{ Controller }

// Slow takes 300ms and reports whether its request context stayed alive.
func (c *shutCtl) Slow() {
	slowStarted <- struct{}{}
	time.Sleep(300 * time.Millisecond)
	c.JSON(200, map[string]any{"done": true, "ctx_alive": c.Ctx().Err() == nil})
	slowFinished.Store(true)
}

// Hang never finishes until the test releases it.
func (c *shutCtl) Hang() {
	hangStarted <- struct{}{}
	<-hangRelease
	c.JSON(200, "late")
}

func newShutApp(t *testing.T) *App {
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	a := New(t.TempDir())
	a.Static("")
	a.Register("index", &shutCtl{})
	return a
}

type running struct {
	base   string
	cancel context.CancelFunc
	done   chan error
	client *http.Client
}

func startServe(t *testing.T, a *App) *running {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{
		base: "http://" + ln.Addr().String(), cancel: cancel, done: make(chan error, 1),
		client: &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second},
	}
	go func() { r.done <- a.Serve(ctx, ln) }()
	t.Cleanup(cancel)
	return r
}

func (r *running) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
		return nil
	}
}

func TestShutdownLetsRunningRequestsFinish(t *testing.T) {
	a := newShutApp(t)
	slowFinished.Store(false)
	hookSawFinished.Store(false)
	a.OnShutdown(func(context.Context) error {
		hookSawFinished.Store(slowFinished.Load()) // hooks must run after requests drain
		return nil
	})
	r := startServe(t, a)

	type result struct {
		code int
		body string
		err  error
	}
	res := make(chan result, 1)
	go func() {
		resp, err := r.client.Get(r.base + "/slow")
		if err != nil {
			res <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		res <- result{resp.StatusCode, string(b), nil}
	}()

	<-slowStarted
	r.cancel() // the request is running: tell the server to stop
	if err := r.wait(t); err != nil {
		t.Fatalf("clean shutdown returned %v", err)
	}
	got := <-res
	if got.err != nil || got.code != 200 || !strings.Contains(got.body, `"done":true`) {
		t.Fatalf("running request was cut off: %+v", got)
	}
	if !strings.Contains(got.body, `"ctx_alive":true`) {
		t.Fatalf("shutdown cancelled the running request's context: %s", got.body)
	}
	if !hookSawFinished.Load() {
		t.Fatal("OnShutdown hook ran before the running request finished")
	}
	if _, err := r.client.Get(r.base + "/"); err == nil {
		t.Fatal("server still accepts connections after shutdown")
	}
}

func TestShutdownTimeoutClosesStuckRequests(t *testing.T) {
	a := newShutApp(t)
	a.ShutdownTimeout = 100 * time.Millisecond
	var hook atomic.Bool
	a.OnShutdown(func(context.Context) error { hook.Store(true); return nil })
	r := startServe(t, a)
	defer close(hangRelease)

	stuck := make(chan error, 1)
	go func() {
		resp, err := r.client.Get(r.base + "/hang")
		if err == nil {
			resp.Body.Close()
		}
		stuck <- err
	}()
	<-hangStarted
	start := time.Now()
	r.cancel()
	err := r.wait(t)
	select { // the handler is still blocked, so only a forced close can end this request
	case cerr := <-stuck:
		if cerr == nil {
			t.Fatal("stuck request completed normally; its connection should have been closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stuck request's connection was left open after the shutdown timeout")
	}
	if err == nil || !strings.Contains(err.Error(), "shutdown") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	if !hook.Load() {
		t.Fatal("hooks must still run after a forced close")
	}
}

func TestShutdownHooksRunInReverseAndDoNotStopOnError(t *testing.T) {
	a := newShutApp(t)
	var order []string
	a.OnShutdown(func(context.Context) error { order = append(order, "db"); return nil })
	a.OnShutdown(func(context.Context) error { order = append(order, "redis"); return errors.New("redis: boom") })
	a.OnShutdown(func(context.Context) error { order = append(order, "cache"); return nil })
	r := startServe(t, a)
	r.cancel()
	err := r.wait(t)
	if strings.Join(order, ",") != "cache,redis,db" {
		t.Fatalf("order = %v", order)
	}
	if err == nil || !strings.Contains(err.Error(), "redis: boom") {
		t.Fatalf("hook error not reported: %v", err)
	}
}

func TestServeReturnsListenerErrorsAndStillRunsHooks(t *testing.T) {
	a := newShutApp(t)
	var hook atomic.Bool
	a.OnShutdown(func(context.Context) error { hook.Store(true); return nil })
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ln.Close() // broken listener
	err := a.Serve(context.Background(), ln)
	if err == nil || !hook.Load() {
		t.Fatalf("err = %v, hook ran = %v", err, hook.Load())
	}
}

func TestRunContextAddressInUse(t *testing.T) {
	a := newShutApp(t)
	var hook atomic.Bool
	a.OnShutdown(func(context.Context) error { hook.Store(true); return nil })
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	if err := a.RunContext(context.Background(), ln.Addr().String()); err == nil {
		t.Fatal("expected an 'address in use' error")
	}
	if !hook.Load() {
		t.Fatal("hooks must run when the server cannot start, so connections get closed")
	}
}
