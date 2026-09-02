// Package run executes external commands. The tool drives docker and the
// engine's CLIs as subprocesses rather than through an SDK, because the command
// line is what a user can reproduce by hand -- which is worth something for a
// tool whose --verbose output is meant to teach the assembly (ADR-001).
package run

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

type Runner struct {
	Verbose bool
	Log     io.Writer // where --verbose echoes the command; may be nil
}

type Result struct {
	Cmd      string
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
	TimedOut bool
}

// Run executes name with args, bounded by ctx. It returns a Result for any
// outcome the operating system reported, and an error only when the command
// could not be started at all.
func (r *Runner) Run(ctx context.Context, name string, args ...string) (*Result, error) {
	line := name + " " + strings.Join(args, " ")
	if r.Verbose && r.Log != nil {
		fmt.Fprintf(r.Log, "+ %s\n", strings.TrimSpace(line))
	}
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb

	start := time.Now()
	err := cmd.Run()
	res := &Result{
		Cmd:      strings.TrimSpace(line),
		Stdout:   out.String(),
		Stderr:   errb.String(),
		Duration: time.Since(start),
		TimedOut: ctx.Err() == context.DeadlineExceeded,
	}
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			res.ExitCode = ee.ExitCode()
			return res, nil
		}
		return res, err // could not start: binary missing, permission denied
	}
	return res, nil
}

func asExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}
