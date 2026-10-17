// Package password hashes and verifies user passwords with argon2id.
//
// Hashes are stored in the standard PHC string format:
//
//	$argon2id$v=19$m=16384,t=4,p=2$<salt>$<hash>
//
// The parameters match the ones used by the organisation's Go services so that
// hashes remain portable between them.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	algorithm = "argon2id"
	// version is the argon2 version encoded in the PHC string.
	version = 19
)

// MaxPasswordLength bounds the input so a pathological password cannot be used
// to burn CPU and memory. Argon2 has no truncation limit of its own.
const MaxPasswordLength = 1024

// Bounds applied when reading parameters back out of a stored hash. Without
// these, a crafted hash could make Validate allocate an arbitrary amount of
// memory.
const (
	minMemory      = 8       // KiB
	maxMemory      = 1 << 18 // 256 MiB in KiB
	minIterations  = 1
	maxIterations  = 16
	minParallelism = 1
	maxParallelism = 16
	minSaltLength  = 8
	maxSaltLength  = 64
	minKeyLength   = 16
	maxKeyLength   = 64
)

// ErrInvalidHash is returned when a stored hash cannot be parsed or its
// parameters are out of range.
var ErrInvalidHash = errors.New("password: invalid hash format")

// Params controls the cost of hashing.
type Params struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32 // bytes
	KeyLength   uint32 // bytes
}

// DefaultParams returns the organisation's standard argon2id parameters
// (16 MiB, 4 iterations, 2 threads).
func DefaultParams() Params {
	return Params{
		Memory:      16384,
		Iterations:  4,
		Parallelism: 2,
		SaltLength:  8,
		KeyLength:   32,
	}
}

// validateCosts reports whether the cost parameters are in range. These are the
// only parameters encoded in a PHC string, so decoding uses this rather than
// validate.
func (p Params) validateCosts() error {
	switch {
	case p.Memory < minMemory || p.Memory > maxMemory:
		return fmt.Errorf("password: memory %d KiB outside %d..%d", p.Memory, minMemory, maxMemory)
	case p.Iterations < minIterations || p.Iterations > maxIterations:
		return fmt.Errorf("password: iterations %d outside %d..%d", p.Iterations, minIterations, maxIterations)
	case p.Parallelism < minParallelism || p.Parallelism > maxParallelism:
		return fmt.Errorf("password: parallelism %d outside %d..%d", p.Parallelism, minParallelism, maxParallelism)
	}
	return nil
}

// validate reports whether the parameters are usable for hashing.
func (p Params) validate() error {
	if err := p.validateCosts(); err != nil {
		return err
	}
	switch {
	case p.SaltLength < minSaltLength || p.SaltLength > maxSaltLength:
		return fmt.Errorf("password: salt length %d outside %d..%d", p.SaltLength, minSaltLength, maxSaltLength)
	case p.KeyLength < minKeyLength || p.KeyLength > maxKeyLength:
		return fmt.Errorf("password: key length %d outside %d..%d", p.KeyLength, minKeyLength, maxKeyLength)
	}
	return nil
}

// Hasher hashes and verifies passwords using a fixed set of parameters.
type Hasher struct {
	params Params
}

// NewHasher returns a Hasher using DefaultParams.
func NewHasher() *Hasher {
	return &Hasher{params: DefaultParams()}
}

// NewHasherWithParams returns a Hasher using custom parameters. It panics if
// the parameters are out of range, because they are compile-time configuration
// rather than user input.
func NewHasherWithParams(p Params) *Hasher {
	if err := p.validate(); err != nil {
		panic(err)
	}
	return &Hasher{params: p}
}

// Params returns the parameters this Hasher uses.
func (h *Hasher) Params() Params { return h.params }

// Hash derives a PHC-encoded argon2id hash from password using a fresh random
// salt. The same password hashes differently every time.
func (h *Hasher) Hash(password string) (string, error) {
	if len(password) > MaxPasswordLength {
		return "", fmt.Errorf("password: exceeds %d bytes", MaxPasswordLength)
	}

	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: read salt: %w", err)
	}

	key := argon2.IDKey(
		[]byte(password),
		salt,
		h.params.Iterations,
		h.params.Memory,
		h.params.Parallelism,
		h.params.KeyLength,
	)

	return fmt.Sprintf(
		"$%s$v=%d$m=%d,t=%d,p=%d$%s$%s",
		algorithm,
		version,
		h.params.Memory,
		h.params.Iterations,
		h.params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Validate reports whether password matches the PHC-encoded hash.
//
// It returns (false, nil) for a wrong password and (false, ErrInvalidHash) when
// the stored hash is unparseable, so callers can tell "wrong password" apart
// from "corrupt data" without leaking either to the client.
func (h *Hasher) Validate(password, encoded string) (bool, error) {
	params, salt, want, err := decode(encoded)
	if err != nil {
		return false, err
	}
	if len(password) > MaxPasswordLength {
		// Cannot match a hash produced from a longer password anyway.
		return false, nil
	}

	got := argon2.IDKey(
		[]byte(password),
		salt,
		params.Iterations,
		params.Memory,
		params.Parallelism,
		uint32(len(want)),
	)

	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// decode parses a PHC argon2id string into its parameters, salt and key.
func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// Leading empty element because the string starts with '$'.
	if len(parts) != 6 || parts[0] != "" {
		return Params{}, nil, nil, ErrInvalidHash
	}
	if parts[1] != algorithm {
		return Params{}, nil, nil, fmt.Errorf("%w: unsupported algorithm %q", ErrInvalidHash, parts[1])
	}

	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: version: %v", ErrInvalidHash, err)
	}
	if v != version {
		return Params{}, nil, nil, fmt.Errorf("%w: unsupported version %d", ErrInvalidHash, v)
	}

	var params Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d",
		&params.Memory, &params.Iterations, &params.Parallelism); err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: parameters: %v", ErrInvalidHash, err)
	}
	if err := params.validateCosts(); err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: %v", ErrInvalidHash, err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: salt: %v", ErrInvalidHash, err)
	}
	if len(salt) < minSaltLength || len(salt) > maxSaltLength {
		return Params{}, nil, nil, fmt.Errorf("%w: salt length %d", ErrInvalidHash, len(salt))
	}

	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: hash: %v", ErrInvalidHash, err)
	}
	if len(key) < minKeyLength || len(key) > maxKeyLength {
		return Params{}, nil, nil, fmt.Errorf("%w: key length %d", ErrInvalidHash, len(key))
	}

	// Report the salt and key lengths the string actually used, so callers get
	// a fully populated Params.
	params.SaltLength = uint32(len(salt))
	params.KeyLength = uint32(len(key))

	return params, salt, key, nil
}
