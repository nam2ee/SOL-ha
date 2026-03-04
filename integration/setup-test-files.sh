#!/bin/bash

set -e

echo "Setting up test identity files..."

mkdir -p ./test-files

# Generate Solana-format keypair files using Python (ships with macOS, no deps needed).
# The pubkeys (bytes[32:64]) MUST match the hardcoded values in mock-solana/main.go
# so that getIdentity verification passes during integration tests.
python3 -c '
import json, os, secrets

# Base58 alphabet (Bitcoin/Solana variant)
B58_ALPHABET = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

def b58decode(s):
    """Decode a base58-encoded string to bytes."""
    n = 0
    for c in s:
        n = n * 58 + B58_ALPHABET.index(c)
    # Convert to bytes
    result = []
    while n > 0:
        result.append(n & 0xFF)
        n >>= 8
    result.reverse()
    # Handle leading 1s (zero bytes)
    pad = len(s) - len(s.lstrip("1"))
    return bytes(pad) + bytes(result)

def generate_keypair(filename, pubkey_b58):
    """Generate a Solana keypair file: [seed(32) + pubkey(32)] as JSON array of ints.
    The seed is random (not cryptographically valid for the pubkey) but that is fine
    because the HA manager only reads bytes[32:64] as the public key."""
    pubkey_bytes = b58decode(pubkey_b58)
    assert len(pubkey_bytes) == 32, f"pubkey must be 32 bytes, got {len(pubkey_bytes)}"

    seed = secrets.token_bytes(32)
    keypair = list(seed + pubkey_bytes)

    with open(filename, "w") as f:
        json.dump(keypair, f)

    print(f"  {filename}: pubkey={pubkey_b58}")

# These pubkeys MUST match the constants in integration/mock-solana/main.go
ACTIVE_PUBKEY     = "ArkzFExXXHaA6izkNhTJJ5zpXdQpynffjfRMJu4Yq6H"
PASSIVE_1_PUBKEY  = "AP4JyZq2vuN4u64FGFHTwdG11xHu1vZWVYQj21MPLrnw"
PASSIVE_2_PUBKEY  = "DJ7w4p8Ve7qdSAmkpA3sviSbsd1HPUxd43x7MTH72JHT"
PASSIVE_3_PUBKEY  = "5dXttfrjFEEExmZhVmVAdw2LzepNAhFYJTUgPCDk8CYD"

os.chdir("./test-files")
generate_keypair("active-identity.json",     ACTIVE_PUBKEY)
generate_keypair("passive-identity-1.json",  PASSIVE_1_PUBKEY)
generate_keypair("passive-identity-2.json",  PASSIVE_2_PUBKEY)
generate_keypair("passive-identity-3.json",  PASSIVE_3_PUBKEY)
'

echo ""
echo "Test identity files created (pubkeys match mock-solana constants)"
echo "Test files are ready for integration testing"
