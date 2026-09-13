package upnp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tr1v3r/rcast/internal/config"
	"github.com/tr1v3r/rcast/internal/player"
)

type blockingResponseWriter struct {
	header  http.Header
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingResponseWriter() *blockingResponseWriter {
	return &blockingResponseWriter{
		header:  make(http.Header),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (w *blockingResponseWriter) Header() http.Header { return w.header }
func (w *blockingResponseWriter) WriteHeader(int)     {}
func (w *blockingResponseWriter) Write(body []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(body), nil
}

func TestSlowSOAPWriterDoesNotBlockStopCommand(t *testing.T) {
	fake := newFakePlayer()
	st, cleanup := newAVTState(t, func() player.Player { return fake })
	defer cleanup()
	cfg := config.Config{}
	avt := AVTransportHandler(st, cfg)
	setupAVT(t, st, avt, "10.0.0.1:1", "https://example.test/movie.mp4")

	// SetVolume mutates state under commandMu, then blocks only while flushing
	// the buffered SOAP response to this deliberately slow client.
	slow := newBlockingResponseWriter()
	volumeReq := httptest.NewRequest(http.MethodPost, "/control", strings.NewReader(soapBody(`<DesiredVolume>33</DesiredVolume>`)))
	volumeReq.Header.Set("SOAPACTION", `"service#SetVolume"`)
	volumeReq.RemoteAddr = "10.0.0.1:1"
	volumeDone := make(chan struct{})
	go func() {
		RenderingControlHandler(st, cfg).ServeHTTP(slow, volumeReq)
		close(volumeDone)
	}()

	select {
	case <-slow.started:
	case <-time.After(time.Second):
		t.Fatal("slow response writer was never reached")
	}

	stopDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		stopDone <- serveAction(avt, "Stop", soapBody(""), "10.0.0.1:1")
	}()
	select {
	case response := <-stopDone:
		if response.Code != http.StatusOK {
			t.Fatalf("Stop status=%d body=%s", response.Code, response.Body.String())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop remained blocked behind slow SOAP response write")
	}

	close(slow.release)
	select {
	case <-volumeDone:
	case <-time.After(time.Second):
		t.Fatal("slow response did not finish after release")
	}
}
