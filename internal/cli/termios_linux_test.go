package cli

import "golang.org/x/sys/unix"

// readTermios is the request that reads a terminal's settings.
const readTermios = unix.TCGETS
