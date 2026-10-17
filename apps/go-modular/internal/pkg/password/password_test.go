package password

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashAndValidateRoundTrip(t *testing.T) {
	h := NewHasher()

	encoded, err := h.Hash("correct horse battery staple")
	require.NoError(t, err)

	ok, err := h.Validate("correct horse battery staple", encoded)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestValidateRejectsWrongPassword(t *testing.T) {
	h := NewHasher()

	encoded, err := h.Hash("correct horse battery staple")
	require.NoError(t, err)

	ok, err := h.Validate("Correct horse battery staple", encoded)
	require.NoError(t, err, "a wrong password is not an error, just a mismatch")
	assert.False(t, ok)
}

func TestHashUsesAFreshSaltEachTime(t *testing.T) {
	h := NewHasher()

	first, err := h.Hash("same-password")
	require.NoError(t, err)
	second, err := h.Hash("same-password")
	require.NoError(t, err)

	assert.NotEqual(t, first, second, "identical passwords must not produce identical hashes")

	// Both must still verify.
	for _, encoded := range []string{first, second} {
		ok, err := h.Validate("same-password", encoded)
		require.NoError(t, err)
		assert.True(t, ok)
	}
}

func TestHashEncodesOrgStandardParameters(t *testing.T) {
	h := NewHasher()

	encoded, err := h.Hash("whatever")
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(encoded, "$argon2id$v=19$m=16384,t=4,p=2$"),
		"expected the organisation's standard parameters, got %q", encoded)

	params, salt, key, err := decode(encoded)
	require.NoError(t, err)
	assert.Equal(t, DefaultParams().Memory, params.Memory)
	assert.Equal(t, DefaultParams().Iterations, params.Iterations)
	assert.Equal(t, DefaultParams().Parallelism, params.Parallelism)
	assert.Len(t, salt, int(DefaultParams().SaltLength))
	assert.Len(t, key, int(DefaultParams().KeyLength))
}

func TestValidateHandlesUnicodeAndBoundaryLengths(t *testing.T) {
	h := NewHasher()

	for _, pw := range []string{
		"p",                        // shortest accepted here
		"rahasia yang sangat aman", // non-ASCII-adjacent spacing
		"kata-sandi-ünïcödé-🔐",     // multi-byte runes
		strings.Repeat("a", MaxPasswordLength),
	} {
		encoded, err := h.Hash(pw)
		require.NoError(t, err)

		ok, err := h.Validate(pw, encoded)
		require.NoError(t, err)
		assert.True(t, ok, "password of length %d should verify", len(pw))
	}
}

func TestHashRejectsOverlongPassword(t *testing.T) {
	h := NewHasher()

	_, err := h.Hash(strings.Repeat("a", MaxPasswordLength+1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestValidateRejectsMalformedHashes(t *testing.T) {
	h := NewHasher()

	for name, encoded := range map[string]string{
		"empty":               "",
		"not phc":             "plaintext",
		"too few fields":      "$argon2id$v=19$m=16384,t=4,p=2$c2FsdA",
		"wrong algorithm":     "$argon2i$v=19$m=16384,t=4,p=2$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"unsupported version": "$argon2id$v=16$m=16384,t=4,p=2$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"garbage params":      "$argon2id$v=19$m=x,t=y,p=z$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"bad salt base64":     "$argon2id$v=19$m=16384,t=4,p=2$!!!!$aGFzaGhhc2hoYXNoaGFzaA",
	} {
		t.Run(name, func(t *testing.T) {
			ok, err := h.Validate("password", encoded)
			require.Error(t, err)
			assert.False(t, ok)
		})
	}
}

// A stored hash is attacker-reachable in a credential-stuffing scenario only if
// the database is already compromised, but an out-of-range parameter would turn
// a read into a multi-gigabyte allocation. Validate must refuse it up front.
func TestValidateRejectsOutOfRangeParameters(t *testing.T) {
	h := NewHasher()

	for name, encoded := range map[string]string{
		"huge memory":  "$argon2id$v=19$m=4294967295,t=4,p=2$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"huge threads": "$argon2id$v=19$m=16384,t=4,p=255$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"huge time":    "$argon2id$v=19$m=16384,t=99,p=2$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"tiny memory":  "$argon2id$v=19$m=1,t=4,p=2$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"zero time":    "$argon2id$v=19$m=16384,t=0,p=2$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
	} {
		t.Run(name, func(t *testing.T) {
			ok, err := h.Validate("password", encoded)
			require.Error(t, err)
			assert.False(t, ok)
		})
	}
}

func TestCustomParamsRoundTrip(t *testing.T) {
	// Deliberately cheaper than the default so the test stays fast.
	h := NewHasherWithParams(Params{
		Memory:      8,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   16,
	})

	encoded, err := h.Hash("custom")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(encoded, "$argon2id$v=19$m=8,t=1,p=1$"))

	ok, err := h.Validate("custom", encoded)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestNewHasherWithParamsRejectsUnsafeParameters(t *testing.T) {
	assert.Panics(t, func() {
		NewHasherWithParams(Params{Memory: 0, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 32})
	})
}
