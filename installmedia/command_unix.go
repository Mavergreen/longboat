//go:build unix

package installmedia

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// commandWaitDelay bounds how long a command's leftovers may hold its
// output open once it has exited or been cancelled.
const commandWaitDelay = 10 * time.Second

// runCommand runs argv in a process group of its own, in dir, with
// exactly env and no stdin. A cancel kills the whole group, so a child
// it forked dies with it; so does anything still in the group once it
// exits, so nothing it left behind writes into dir after it is read.
// Its errors say what became of the command.
func runCommand(ctx context.Context, argv []string, dir string, env []string, o Options) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	cmd.WaitDelay = commandWaitDelay

	// The command's output goes to a file of ours, never to a pipe that
	// os/exec drains: a child the command left behind would hold that
	// pipe open, and Wait with it, until the WaitDelay ran out.
	var copied chan struct{}
	var pr, pw *os.File
	switch w := o.Output.(type) {
	case nil:
		// os/exec gives it /dev/null.
	case *os.File:
		cmd.Stdout, cmd.Stderr = w, w
	default:
		var err error
		if pr, pw, err = os.Pipe(); err != nil {
			return err
		}
		cmd.Stdout, cmd.Stderr = pw, pw
		copied = make(chan struct{})
		go func() {
			defer close(copied)
			copyOutput(w, pr, o.logf)
		}()
	}
	err := cmd.Start()
	if pw != nil {
		pw.Close()
	}
	if err == nil {
		err = cmd.Wait()
		// Whatever the command left in its group goes too.
		killGroup(cmd.Process.Pid)
	}
	if copied != nil {
		select {
		case <-copied:
		case <-time.After(commandWaitDelay):
		}
		pr.Close()
		<-copied
	}
	var ee *exec.ExitError
	switch {
	case err == nil:
		return nil
	case cmd.Process == nil:
		return fmt.Errorf("the command could not be started: %w", err)
	case errors.As(err, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return fmt.Errorf("the command was killed by signal %d (%v)", int(ws.Signal()), ws.Signal())
		}
		return fmt.Errorf("the command exited with status %d", ee.ExitCode())
	}
	return fmt.Errorf("the command failed: %w", err)
}

// killGroup sends SIGKILL to the process group pgid, which the command
// leads. Both callers may run after the leader has been reaped: the
// Cancel when a cancel lands just after Wait reaps it, and the kill of
// the leftovers after a normal exit. A group's id stays reserved while
// anything is left in the group, so the signal reaches exactly the
// leftovers; with none left, it finds no group. It could reach another
// group only if, in that instant, a new process got this same pid and
// made itself a group leader -- possible on a host that hands out pids
// at random, and on any host vanishingly unlikely in that window.
func killGroup(pgid int) error {
	err := syscall.Kill(-pgid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

// makeReadable makes the command's file 0644, through a descriptor that
// cannot be a symbolic link, and only if it is a regular file with no
// other hard link: a link to one of the user's own files would
// otherwise change that file's mode.
func makeReadable(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("could not be opened: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("is not a regular file")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("could not be inspected for hard links")
	}
	if uint64(st.Nlink) != 1 {
		return errors.New("has another hard link: it must be a file of its own")
	}
	return f.Chmod(0o644)
}
