package ptyutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// Open opens a new pty pair and returns master and slave.
func Open() (master, slave *os.File, err error) {
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	n, err := unix.IoctlGetInt(int(ptmx.Fd()), syscall.TIOCGPTN)
	if err != nil {
		ptmx.Close()
		return nil, nil, err
	}
	// unlockpt
	unlock := 0
	if err := unix.IoctlSetPointerInt(int(ptmx.Fd()), syscall.TIOCSPTLCK, unlock); err != nil {
		ptmx.Close()
		return nil, nil, err
	}
	slave, err = os.OpenFile(filepath.Join("/dev/pts", itoa(n)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		ptmx.Close()
		return nil, nil, err
	}
	return ptmx, slave, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Start forks the shell connected to the pty slave.
// cmdline[0] is both the program and argv[0]. Returns the child command.
func Start(slave *os.File, cmdline []string, env []string) (*exec.Cmd, error) {
	return start(slave, cmdline, env)
}

// StartArgv0 forks prog with a custom argv[0] (mirrors st's execsh which
// invokes the shell via execvp so argv[0] is the resolved path).
func StartArgv0(slave *os.File, argv0 string, prog string, args, env []string) (*exec.Cmd, error) {
	cmdline := append([]string{argv0}, args...)
	cmdline = append([]string{prog}, args...)
	cmd := exec.Command(prog, args...)
	cmd.Args = cmdline
	return applyTTY(cmd, slave, env)
}

func start(slave *os.File, cmdline []string, env []string) (*exec.Cmd, error) {
	if len(cmdline) == 0 {
		return nil, os.ErrInvalid
	}
	cmd := exec.Command(cmdline[0], cmdline[1:]...)
	return applyTTY(cmd, slave, env)
}

func applyTTY(cmd *exec.Cmd, slave *os.File, env []string) (*exec.Cmd, error) {
	cmd.Env = env
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0, // slave is fd 0 (Stdin) in the child
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	slave.Close()
	return cmd, nil
}

// SetWinSize sets the terminal window size on the master.
func SetWinSize(master *os.File, rows, cols int) error {
	ws := &unix.Winsize{
		Row: uint16(rows),
		Col: uint16(cols),
	}
	return unix.IoctlSetWinsize(int(master.Fd()), syscall.TIOCSWINSZ, ws)
}
