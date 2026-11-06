// Package harness drives the built pds-mcp binary the way an agent and a person do: it runs the
// login command, answers its browser step through a function the test supplies, and opens an MCP
// session over the binary's stdio. Tests built on it see only what leaves the process.
package harness

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	buildOnce sync.Once
	binary    string
	buildErr  error
)

// Binary builds cmd/pds-mcp once per test process, with a stamped version, and returns its path.
func Binary(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "pds-mcp-bin-")
		if err != nil {
			buildErr = err

			return
		}
		binary = filepath.Join(dir, "pds-mcp")
		cmd := exec.CommandContext(context.Background(), "go", "build",
			"-ldflags", "-X main.version="+Version, "-o", binary, "github.com/percona/pds/apps/mcp/cmd/pds-mcp")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %w\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}

	return binary
}

// Version is the version the test binary is stamped with.
const Version = "9.8.7-test"

// Browse is the person's side of a login: it is given the address the command printed and must
// finish the sign in at the control plane, ending with the request to the loopback listener.
type Browse func(ctx context.Context, address string) error

// Machine is one computer: a home for the login file and the control plane its programs use.
type Machine struct {
	Bin    string
	Config string
	URL    string
	// Env is added to every run.
	Env []string
}

// NewMachine returns a computer with no sign in saved, pointing at a control plane.
func NewMachine(t testing.TB, controlPlaneURL string) *Machine {
	t.Helper()

	return &Machine{Bin: Binary(t), Config: t.TempDir(), URL: controlPlaneURL}
}

// LoginFile is where the command keeps its login on this machine.
func (m *Machine) LoginFile() string {
	return filepath.Join(m.Config, "pds-mcp", "credentials.json")
}

func (m *Machine) command(ctx context.Context, args []string, extra []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, m.Bin, args...)
	cmd.Env = append(cleanEnv(), "PDS_URL="+m.URL, "XDG_CONFIG_HOME="+m.Config, "HOME="+m.Config)
	cmd.Env = append(cmd.Env, m.Env...)
	cmd.Env = append(cmd.Env, extra...)

	return cmd
}

// cleanEnv is the test process's environment without anything pds-mcp reads, so a developer's own
// settings never reach a test.
func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PDS_") || strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
			continue
		}
		env = append(env, kv)
	}

	return env
}

// Result is what a finished command left.
type Result struct {
	Stdout, Stderr string
	Code           int
}

// Run runs a command to its end with stdin closed.
func (m *Machine) Run(ctx context.Context, args []string, extraEnv ...string) Result {
	cmd := m.command(ctx, args, extraEnv)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A pipe, as an agent gives the bridge: an empty file would look like a terminal.
	cmd.Stdin = strings.NewReader("")
	err := cmd.Run()

	code := 0
	if err != nil {
		code = -1
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		}
	}

	return Result{Stdout: stdout.String(), Stderr: stderr.String(), Code: code}
}

// Login runs pds-mcp login --no-browser, which prints the address a person would open, and hands
// that address to browse. It returns what the command printed.
func (m *Machine) Login(ctx context.Context, browse Browse, args ...string) (Result, error) {
	cmd := m.command(ctx, append([]string{"login", "--no-browser"}, args...), nil)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}

	var stdout bytes.Buffer
	scanner := bufio.NewScanner(out)
	browsed := make(chan error, 1)
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		var sent bool
		for scanner.Scan() {
			line := scanner.Text()
			stdout.WriteString(line + "\n")
			if !sent && strings.HasPrefix(line, "http") {
				sent = true
				go func() { browsed <- browse(ctx, line) }()
			}
		}
		if !sent {
			browsed <- fmt.Errorf("the command printed no address: %q", stdout.String())
		}
	}()

	berr := <-browsed
	// Every read of the pipe ends before Wait, and before stdout is read here.
	<-scanned
	werr := cmd.Wait()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if werr != nil {
		res.Code = 1
		if exit, ok := werr.(*exec.ExitError); ok {
			res.Code = exit.ExitCode()
		}
	}
	if berr != nil {
		return res, berr
	}

	return res, nil
}

// Session is an MCP client connected to the bridge over its stdio.
type Session struct {
	*sdk.ClientSession
	stderr *bytes.Buffer
	mu     sync.Mutex
}

// Stderr is everything the bridge has logged so far.
func (s *Session) Stderr() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.stderr.String()
}

type lockedWriter struct {
	s *Session
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()

	return w.s.stderr.Write(p)
}

// Connect starts the bridge as an agent would and completes the MCP handshake. The error it
// returns is the one the agent would see.
func (m *Machine) Connect(ctx context.Context, extraEnv ...string) (*Session, error) {
	return m.connect(ctx, nil, nil, extraEnv)
}

// ConnectData starts the local data server, pds-mcp data, as an agent would. The options are the
// client's own, so a test can make it declare URL mode elicitation.
func (m *Machine) ConnectData(ctx context.Context, opts *sdk.ClientOptions, extraEnv ...string) (*Session, error) {
	return m.connect(ctx, []string{"data"}, opts, extraEnv)
}

func (m *Machine) connect(ctx context.Context, args []string, opts *sdk.ClientOptions, extraEnv []string) (*Session, error) {
	cmd := m.command(ctx, args, extraEnv)
	s := &Session{stderr: &bytes.Buffer{}}
	cmd.Stderr = lockedWriter{s}

	client := sdk.NewClient(&sdk.Implementation{Name: "pds-blackbox", Version: "1.0"}, opts)
	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	session, err := client.Connect(connectCtx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return s, err
	}
	s.ClientSession = session

	return s, nil
}
