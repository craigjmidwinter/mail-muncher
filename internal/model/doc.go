// Package model defines the canonical, provider-independent message model
// that flows through the pipeline: headers, addresses, bodies, and
// attachments normalised from whatever the provider returned.
//
// It also settles thread identity. A hiring process is a thread, not a
// message, so every Message carries a non-empty ThreadID: the provider's own
// conversation id where there is one, and otherwise one synthesized from
// In-Reply-To / References. Reassembling reference chains across a directory
// of .eml files is exactly the MIME drudgery this package exists to absorb;
// ThreadIDSource records which of the two a given id was, because only the
// first comes with a guarantee.
package model
