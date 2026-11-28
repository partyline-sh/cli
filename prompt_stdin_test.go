package main

import (
	"bufio"
	"os"
	"sync"
)

var (
	stdinOnce sync.Once
	stdinBuf  *bufio.Reader
)

// stdin is a buffered reader on os.Stdin for these tests: they pipe lines in and check that no
// prompt consumed them.
func stdin() *bufio.Reader {
	stdinOnce.Do(func() { stdinBuf = bufio.NewReader(os.Stdin) })
	return stdinBuf
}
