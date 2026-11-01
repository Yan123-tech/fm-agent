// Package term provides minimal raw-mode terminal control using only the
// standard library (ioctl/termios on Darwin).
package term

import (
	"os"
	"syscall"
	"unsafe"
)

// winsize mirrors struct winsize for TIOCGWINSZ.
type winsize struct {
	Row, Col, Xpixel, Ypixel uint16
}

// Size returns the terminal dimensions of f, or 80x24 when unknown.
func Size(f *os.File) (width, height int) {
	var ws winsize
	_, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL, f.Fd(),
		uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)),
		0, 0, 0)
	if errno != 0 || ws.Col == 0 || ws.Row == 0 {
		return 80, 24
	}
	return int(ws.Col), int(ws.Row)
}

// MakeRaw switches f to raw mode and returns a restore function. In raw
// mode input is available byte-by-byte without echo, line editing, or
// signal generation; output post-processing is disabled, so callers must
// emit CRLF explicitly.
func MakeRaw(f *os.File) (restore func(), err error) {
	var old syscall.Termios
	if errno := tcgetattr(f.Fd(), &old); errno != nil {
		return nil, errno
	}
	raw := old
	raw.Iflag &^= syscall.IXON | syscall.ICRNL | syscall.BRKINT | syscall.INPCK | syscall.ISTRIP
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Oflag &^= syscall.OPOST
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if errno := tcsetattr(f.Fd(), &raw); errno != nil {
		return nil, errno
	}
	return func() { _ = tcsetattr(f.Fd(), &old) }, nil
}

func tcgetattr(fd uintptr, t *syscall.Termios) error {
	_, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL, fd,
		uintptr(syscall.TIOCGETA), uintptr(unsafe.Pointer(t)),
		0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func tcsetattr(fd uintptr, t *syscall.Termios) error {
	_, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL, fd,
		uintptr(syscall.TIOCSETA), uintptr(unsafe.Pointer(t)),
		0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
