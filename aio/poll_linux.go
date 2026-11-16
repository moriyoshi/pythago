package aio

import "golang.org/x/sys/unix"

// pollReadyExtra are the revents besides the requested ones that count as ready:
// POLLERR, POLLHUP and POLLRDHUP.
const pollReadyExtra = unix.POLLERR | unix.POLLHUP | unix.POLLRDHUP
