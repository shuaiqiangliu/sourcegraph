package gitserver

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/neelance/chanrpc"
	"github.com/neelance/chanrpc/chanrpcutil"
	"github.com/prometheus/client_golang/prometheus"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/honey"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/repotrackutil"
	"sourcegraph.com/sourcegraph/sourcegraph/pkg/statsutil"
)

// Server is a gitserver server.
type Server struct {
	// ReposDir is the path to the base directory for gitserver storage.
	ReposDir string

	// InsecureSkipCheckVerifySSH controls whether the client verifies the
	// SSH server's certificate or host key. If InsecureSkipCheckVerifySSH
	// is true, the program is susceptible to a man-in-the-middle
	// attack. This should only be used for testing.
	InsecureSkipCheckVerifySSH bool

	// cloning tracks repositories (key is '/'-separated path) that are
	// in the process of being cloned.
	cloningMu sync.Mutex
	cloning   map[string]struct{}
}

// Serve serves incoming gitserver requests on listener l.
func (s *Server) Serve(l net.Listener) error {
	s.cloning = make(map[string]struct{})

	s.registerMetrics()
	requests := make(chan *request, 100)
	go s.processRequests(requests)
	srv := &chanrpc.Server{RequestChan: requests}
	return srv.Serve(l)
}

func (s *Server) processRequests(requests <-chan *request) {
	for req := range requests {
		if req.Exec != nil {
			go s.handleExecRequest(req.Exec)
		}
		if req.Create != nil {
			go s.handleCreateRequest(req.Create)
		}
		if req.Remove != nil {
			go s.handleRemoveRequest(req.Remove)
		}
	}
}

// handleExecRequest handles a exec request.
func (s *Server) handleExecRequest(req *execRequest) {
	start := time.Now()
	exitStatus := -10810 // sentinel value to indicate not set
	var stdoutN, stderrN int64
	var status string
	var errStr string

	defer recoverAndLog()
	defer close(req.ReplyChan)

	// Instrumentation
	{
		repo := repotrackutil.GetTrackedRepo(req.Repo)
		cmd := ""
		if len(req.Args) > 0 {
			cmd = req.Args[0]
		}
		execRunning.WithLabelValues(cmd, repo).Inc()
		defer func() {
			duration := time.Since(start)
			execRunning.WithLabelValues(cmd, repo).Dec()
			execDuration.WithLabelValues(cmd, repo, status).Observe(duration.Seconds())
			// Only log to honeycomb if we have the repo to reduce noise
			if ranGit := exitStatus != -10810; ranGit && honey.Enabled() {
				ev := honey.Event("gitserver-exec")
				ev.AddField("repo", req.Repo)
				ev.AddField("cmd", cmd)
				ev.AddField("args", strings.Join(req.Args, " "))
				ev.AddField("duration_ms", duration.Seconds()*1000)
				ev.AddField("stdout_size", stdoutN)
				ev.AddField("stderr_size", stderrN)
				ev.AddField("exit_status", exitStatus)
				if errStr != "" {
					ev.AddField("error", errStr)
				}
				ev.Send()
			}
		}()
	}

	dir := path.Join(s.ReposDir, req.Repo)
	s.cloningMu.Lock()
	_, cloneInProgress := s.cloning[dir]
	s.cloningMu.Unlock()
	if cloneInProgress {
		chanrpcutil.Drain(req.Stdin)
		req.ReplyChan <- &execReply{CloneInProgress: true}
		status = "clone-in-progress"
		return
	}
	if !repoExists(dir) {
		chanrpcutil.Drain(req.Stdin)
		req.ReplyChan <- &execReply{RepoNotFound: true}
		status = "repo-not-found"
		return
	}

	stdoutC, stdoutWRaw := chanrpcutil.NewWriter()
	stderrC, stderrWRaw := chanrpcutil.NewWriter()
	stdoutW := &writeCounter{w: stdoutWRaw}
	stderrW := &writeCounter{w: stderrWRaw}

	cmd := exec.Command("git", req.Args...)
	cmd.Dir = dir
	cmd.Stdin = chanrpcutil.NewReader(req.Stdin)
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	processResultChan := make(chan *processResult, 1)
	req.ReplyChan <- &execReply{
		Stdout:        stdoutC,
		Stderr:        stderrC,
		ProcessResult: processResultChan,
	}

	if err := s.runWithRemoteOpts(cmd, req.Opt); err != nil {
		errStr = err.Error()
	}
	if cmd.ProcessState != nil { // is nil if process failed to start
		exitStatus = cmd.ProcessState.Sys().(syscall.WaitStatus).ExitStatus()
	}

	chanrpcutil.Drain(req.Stdin)
	stdoutW.Close()
	stderrW.Close()

	processResultChan <- &processResult{
		Error:      errStr,
		ExitStatus: exitStatus,
	}
	close(processResultChan)
	status = strconv.Itoa(exitStatus)
	stdoutN = stdoutW.n
	stderrN = stderrW.n
}

