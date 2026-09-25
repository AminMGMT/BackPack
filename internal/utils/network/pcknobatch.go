package network

import "errors"

// errPckNoBatch says the pck carrier cannot read in batches after all; the
// caller reads one frame at a time instead. See pckbatch_linux.go.
var errPckNoBatch = errors.New("pck: this carrier cannot read in batches")

// IsNoBatch reports whether err is a carrier declining to batch.
func IsNoBatch(err error) bool { return errors.Is(err, errPckNoBatch) }
