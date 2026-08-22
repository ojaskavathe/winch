package rigs

// Linux twin of pty_darwin_test.go: raw /dev/ptmx, stdlib only. No grantpt
// ioctl here — devpts owns permissions — so it's unlock (TIOCSPTLCK) plus
// slave lookup (TIOCGPTN -> /dev/pts/N).

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	tiocsptlck = 0x40045431 // unlockpt, *int32 (0 = unlock)
	tiocgptn   = 0x80045430 // ptsname, *uint32 out (pts number)
	tiocswinsz = 0x5414
)

type winsize struct{ row, col, x, y uint16 }

func ioctl(fd uintptr, req uintptr, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); e != 0 {
		return e
	}
	return nil
}

// openPty returns a master/slave pair with the slave sized rows x cols.
func openPty(rows, cols int) (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	fail := func(e error) (*os.File, *os.File, error) { master.Close(); return nil, nil, e }
	var unlock int32
	if err := ioctl(master.Fd(), tiocsptlck, uintptr(unsafe.Pointer(&unlock))); err != nil {
		return fail(err)
	}
	var n uint32
	if err := ioctl(master.Fd(), tiocgptn, uintptr(unsafe.Pointer(&n))); err != nil {
		return fail(err)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return fail(err)
	}
	ws := winsize{row: uint16(rows), col: uint16(cols)}
	if err := ioctl(slave.Fd(), tiocswinsz, uintptr(unsafe.Pointer(&ws))); err != nil {
		slave.Close()
		return fail(err)
	}
	return master, slave, nil
}
