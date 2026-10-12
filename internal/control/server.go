package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
)

// Server accepts control connections on a unix socket and dispatches each to a
// Handler. It owns the socket file: Serve removes a stale one first, and Close
// stops accepting and removes it.
type Server struct {
	path string
	h    Handler
	log  *slog.Logger

	mu  sync.Mutex
	ln  net.Listener
	off bool
}

// NewServer builds a Server for the given socket path and handler.
func NewServer(path string, h Handler, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{path: path, h: h, log: log}
}

// Serve listens and handles connections until Close is called. A leftover
// socket from a previous run is removed first. It returns nil on a clean Close.
func (s *Server) Serve() error {
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket %s: %w", s.path, err)
	}
	ln, err := net.Listen("unix", s.path)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.path, err)
	}
	s.mu.Lock()
	s.ln = ln
	closed := s.off
	s.mu.Unlock()
	if closed { // Close raced in before we stored the listener
		_ = ln.Close()
		_ = os.Remove(s.path)
		return nil
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			off := s.off
			s.mu.Unlock()
			if off {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		go s.handle(conn)
	}
}

// Close stops the server and removes the socket.
func (s *Server) Close() error {
	s.mu.Lock()
	s.off = true
	ln := s.ln
	s.mu.Unlock()
	var err error
	if ln != nil {
		err = ln.Close()
	}
	_ = os.Remove(s.path)
	return err
}

func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		s.log.Warn("control: read request", "err", err)
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.reply(conn, Response{Error: "malformed request: " + err.Error()})
		return
	}
	s.reply(conn, s.dispatch(req))
}

func (s *Server) dispatch(req Request) Response {
	switch req.Cmd {
	case CmdStatus:
		info, err := s.h.Status()
		if err != nil {
			return errResp(err)
		}
		data, err := json.Marshal(info)
		if err != nil {
			return errResp(err)
		}
		return Response{OK: true, Data: data}

	case CmdCalibrate:
		now := req.Args["now"] == "true"
		if err := s.h.Calibrate(now); err != nil {
			return errResp(err)
		}
		return Response{OK: true}

	case CmdCancel:
		if err := s.h.Cancel(); err != nil {
			return errResp(err)
		}
		return Response{OK: true}

	case CmdBand:
		start, err := strconv.Atoi(req.Args["start"])
		if err != nil {
			return errResp(errors.New("band: start is not an integer"))
		}
		stop, err := strconv.Atoi(req.Args["stop"])
		if err != nil {
			return errResp(errors.New("band: stop is not an integer"))
		}
		if err := s.h.SetBand(start, stop); err != nil {
			return errResp(err)
		}
		return Response{OK: true}

	default:
		return errResp(fmt.Errorf("unknown command %q", req.Cmd))
	}
}

func (s *Server) reply(conn net.Conn, resp Response) {
	b, err := json.Marshal(resp)
	if err != nil {
		s.log.Error("control: encode response", "err", err)
		return
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		s.log.Warn("control: write response", "err", err)
	}
}

func errResp(err error) Response { return Response{Error: err.Error()} }
