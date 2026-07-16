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

	_, disconnected := (&GatewayHandler{}).streamUpstreamResponse(w, resp, "genfity/test", "openai")
	if !disconnected {
		t.Fatal("downstream write failure was not reported")
	}
	if !body.closed {
		t.Fatal("upstream body was not closed immediately")
	}
	if w.writes != 1 {
		t.Fatalf("writes=%d, want exactly one failed downstream write", w.writes)
	}
}
