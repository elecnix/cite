package scope

import (
	"bytes"
	"path"
	"strings"
)

// Named skip reasons. Every skip carries a machine reason that appears in the
// run record; a skip is never a pass (§7, §11).
const (
	SkipReasonBinary    = "binary"
	SkipReasonVendored  = "vendored"
	SkipReasonLockfile  = "lockfile"
	SkipReasonGenerated = "generated"
	SkipReasonMinified  = "minified"
	SkipReasonIgnored   = "paths_ignore"

	// SkipReasonRiskCutoff marks files below the risk-ranking cutoff when
	// more than 40 files are flagged (§7). It is recorded, never silent —
	// and it is deliberately NOT an approved skip: those files were not
	// reviewed, so coverage arithmetic must not count them as covered.
	SkipReasonRiskCutoff = "risk_rank_cutoff"

	// SkipReasonOversized is reserved for callers that cap per-file payload
	// size before reading content.
	SkipReasonOversized = "oversized"
)

// IsBinary reports whether data looks binary: it contains a NUL byte in the
// first 8000 bytes (the same sniff length git uses). A file with a null byte
// is skipped(reason="binary") — that skip must never collapse into "clean"
// (§11); a backdoor rides in on a null byte exactly once.
func IsBinary(data []byte) bool {
	const sniffLen = 8000
	if len(data) > sniffLen {
		data = data[:sniffLen]
	}
	return bytes.IndexByte(data, 0) >= 0
}

var lockfileNames = map[string]bool{
	"package-lock.json":   true,
	"npm-shrinkwrap.json": true,
	"yarn.lock":           true,
	"pnpm-lock.yaml":      true,
	"bun.lock":            true,
	"bun.lockb":           true,
	"go.sum":              true,
	"go.work.sum":         true,
	"cargo.lock":          true,
	"poetry.lock":         true,
	"pipfile.lock":        true,
	"gemfile.lock":        true,
	"composer.lock":       true,
	"mix.lock":            true,
	"podfile.lock":        true,
	"packages.lock.json":  true,
	"paket.lock":          true,
	"deno.lock":           true,
	"flake.lock":          true,
	"uv.lock":             true,
}

var vendoredSegments = map[string]bool{
	"vendor":       true,
	"third_party":  true,
	"node_modules": true,
}

var generatedPatterns = []string{
	"*.pb.go",
	"*.pb.gw.go",
	"*_gen.go",
	"*.gen.go",
	"*_pb2.py",
	"*_pb2_grpc.py",
	"*_pb.js",
	"*_pb.dart",
	"*.snap",
	"*.golden",
	"*.generated.ts",
	"*.g.cs",
	"*.designer.cs",
	"*.autogen.go",
}

var minifiedPatterns = []string{
	"*.min.js",
	"*.min.css",
	"*.min.mjs",
	"*.bundle.js",
	"*.map", // source maps ship with minified output
}

// DefaultSkipReason classifies a path against the default skip list
// (§7): vendored trees, lockfiles, generated files, minified output. It
// returns "" when nothing applies. Content-dependent classification
// (binaries) needs data and lives in SkipReason.
func DefaultSkipReason(pathname string) string {
	p := strings.TrimPrefix(path.Clean("/"+pathname), "/")
	segs := strings.Split(p, "/")
	for _, s := range segs[:len(segs)-1] {
		if vendoredSegments[s] {
			return SkipReasonVendored
		}
	}
	base := strings.ToLower(segs[len(segs)-1])
	if lockfileNames[base] {
		return SkipReasonLockfile
	}
	for _, pat := range generatedPatterns {
		if ok, _ := path.Match(pat, base); ok {
			return SkipReasonGenerated
		}
	}
	for _, pat := range minifiedPatterns {
		if ok, _ := path.Match(pat, base); ok {
			return SkipReasonMinified
		}
	}
	return ""
}

// SkipReason returns the named reason to skip pathname, and whether it should
// be skipped at all. data may be nil when only path classification is wanted;
// when present it enables binary detection. extraIgnores are paths_ignore
// glob patterns (see Match); they add to the default list, never subtract.
func SkipReason(pathname string, data []byte, extraIgnores []string) (string, bool) {
	if data != nil && IsBinary(data) {
		return SkipReasonBinary, true
	}
	if r := DefaultSkipReason(pathname); r != "" {
		return r, true
	}
	for _, pat := range extraIgnores {
		if Match(pat, pathname) {
			return SkipReasonIgnored, true
		}
	}
	return "", false
}

// approvedSkipReasons is the closed set of skip reasons that count toward
// coverage as approved skips. An unexpected skip reason fails the gate (§11).
var approvedSkipReasons = map[string]bool{
	SkipReasonBinary:    true,
	SkipReasonVendored:  true,
	SkipReasonLockfile:  true,
	SkipReasonGenerated: true,
	SkipReasonMinified:  true,
	SkipReasonIgnored:   true,
	SkipReasonOversized: true,
}

// IsApprovedSkipReason reports whether a skip reason counts toward the
// approved-skip half of the coverage arithmetic. risk_rank_cutoff is
// intentionally absent: cut files were not reviewed and are not covered.
func IsApprovedSkipReason(reason string) bool {
	return approvedSkipReasons[reason]
}
