package handler

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type failingStreamWriter struct {
	header http.Header
	writes int
}

func (w *failingStreamWriter) Header() http.Header {
	return w.header
}

func (w *failingStreamWriter) WriteHeader(int) {}

func (w *failingStreamWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("downstream closed")
}

type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

func TestStreamUpstreamResponseStopsOnDownstreamWriteFailure(t *testing.T) {
	body := &closeTrackingBody{Reader: strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       body,
	}
	w := &failingStreamWriter{header: make(http.Header)}

	result := (&GatewayHandler{}).streamUpstreamResponse(w, resp, "genfity/test", "openai")
	if !result.DownstreamWriteFailed {
		t.Fatal("downstream write failure was not reported")
	}
	if !strings.Contains(string(result.Body), "hello") {
		t.Fatalf("provider payload was not captured before the failed write: %q", result.Body)
	}
	if !body.closed {
		t.Fatal("upstream body was not closed immediately")
	}
	if w.writes != 1 {
		t.Fatalf("writes=%d, want exactly one failed downstream write", w.writes)
	}
}

func TestStreamUpstreamResponseConsumesProviderStartedMarker(t *testing.T) {
	body := &closeTrackingBody{Reader: strings.NewReader(providerStartedSSEMarker + "data: {\"choices\":[{\"delta\":{},\"finish_reason\":null}]}\n\n")}
	resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}
	w := &recordingStreamWriter{header: make(http.Header)}

	result := (&GatewayHandler{}).streamUpstreamResponse(w, resp, "genfity/test", "openai")
	if !result.ProviderStarted {
		t.Fatal("provider-started marker was not detected")
	}
	if strings.Contains(w.body.String(), "genfity-provider-started") {
		t.Fatalf("internal marker leaked downstream: %q", w.body.String())
	}
}

type recordingStreamWriter struct {
	header http.Header
	body   strings.Builder
}

func (w *recordingStreamWriter) Header() http.Header { return w.header }
func (w *recordingStreamWriter) WriteHeader(int)     {}
func (w *recordingStreamWriter) Write(p []byte) (int, error) {
	return w.body.Write(p)
}
