package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// readyEnv names, in the environment of a --daemon child, the descriptor
// on which it says that it serves. It closes it either way: a child that
// fails closes it by exiting.
const readyEnv = "HERDREX_READY_FD"

// readyMsg is what a --daemon child writes once it serves.
const readyMsg = "ready\n"

// daemonize runs this command again in the background, in a session of its
// own so that closing the terminal does not end it, with its output
// appended to logFile. It returns once the child serves, or when it has
// failed, with what the child logged.
func daemonize(logFile string) error {
	if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
		return err
	}
	lf, err := os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	start, err := lf.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), readyEnv+"=3")
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.ExtraFiles = []*os.File{w}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	w.Close()
	if err != nil {
		return err
	}
	msg, _ := io.ReadAll(r)
	if string(msg) != readyMsg {
		cmd.Wait()
		if out, err := readFrom(logFile, start); err == nil {
			os.Stderr.Write(out)
		}
		return fmt.Errorf("herdrex did not start in the background; its log is %s", logFile)
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	log.Printf("herdrex runs in the background as pid %d, logging to %s", pid, logFile)
	log.Printf("stop it with: kill %d", pid)
	return nil
}

// daemonReady is the descriptor a --daemon child reports on, or nil when
// this process is not one. It keeps the descriptor from the processes this
// one starts, such as a Herdr server, which would otherwise hold it open
// after this one has closed it.
func daemonReady() *os.File {
	v, ok := os.LookupEnv(readyEnv)
	if !ok {
		return nil
	}
	os.Unsetenv(readyEnv)
	fd, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	syscall.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), "ready")
}

// readFrom returns what file holds from offset on.
func readFrom(file string, offset int64) ([]byte, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

// daemonLog is the default log file of --daemon for a bridge on port.
func daemonLog(port uint16) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", errors.New("--daemon: no cache directory for its log; give one with --log")
	}
	return filepath.Join(dir, "herdrex", "herdrex-"+strconv.Itoa(int(port))+".log"), nil
}
