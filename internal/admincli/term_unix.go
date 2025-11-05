//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package admincli

import "golang.org/x/sys/unix"

func isTerminal(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	return err == nil
}

// readNoEcho reads a line from the terminal fd without echoing it.
func readNoEcho(fd int) ([]byte, error) {
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	quiet.Lflag |= unix.ICANON | unix.ISIG
	quiet.Iflag |= unix.ICRNL
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &quiet); err != nil {
		return nil, err
	}
	defer unix.IoctlSetTermios(fd, ioctlSetTermios, old)
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := unix.Read(fd, buf)
		if n == 1 {
			if buf[0] == '\n' {
				return line, nil
			}
			line = append(line, buf[0])
			continue
		}
		if err != nil && err != unix.EINTR {
			return line, err
		}
		if n == 0 {
			return line, nil
		}
	}
}
