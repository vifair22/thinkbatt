package control

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/user"
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
	calibBat    string
	calibCalled bool
	calibErr    error
	cancelErr   error
	bandBat     string
	bandStart   int
	bandStop    int
	bandErr     error
	ovBat       string
	ovTarget    int
	ovUntil     time.Time
	ovFull      bool
	ovErr       error
	clearBat    string
	clearCalled bool
	clearErr    error
}

func (h *fakeHandler) Status() (StatusInfo, error) { return h.status, h.statusErr }
func (h *fakeHandler) Calibrate(battery string, now bool) error {
	h.calibCalled, h.calibBat, h.calibNow = true, battery, now
	return h.calibErr
}
func (h *fakeHandler) Cancel() error { return h.cancelErr }
func (h *fakeHandler) SetBand(battery string, start, stop int) error {
	h.bandBat, h.bandStart, h.bandStop = battery, start, stop
	return h.bandErr
}

func (h *fakeHandler) SetOverride(battery string, target int, until time.Time, untilFull bool) error {
	h.ovBat, h.ovTarget, h.ovUntil, h.ovFull = battery, target, until, untilFull
	return h.ovErr
}
func (h *fakeHandler) ClearOverride(battery string) error {
	h.clearCalled, h.clearBat = true, battery
	return h.clearErr
}

func serve(t *testing.T, h Handler) (Client, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ctl.sock")
	srv := NewServer(path, "", h, nil)
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
	h := &fakeHandler{status: StatusInfo{Packs: []PackStatus{
		{Name: "BAT0", Capacity: 79, Status: "Not charging", BandStart: 75, BandStop: 80},
	}}}
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
	if len(info.Packs) != 1 || info.Packs[0].Capacity != 79 || info.Packs[0].BandStop != 80 {
		t.Errorf("round trip mismatch: %+v", info)
	}
}

func TestCalibrateAndCancelAndBand(t *testing.T) {
	h := &fakeHandler{}
	c, stop := serve(t, h)
	defer stop()

	if resp, err := c.Do(Request{Cmd: CmdCalibrate, Args: map[string]string{"now": "true", "battery": "BAT1"}}); err != nil || !resp.OK {
		t.Fatalf("calibrate: %v %+v", err, resp)
	}
	if !h.calibCalled || !h.calibNow || h.calibBat != "BAT1" {
		t.Errorf("Calibrate(battery=BAT1, now=true) not forwarded: called=%t now=%t bat=%q", h.calibCalled, h.calibNow, h.calibBat)
	}
	if resp, err := c.Do(Request{Cmd: CmdCancel}); err != nil || !resp.OK {
		t.Fatalf("cancel: %v %+v", err, resp)
	}
	if resp, err := c.Do(Request{Cmd: CmdBand, Args: map[string]string{"start": "70", "stop": "85", "battery": "BAT1"}}); err != nil || !resp.OK {
		t.Fatalf("band: %v %+v", err, resp)
	}
	if h.bandBat != "BAT1" || h.bandStart != 70 || h.bandStop != 85 {
		t.Errorf("SetBand got %q %d/%d, want BAT1 70/85", h.bandBat, h.bandStart, h.bandStop)
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
	srv := NewServer(path, "", &fakeHandler{}, nil)
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
	srv := NewServer(filepath.Join(t.TempDir(), "ctl.sock"), "", &fakeHandler{}, nil)
	if err := srv.Close(); err != nil {
		t.Errorf("Close before Serve: %v", err)
	}
}

func TestConnClosedBeforeRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ctl.sock")
	srv := NewServer(path, "", &fakeHandler{}, nil)
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

func TestApplyPerms(t *testing.T) {
	// Operate on a plain temp file; applyPerms only chmod/chowns s.path.
	f := filepath.Join(t.TempDir(), "sock")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// No group: world-accessible.
	if err := (&Server{path: f}).applyPerms(); err != nil {
		t.Fatalf("applyPerms (no group): %v", err)
	}

	// Unknown group: error.
	if err := (&Server{path: f, group: "no-such-group-zzz"}).applyPerms(); err == nil {
		t.Errorf("applyPerms with a nonexistent group should error")
	}

	// The caller's own primary group: chown to a group you belong to succeeds
	// as a non-root user, and as root (CI) chowning anywhere succeeds.
	cur, err := user.Current()
	if err != nil {
		t.Skip("cannot determine current user")
	}
	g, err := user.LookupGroupId(cur.Gid)
	if err != nil {
		t.Skip("cannot resolve current group")
	}
	if err := (&Server{path: f, group: g.Name}).applyPerms(); err != nil {
		t.Errorf("applyPerms with own group: %v", err)
	}
}

func TestDialFailure(t *testing.T) {
	c := Client{Path: filepath.Join(t.TempDir(), "nonexistent.sock"), Timeout: 200 * time.Millisecond}
	if _, err := c.Do(Request{Cmd: CmdStatus}); err == nil {
		t.Errorf("dialing a missing socket should error")
	}
}

func TestChargeAndReset(t *testing.T) {
	h := &fakeHandler{}
	c, stop := serve(t, h)
	defer stop()

	if resp, err := c.Do(Request{Cmd: CmdCharge, Args: map[string]string{"target": "100", "until": UntilFull, "battery": "BAT0"}}); err != nil || !resp.OK {
		t.Fatalf("charge until full: %v %+v", err, resp)
	}
	if h.ovBat != "BAT0" || h.ovTarget != 100 || !h.ovFull || !h.ovUntil.IsZero() {
		t.Errorf("SetOverride got %q %d full=%t until=%s", h.ovBat, h.ovTarget, h.ovFull, h.ovUntil)
	}

	deadline := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	if resp, err := c.Do(Request{Cmd: CmdCharge, Args: map[string]string{"target": "90", "until": deadline.Format(time.RFC3339)}}); err != nil || !resp.OK {
		t.Fatalf("charge until deadline: %v %+v", err, resp)
	}
	if h.ovTarget != 90 || h.ovFull || !h.ovUntil.Equal(deadline) {
		t.Errorf("SetOverride got %d full=%t until=%s, want 90 until %s", h.ovTarget, h.ovFull, h.ovUntil, deadline)
	}

	if resp, err := c.Do(Request{Cmd: CmdChargeReset, Args: map[string]string{"battery": "BAT1"}}); err != nil || !resp.OK {
		t.Fatalf("charge reset: %v %+v", err, resp)
	}
	if !h.clearCalled || h.clearBat != "BAT1" {
		t.Errorf("ClearOverride not forwarded: called=%t bat=%q", h.clearCalled, h.clearBat)
	}
}

func TestChargeErrors(t *testing.T) {
	h := &fakeHandler{ovErr: errTest, clearErr: errTest}
	c, stop := serve(t, h)
	defer stop()
	bad := []map[string]string{
		{"target": "x", "until": UntilFull},        // non-integer target
		{"target": "100", "until": "tomorrow-ish"}, // unparseable deadline
		{"target": "100", "until": UntilFull},      // handler error
	}
	for _, args := range bad {
		if resp, _ := c.Do(Request{Cmd: CmdCharge, Args: args}); resp.OK || resp.Error == "" {
			t.Errorf("charge %v should error", args)
		}
	}
	if resp, _ := c.Do(Request{Cmd: CmdChargeReset}); resp.OK {
		t.Errorf("charge reset handler error should propagate")
	}
}
