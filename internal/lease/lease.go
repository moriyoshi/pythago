// Package lease lets a value lend Go memory to package py's conversion: py.From (and so a
// call's arguments, a posted value converted on a loop thread) turns a Bytes into a
// Python bytes object, a copy, and then calls ReleaseLease, exactly once. The lender
// keeps the memory valid until then: a server's request memory, held by a reference
// that ReleaseLease drops.
package lease

// Bytes is memory lent until its conversion.
type Bytes interface {
	// LeasedBytes returns the memory; it stays valid until ReleaseLease.
	LeasedBytes() []byte
	// ReleaseLease ends the lease. py calls it once the copy is made (or failed).
	ReleaseLease()
}
