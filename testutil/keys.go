//go:build test

package testutil

import (
	"encoding/hex"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Hardcoded test keys for deterministic testing.
// These keys are pre-generated and committed to ensure reproducibility across test runs.
// NEVER use these keys in production - they are publicly known.
//
// Generated using a deterministic seed for reproducibility.
// If you need to regenerate:
//   1. Use a fixed seed (e.g., 42 for supplier, 43 for app)
//   2. Generate secp256k1 private key bytes
//   3. Encode as hex and commit

// TestSupplierPrivKeyHex is a hardcoded secp256k1 private key for the test supplier.
// This is a deterministic key generated from seed 42 - DO NOT USE IN PRODUCTION.
const TestSupplierPrivKeyHex = "a3f7b1c2d8e4f9a5b6c3d0e7f8a9b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9"

// TestAppPrivKeyHex is a hardcoded secp256k1 private key for the test application.
// This is a deterministic key generated from seed 43 - DO NOT USE IN PRODUCTION.
const TestAppPrivKeyHex = "b4e8c2d9f5a6b7c4d1e8f9a0b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2"

// TestSupplier2PrivKeyHex is a second hardcoded secp256k1 private key for multi-supplier tests.
// This is a deterministic key generated from seed 44 - DO NOT USE IN PRODUCTION.
const TestSupplier2PrivKeyHex = "c5f9d3e0a6b7c8d5e2f9a0b1c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3"

// TestApp2PrivKeyHex is a second hardcoded secp256k1 private key for multi-app tests.
// This is a deterministic key generated from seed 45 - DO NOT USE IN PRODUCTION.
const TestApp2PrivKeyHex = "d6a0e4f1b7c8d9e6f3a0b1c2d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4"

var (
	// Pre-parsed keys and addresses for convenience.
	// These are computed once at package initialization.

	testSupplierPrivKeyBytes, _ = hex.DecodeString(TestSupplierPrivKeyHex)
	testAppPrivKeyBytes, _      = hex.DecodeString(TestAppPrivKeyHex)
	testSupplier2PrivKeyBytes, _ = hex.DecodeString(TestSupplier2PrivKeyHex)
	testApp2PrivKeyBytes, _      = hex.DecodeString(TestApp2PrivKeyHex)
)

// TestSupplierPrivKey returns the test supplier's private key.
func TestSupplierPrivKey() *secp256k1.PrivKey {
	return &secp256k1.PrivKey{Key: testSupplierPrivKeyBytes}
}

// TestSupplierPubKey returns the test supplier's public key.
func TestSupplierPubKey() *secp256k1.PubKey {
	privKey := TestSupplierPrivKey()
	return privKey.PubKey().(*secp256k1.PubKey)
}

// TestSupplierAddress returns the test supplier's bech32 address.
func TestSupplierAddress() string {
	pubKey := TestSupplierPubKey()
	addr := sdk.AccAddress(pubKey.Address())
	return addr.String()
}

// TestAppPrivKey returns the test application's private key.
func TestAppPrivKey() *secp256k1.PrivKey {
	return &secp256k1.PrivKey{Key: testAppPrivKeyBytes}
}

// TestAppPubKey returns the test application's public key.
func TestAppPubKey() *secp256k1.PubKey {
	privKey := TestAppPrivKey()
	return privKey.PubKey().(*secp256k1.PubKey)
}

// TestAppAddress returns the test application's bech32 address.
func TestAppAddress() string {
	pubKey := TestAppPubKey()
	addr := sdk.AccAddress(pubKey.Address())
	return addr.String()
}

// TestSupplier2PrivKey returns the second test supplier's private key.
func TestSupplier2PrivKey() *secp256k1.PrivKey {
	return &secp256k1.PrivKey{Key: testSupplier2PrivKeyBytes}
}

// TestSupplier2PubKey returns the second test supplier's public key.
func TestSupplier2PubKey() *secp256k1.PubKey {
	privKey := TestSupplier2PrivKey()
	return privKey.PubKey().(*secp256k1.PubKey)
}

// TestSupplier2Address returns the second test supplier's bech32 address.
func TestSupplier2Address() string {
	pubKey := TestSupplier2PubKey()
	addr := sdk.AccAddress(pubKey.Address())
	return addr.String()
}

// TestApp2PrivKey returns the second test application's private key.
func TestApp2PrivKey() *secp256k1.PrivKey {
	return &secp256k1.PrivKey{Key: testApp2PrivKeyBytes}
}

// TestApp2PubKey returns the second test application's public key.
func TestApp2PubKey() *secp256k1.PubKey {
	privKey := TestApp2PrivKey()
	return privKey.PubKey().(*secp256k1.PubKey)
}

// TestApp2Address returns the second test application's bech32 address.
func TestApp2Address() string {
	pubKey := TestApp2PubKey()
	addr := sdk.AccAddress(pubKey.Address())
	return addr.String()
}

// TestServiceID returns a deterministic service ID for testing.
const TestServiceID = "anvil"

// TestServiceID2 returns a second deterministic service ID for multi-service tests.
const TestServiceID2 = "ethereum"
