package lsp

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/sourcegraph/jsonrpc2"
)

// ServerOpts ...
type ServerOpts struct {
	// Path to the LSP server executable.
	Path string

	// Args are additional arguments passed to the LSP server at launch.
	Args []string
}

// ErrStopped is returned when a RPC method is called on a stopped Server.
var ErrStopped = errors.New("stopped server")

// NewServer ...
func NewServer() (*Server, error) {
	return &Server{
		lock: &sync.Mutex{},
		stop: make(chan struct{}, 1),
	}, nil
}

type handler struct {
}

func (h *handler) Handle(ctx context.Context, c *jsonrpc2.Conn, r *jsonrpc2.Request) {
	switch r.Method {
	default:
	}
}

type rwc struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

func (r *rwc) Read(p []byte) (int, error) {
	return r.stdout.Read(p)
}

func (r *rwc) Write(p []byte) (int, error) {
	return r.stdin.Write(p)
}

func (r *rwc) Close() error {
	// Don't close, because really the Server owns these files.
	return nil
}

// Server is an instance of a LSP server process.
type Server struct {
	cmd  *exec.Cmd
	lock *sync.Mutex
	stop chan struct{}

	conn *jsonrpc2.Conn

	in  io.WriteCloser
	out io.ReadCloser
}

func (s *Server) rwc() *rwc {
	return &rwc{
		stdin:  s.in,
		stdout: s.out,
	}
}

func (s *Server) reset() {
	if s.in != nil {
		s.in.Close()
		s.in = nil
	}

	if s.out != nil {
		s.out.Close()
		s.out = nil
	}

	s.cmd = nil
}

func (s *Server) start(path string, args []string) error {
	var err error

	s.cmd = exec.Command(path, args...)
	s.cmd.Stderr = os.Stderr
	s.cmd.SysProcAttr = procattr()

	s.in, err = s.cmd.StdinPipe()
	if err != nil {
		s.reset()
		return err
	}

	s.out, err = s.cmd.StdoutPipe()
	if err != nil {
		s.reset()
		return err
	}

	err = s.cmd.Start()
	if err != nil {
		s.reset()
		return err
	}

	rpcOpt := []jsonrpc2.ConnOpt{}

	s.conn = jsonrpc2.NewConn(
		context.Background(),
		jsonrpc2.NewBufferedStream(s.rwc(), jsonrpc2.VSCodeObjectCodec{}),
		&handler{},
		rpcOpt...)

	return nil
}

// Start ...
func (s *Server) Start(opts *ServerOpts) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.cmd != nil {
		return errors.New("server already running")
	}

	if err := s.start(opts.Path, opts.Args); err != nil {
		return err
	}

	go func() {
		s.cmd.Wait()

		s.lock.Lock()
		defer s.lock.Unlock()

		s.reset()

		s.stop <- struct{}{}

		// TODO(jpeach): Send a notification that the server died and
		// the caller should reinitialize it.
	}()

	return nil
}

// Stop ...
func (s *Server) Stop() {
	s.lock.Lock()

	if s.cmd == nil {
		s.lock.Unlock()
		return
	}

	s.cmd.Process.Kill()
	s.lock.Unlock()
	<-s.stop
}

// Call ...
func (s *Server) Call(ctx context.Context, method string, params interface{}, result interface{}) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.cmd == nil {
		return ErrStopped
	}

	return s.conn.Call(ctx, method, params, result)
}

// Notify ...
func (s *Server) Notify(ctx context.Context, method string, params interface{}) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.cmd == nil {
		return ErrStopped
	}

	return s.conn.Notify(context.Background(), method, &params)
}
