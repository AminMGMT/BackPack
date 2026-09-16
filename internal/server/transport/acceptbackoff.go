package transport

import "github.com/backpack/backpack/internal/utils/acceptloop"

// acceptBackoff is the shared accept-loop backoff, under the name the
// transports already call it. The reasoning, and why it exists at all, is in
// the acceptloop package doc; it was moved there when the SOCKS proxy turned
// out to need the same thing and could not import this package.
type acceptBackoff = acceptloop.Backoff
