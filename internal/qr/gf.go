package qr

// Reed-Solomon error correction over GF(256), the field QR codes are defined
// in: byte-wide symbols, primitive polynomial x^8 + x^4 + x^3 + x^2 + 1 (0x11D)
// and generator element α = 2.
//
// Multiplication is done through exponential/logarithm tables. Because α is a
// generator, every non-zero element is α^k for exactly one k in [0,255), so
// a·b = α^(log a + log b mod 255) turns a carry-less multiply-and-reduce into
// two lookups and an add.

const (
	// gfPrim is the field's primitive polynomial with its x^8 term, applied as a
	// reduction whenever a doubling overflows eight bits.
	gfPrim = 0x11D
	// gfOrder is the number of non-zero elements — the period of α, and so the
	// modulus for exponents.
	gfOrder = 255
)

// gfExp[k] = α^k for k in [0, 510), doubled in length so a sum of two logarithms
// (each < 255) indexes it without a modulo. gfLog[v] = k such that α^k = v;
// gfLog[0] is unused (zero has no logarithm) and never read, because both
// multiply paths test for zero first.
var gfExp [512]byte
var gfLog [256]byte

func init() {
	x := 1
	for i := range gfOrder {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= gfPrim
		}
	}
	// The tail repeats the cycle so gfExp[i+j] is valid for any i, j < 255.
	for i := gfOrder; i < len(gfExp); i++ {
		gfExp[i] = gfExp[i-gfOrder]
	}
}

// gfMul multiplies two field elements. Zero is special-cased because it has no
// logarithm — the table trick only covers the multiplicative group.
func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

// rsGenerator returns the generator polynomial of degree n — the product of
// (x - α^i) for i in [0, n) — with coefficients in descending powers of x and
// the leading 1 omitted, which is the form rsRemainder's synthetic division
// wants.
//
// Building it by repeated multiplication rather than hardcoding the spec's
// tables is deliberate: the tables are 40 rows of magic numbers whose only
// verification is transcription care, while this is the definition itself.
func rsGenerator(n int) []byte {
	gen := make([]byte, n)
	gen[n-1] = 1 // start at the polynomial 1, stored right-aligned
	root := byte(1)
	for range n {
		// Multiply the accumulated polynomial by (x - α^i). In GF(2^k)
		// subtraction is XOR, so (x - r) and (x + r) are the same factor.
		for j := range n {
			gen[j] = gfMul(gen[j], root)
			if j+1 < n {
				gen[j] ^= gen[j+1]
			}
		}
		root = gfMul(root, 2)
	}
	return gen
}

// rsRemainder returns the n error-correction codewords for data: the remainder
// of data·x^n divided by the degree-n generator polynomial.
//
// The loop is synthetic division carrying only the remainder register, which is
// why it costs O(len(data)·n) byte operations and no allocation beyond the
// result.
func rsRemainder(data []byte, n int) []byte {
	gen := rsGenerator(n)
	rem := make([]byte, n)
	for _, b := range data {
		factor := b ^ rem[0]
		copy(rem, rem[1:])
		rem[n-1] = 0
		for i := range n {
			rem[i] ^= gfMul(gen[i], factor)
		}
	}
	return rem
}
