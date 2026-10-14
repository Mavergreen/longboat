//go:build unix

package download

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestAnAlreadyCancelledGetDoesNothing: a signal cancels the command in
// progress. A Get whose context is already done must say so -- not
// download, create anything, or report success (a cancelled fetch must
// never report success).
func TestAnAlreadyCancelledGetDoesNothing(t *testing.T) {
	body := []byte("good")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write(body)
	}))
	defer srv.Close()
	g := getter(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := g.Get(ctx, Item{Name: "x", URL: srv.URL + "/x.zip", SHA256: sum(body)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(g.cache()); !os.IsNotExist(err) {
		t.Fatalf("a cancelled Get created something under %s (%v)", g.cache(), err)
	}
	if hits.Load() != 0 {
		t.Fatalf("a cancelled Get made %d request(s)", hits.Load())
	}
}

// TestCancellingMidHashReturnsPromptly: the cached file Get re-verifies
// is a FIFO that never ends, so hashing it can only stop by noticing the
// context. The writer keeps writing after cancelling, so a hash that
// ignored ctx would never return.
func TestCancellingMidHashReturnsPromptly(t *testing.T) {
	g := getter(t)
	want := sum([]byte("good"))
	fifo := g.file(want, "x.zip")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer w.Close()
		chunk := make([]byte, 64<<10)
		for n := 0; ; n++ {
			if n == 16 {
				cancel() // a megabyte in: mid-hash
			}
			if _, err := w.Write(chunk); err != nil {
				return // the reader gave up: EPIPE
			}
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, err := g.Get(ctx, Item{Name: "x", URL: "http://127.0.0.1:1/x.zip", SHA256: want})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Get kept hashing after its context was cancelled")
	}
	if ents, err := os.ReadDir(filepath.Dir(fifo)); err != nil || len(ents) != 1 {
		t.Fatalf("a cancelled Get left something beside %s behind (%d entries, %v)", fifo, len(ents), err)
	}
}

// TestCancellingMidDownloadLeavesNoTemp: the download's temp file is
// removed, and so is the cache directory made for it.
func TestCancellingMidDownloadLeavesNoTemp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 1<<16))
		w.(http.Flusher).Flush()
		cancel()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	g := getter(t)
	_, err := g.Get(ctx, Item{Name: "x", URL: srv.URL + "/x.zip", SHA256: sum([]byte("good"))})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(g.cache()); !os.IsNotExist(err) {
		t.Fatalf("a cancelled download left %s behind (%v)", g.cache(), err)
	}
}

// TestImportRefusesAFIFOWithoutOpeningIt: open(2) on a FIFO with no
// writer blocks, and a context cannot interrupt it.
func TestImportRefusesAFIFOWithoutOpeningIt(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	g := getter(t)
	done := make(chan error, 1)
	go func() {
		_, err := g.Import(context.Background(), Item{Name: "x", SHA256: sum([]byte("g"))}, fifo)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO was imported")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Import hung opening a FIFO")
	}
	if _, err := os.Stat(g.cache()); !os.IsNotExist(err) {
		t.Fatal("a refused import left something under the cache")
	}
}
