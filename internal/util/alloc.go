// Allocation capacity hints (CodeQL go/allocation-size-overflow).
//
// Every make() in gofer that pre-sizes a slice or map from element counts goes
// through CapSum/CapMul instead of spelling the arithmetic out inline. The reason
// is correctness, not style: `make([]T, 0, len(a)+len(b))` (or `len(m)*2`)
// computes the capacity hint with plain int arithmetic, and that computation is
// not guarded by anything — a wrapped result turns a purely cosmetic capacity
// hint into a panic ("makeslice: cap out of range") or a wild allocation. CodeQL
// reports exactly this shape as go/allocation-size-overflow.
//
// A capacity hint is only a hint: append/insert still grow the collection on
// demand. Clamping the result to MaxHint therefore costs at most a few extra
// copies and can never change observable behaviour, which makes it a strictly
// better trade than risking the overflow.
//
// Both functions do their arithmetic in int64 and clamp every input to MaxHint
// before using it, so no intermediate can wrap on any platform. The 64-bit
// widening is also the mitigation go/allocation-size-overflow itself recognises
// (its WidenTo64BitSanitizer), which keeps these helpers from re-raising the
// finding they exist to remove.

package util

// MaxHint is the largest capacity hint any caller can receive. It is deliberately
// far below any int limit so that the arithmetic in CapSum/CapMul can never wrap,
// and high enough (1M elements) that no realistic collection in gofer is clamped:
// these hints size env blocks, argv blocks and log lines, not bulk payloads.
const MaxHint = 1 << 20

// CapSum returns a capacity hint for a collection that will hold the given
// element counts added together: CapSum(len(a), len(b)) for "a followed by b".
// Non-positive parts contribute nothing (a nil map/slice has no elements). The
// result is always in [0, MaxHint], so the hint can never overflow and can never
// be rejected by make.
func CapSum(parts ...int) int {
	total := int64(0)
	for _, p := range parts {
		if p <= 0 {
			continue
		}
		if p > MaxHint {
			return MaxHint
		}
		// p <= MaxHint and total <= MaxHint, so the sum is at most 2*MaxHint and
		// the 64-bit addition cannot wrap; the check below restores the invariant.
		total += int64(p)
		if total > MaxHint {
			return MaxHint
		}
	}
	return int(total)
}

// CapMul returns a capacity hint for a collection holding n groups of k elements
// each: CapMul(len(m), 2) for "two entries per map key". Non-positive factors
// yield 0. The result is always in [0, MaxHint].
func CapMul(n, k int) int {
	if n <= 0 || k <= 0 {
		return 0
	}
	// Bound both factors first: with n,k <= MaxHint (2^20) the product is at most
	// 2^40, which cannot wrap the 64-bit multiplication below.
	if n > MaxHint || k > MaxHint {
		return MaxHint
	}
	product := int64(n) * int64(k)
	if product > MaxHint {
		return MaxHint
	}
	return int(product)
}
