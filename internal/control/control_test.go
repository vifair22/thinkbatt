package control

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var errTest = errors.New("boom")

type fakeHandler struct {
	status      StatusInfo
	statusErr   error
	calibNow    bool
	calibCalled bool
	calibErr    error
	cancelErr   error
	bandStart   int
	bandStop    int
	bandErr     error
}

func (h *fakeHandler) Status() (StatusInfo, error) { return h.status, h.statusErr }
func (h *fakeHandler) Calibrate(now bool) error {
	h.calibCalled, h.calibNow = true, now
	return h.calibErr
}
func (h *fakeHandler) Cancel() error { return h.cancelErr }
func (h *fakeHandler) SetBand(start, stop int) error {
	h.bandStart, h.bandStop = start, stop
	return h.bandErr
}

func serve(t *testing.T, h Handler) (Client, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ctl.sock")
	srv := NewServer(path, h, nil)
	go srv.Serve() //nolint:errcheck // Close returns the result in tests that care
	// Wait for the socket to appear.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return Client{Path: path}, func() { _ = srv.Close() }
}

func TestStatusRoundTrip(t *testing.T) {
	h := &fakeHandler{status: StatusInfo{Capacity: 79, Status: "Not charging", BandStart: 75, BandStop: 80}}
	c, stop := serve(t, h)
	defer stop()

	resp, err := c.Do(Request{Cmd: CmdStatus})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !resp.OK {
		t.Fatalf("not ok: %s", resp.Error)
	}
	var info StatusInfo
	if err := json.Unmarshal(resp.Data, &info); err != nil {
		t.Fatal(err)
	}
	if info.Capacity != 79 || info.BandStop != 80 {
		t.Errorf("round trip mismatch: %+v", info)
	}
}

func TestCalibrateAndCancelAndBand(t *testing.T) {
	h := &fakeHandler{}
	c, stop := serve(t, h)
	defer stop()

	if resp, err := c.Do(Request{Cmd: CmdCalibrate, Args: map[string]string{"now": "true"}}); err != nil || !resp.OK {
		t.Fatalf("calibrate: %v %+v", err, resp)
	}
	if !h.calibCalled || !h.calibNow {
		t.Errorf("Calibrate(now=true) not forwarded")
	}
	if resp, err := c.Do(Request{Cmd: CmdCancel}); err != nil || !resp.OK {
		t.Fatalf("cancel: %v %+v", err, resp)
	}
	if resp, err := c.Do(Request{Cmd: CmdBand, Args: map[string]string{"start": "70", "stop": "85"}}); err != nil || !resp.OK {
		t.Fatalf("band: %v %+v", err, resp)
	}
	if h.bandStart != 70 || h.bandStop != 85 {
		t.Errorf("SetBand got %d/%d, want 70/85", h.bandStart, h.bandStop)
	}
}

func TestErrorResponses(t *testing.T) {
	c, stop := serve(t, &fakeHandler{})
	defer stop()

	if resp, _ := c.Do(Request{Cmd: "bogus"}); resp.OK || resp.Error == "" {
		t.Errorf("unknown command should error")
	}
	if resp, _ := c.Do(Request{Cmd: CmdBand, Args: map[string]string{"start": "x", "stop": "85"}}); resp.OK {
		t.Errorf("non-integer band should error")
	}
}

func TestMalformedRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ctl.sock")
	srv := NewServer(path, &fakeHandler{}, nil)
	go srv.Serve() //nolint:errcheck
	defer func() { _ = srv.Close() }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte("not json\n"))
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "malformed") {
		t.Errorf("expected malformed-request reply, got %q", buf[:n])
	}
}

func TestHandlerErrorPropagates(t *testing.T) {
	h := &fakeHandler{statusErr: errTest}
	c, stop := serve(t, h)
	defer stop()
	resp, err := c.Do(Request{Cmd: CmdStatus})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error == "" {
		t.Errorf("handler error should produce a not-ok response")
	}
}

func TestCloseBeforeServe(t *testing.T) {
	srv := NewServer(filepath.Join(t.TempDir(), "ctl.sock"), &fakeHandler{}, nil)
	if err := srv.Close(); err != nil {
		t.Errorf("Close before Serve: %v", err)
	}
}

func TestConnClosedBeforeRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ctl.sock")
	srv := NewServer(path, &fakeHandler{}, nil)
	go srv.Serve() //nolint:errcheck
	defer func() { _ = srv.Close() }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Connect and close without sending a request: the server's read errors and
	// the handler returns cleanly.
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	time.Sleep(20 * time.Millisecond)
}

func TestDialFailure(t *testing.T) {
	c := Client{Path: filepath.Join(t.TempDir(), "nonexistent.sock"), Timeout: 200 * time.Millisecond}
	if _, err := c.Do(Request{Cmd: CmdStatus}); err == nil {
		t.Errorf("dialing a missing socket should error")
	}
}
