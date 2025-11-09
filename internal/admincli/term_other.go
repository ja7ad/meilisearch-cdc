//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package admincli

import "errors"

// Without terminal support passwords are read from stdin like from a pipe.
func isTerminal(int) bool { return false }

func readNoEcho(int) ([]byte, error) { return nil, errors.New("no terminal support") }
