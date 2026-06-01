// Package netresolve provides DNS resolution strategies for the psxdh
// upstream forward path. It is a thin wrapper around three transports
// (system, plain UDP, DNS-over-HTTPS) plus a multi-resolver fallback and
// an in-memory TTL cache.
//
// The motivation is documented in
// docs/decisions/0003-network-resilience.md and the user-facing setup in
// docs/network-resilience.md. The package depends on
// golang.org/x/net/dns/dnsmessage for wire-format encoding/decoding.
//
// The exported API is the Resolver interface; everything else in this
// package is an implementation of it. The proxy never reaches into a
// concrete resolver — it uses whatever NewFromConfig hands back.
package netresolve
