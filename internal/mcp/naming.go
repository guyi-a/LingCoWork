package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Remote tools are renamed before the model sees them.
//
// Two reasons. A server publishing read_file would otherwise shadow our
// builtin of the same name, and the model would have no way to tell which one
// it was calling. And the providers cap a tool name at 64 characters of
// [a-zA-Z0-9_-], which a server name plus a long tool name can exceed.
//
// The scheme is {serverSlug}__{remoteTool}. The double underscore is a
// separator that survives sanitisation and reads as a boundary.

const (
	maxToolNameLen = 64
	nameSeparator  = "__"
	hashLen        = 6
	// fallbackSlug is used when a server name sanitises down to nothing,
	// e.g. one written entirely in Chinese.
	fallbackSlug = "mcp"
)

// allocator hands out collision-free public names.
//
// It must be seeded with the builtin tool names so a remote tool can never
// take one over; the loser of a collision gets a numeric suffix rather than
// being dropped, since a renamed tool is still usable and a missing one is
// just confusing.
//
// It lives as long as the Manager, not as long as one Connect: servers come
// and go at runtime, and a server that reconnects has to get the same names
// back. Hence release, which is what makes reallocation reproduce the
// previous answer instead of appending _2 to everything.
type allocator struct {
	reserved  map[string]struct{}
	usedNames map[string]struct{}
	usedSlugs map[string]struct{}
}

func newAllocator(reserved []string) *allocator {
	a := &allocator{
		reserved:  make(map[string]struct{}, len(reserved)),
		usedNames: make(map[string]struct{}, len(reserved)),
		usedSlugs: make(map[string]struct{}),
	}
	for _, n := range reserved {
		a.reserved[n] = struct{}{}
		a.usedNames[n] = struct{}{}
	}
	return a
}

// release gives a disconnected server's slug and tool names back.
//
// Builtin names are never released: they are reserved, not allocated, and a
// remote tool that once collided with read_file must keep colliding with it.
func (a *allocator) release(slug string, names []string) {
	if slug != "" {
		delete(a.usedSlugs, slug)
	}
	for _, n := range names {
		if _, isBuiltin := a.reserved[n]; isBuiltin {
			continue
		}
		delete(a.usedNames, n)
	}
}

// slugFor returns a stable, unique prefix for a server.
//
// The server name is free text — it can be Chinese, contain spaces, or differ
// from another server's only in punctuation — so it is sanitised first and
// then deduplicated.
func (a *allocator) slugFor(serverName string) string {
	base := sanitize(serverName)
	if base == "" {
		base = fallbackSlug
	}
	slug := base
	for i := 2; ; i++ {
		if _, taken := a.usedSlugs[slug]; !taken {
			a.usedSlugs[slug] = struct{}{}
			return slug
		}
		slug = fmt.Sprintf("%s_%d", base, i)
	}
}

// nameFor returns the public name for one remote tool under a server slug.
func (a *allocator) nameFor(slug, remoteTool string) string {
	remote := sanitize(remoteTool)
	if remote == "" {
		remote = "tool"
	}
	base := fit(slug + nameSeparator + remote)
	name := base
	for i := 2; ; i++ {
		if _, taken := a.usedNames[name]; !taken {
			a.usedNames[name] = struct{}{}
			return name
		}
		suffix := fmt.Sprintf("_%d", i)
		name = fit(base+suffix, len(suffix))
	}
}

// fit shortens a name to the provider limit, reserving `reserve` characters
// for a caller-appended suffix.
//
// A plain truncation could map two different tools onto the same name, so the
// tail is replaced by a hash of the full name. That keeps it unique and, more
// importantly, stable: the same tool gets the same name every restart, which
// matters because these names appear in stored transcripts.
func fit(name string, reserve ...int) string {
	limit := maxToolNameLen
	for _, r := range reserve {
		limit -= r
	}
	if len(name) <= limit {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	digest := hex.EncodeToString(sum[:])[:hashLen]
	keep := limit - hashLen - 1
	if keep < 1 {
		return digest
	}
	return name[:keep] + "_" + digest
}

// sanitize reduces free text to the character set providers accept in a tool
// name, collapsing runs of rejected characters into a single underscore.
func sanitize(s string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
			lastUnderscore = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
			lastUnderscore = false
		case r == '_':
			// Collapsed with any neighbouring rejected characters, so
			// "a _ b" yields a_b rather than a___b.
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		default:
			if !lastUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}