var execRunning = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: "src",
	Subsystem: "gitserver",
	Name:      "exec_running",
	Help:      "number of gitserver.Command running concurrently.",
}, []string{"cmd", "repo"})
var execDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: "src",
	Subsystem: "gitserver",
	Name:      "exec_duration_seconds",
	Help:      "gitserver.Command latencies in seconds.",
	Buckets:   statsutil.UserLatencyBuckets,
}, []string{"cmd", "repo", "status"})

func init() {
	prometheus.MustRegister(execRunning)
	prometheus.MustRegister(execDuration)
}

// handleCreateRequest handles a create request.
func (s *Server) handleCreateRequest(req *createRequest) {
	start := time.Now()
	status := ""

	defer recoverAndLog()
	defer close(req.ReplyChan)
	defer func() { defer observeCreate(start, status) }()

	dir := path.Join(s.ReposDir, req.Repo)
	s.cloningMu.Lock()
	if _, ok := s.cloning[dir]; ok {
		s.cloningMu.Unlock()
		req.ReplyChan <- &createReply{CloneInProgress: true}
		status = "clone-in-progress"
		return
	}
	if repoExists(dir) {
		s.cloningMu.Unlock()
		req.ReplyChan <- &createReply{RepoExist: true}
		status = "repo-exists"
		return
	}

	// We'll take this repo and start cloning it.
	// Mark it as being cloned so no one else starts to.
	s.cloning[dir] = struct{}{}
	s.cloningMu.Unlock()

	defer func() {
		s.cloningMu.Lock()
		delete(s.cloning, dir)
		s.cloningMu.Unlock()
	}()

	if req.MirrorRemote != "" {
		cmd := exec.Command("git", "clone", "--mirror", req.MirrorRemote, dir)

		var outputBuf bytes.Buffer
		cmd.Stdout = &outputBuf
		cmd.Stderr = &outputBuf
		if err := s.runWithRemoteOpts(cmd, req.Opt); err != nil {
			req.ReplyChan <- &createReply{Error: fmt.Sprintf("cloning repository %s failed with output:\n%s", req.Repo, outputBuf.String())}
			status = "clone-fail"
			return
		}
		req.ReplyChan <- &createReply{}
		status = "clone-success"
		return
	}

	cmd := exec.Command("git", "init", "--bare", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		req.ReplyChan <- &createReply{Error: fmt.Sprintf("initializing repository %s failed with output:\n%s", req.Repo, string(out))}
		status = "init-fail"
		return
	}
	status = "init-success"
	req.ReplyChan <- &createReply{}
}

var createDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: "src",
	Subsystem: "gitserver",
	Name:      "create_duration_seconds",
	Help:      "gitserver.Init and gitserver.Clone latencies in seconds.",
	Buckets:   statsutil.UserLatencyBuckets,
}, []string{"status"})

func init() {
	prometheus.MustRegister(createDuration)
}

func observeCreate(start time.Time, status string) {
	createDuration.WithLabelValues(status).Observe(time.Since(start).Seconds())
}

// handleRemoveRequest handles a remove request.
func (s *Server) handleRemoveRequest(req *removeRequest) {
	status := ""

	defer recoverAndLog()
	defer close(req.ReplyChan)
	defer func() { defer observeRemove(status) }()

	dir := path.Join(s.ReposDir, req.Repo)
	s.cloningMu.Lock()
	_, cloneInProgress := s.cloning[dir]
	s.cloningMu.Unlock()
	if cloneInProgress {
		req.ReplyChan <- &removeReply{CloneInProgress: true}
		status = "clone-in-progress"
		return
	}
	if !repoExists(dir) {
		req.ReplyChan <- &removeReply{RepoNotFound: true}
		status = "repo-not-found"
		return
	}

	cmd := exec.Command("git", "remote")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		req.ReplyChan <- &removeReply{Error: fmt.Sprintf("not a repository: %s", req.Repo)}
		status = "not-a-repository"
		return
	}

	if err := os.RemoveAll(dir); err != nil {
		req.ReplyChan <- &removeReply{Error: err.Error()}
		status = "failed"
		return
	}
	req.ReplyChan <- &removeReply{}
	status = "success"
}

// Remove should be pretty much instant, so we just track counts.
var removeCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "src",
	Subsystem: "gitserver",
	Name:      "remove_total",
	Help:      "Total calls to gitserver.Remove",
}, []string{"status"})

func init() {
	prometheus.MustRegister(removeCounter)
}

func observeRemove(status string) {
	removeCounter.WithLabelValues(status).Inc()
}
