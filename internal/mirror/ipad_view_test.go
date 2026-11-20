package mirror

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScreenOnlyViewer(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("ETag", "\"original\"")
			io.WriteString(w, "<html><head></head><body><canvas id=c></canvas></body></html>")
		case "/touch":
			if r.Header.Get("Origin") != "http://"+r.Host {
				http.Error(w, "Origin mismatch", http.StatusForbidden)
				return
			}
			io.Copy(w, r.Body)
		case "/stream.bin":
			io.WriteString(w, "frame")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}
	}))
	defer upstream.Close()
	viewURL, closeViewer, err := startScreenOnlyViewer(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer closeViewer()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(viewURL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.Header.Get("ETag") != "" || !strings.Contains(string(body), screenOnlyView+"</head>") ||
		!strings.Contains(string(body), "<canvas id=c>") {
		t.Fatalf("viewer presentation was not added to the upstream page: %d, %v", resp.StatusCode, err)
	}

	req, err := http.NewRequest(http.MethodPost, viewURL+"touch", strings.NewReader("tap"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", strings.TrimSuffix(viewURL, "/"))
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != "tap" {
		t.Fatalf("viewer input failed: %d %q, %v", resp.StatusCode, body, err)
	}

	resp, err = client.Get(viewURL + "stream.bin")
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 5)
	_, err = io.ReadFull(resp.Body, frame)
	if err != nil || string(frame) != "frame" {
		resp.Body.Close()
		t.Fatalf("video data was not streamed immediately: %q, %v", frame, err)
	}
	closeViewer()
	resp.Body.Close()
	if _, err := client.Get(viewURL); err == nil {
		t.Fatal("viewer still listening after shutdown")
	}
}
