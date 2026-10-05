// SPDX-License-Identifier: AGPL-3.0-or-later

// Package webauthntest is a software passkey authenticator for tests: it
// answers the options Araldo gives a browser with the same JSON a browser
// sends back, signed with real P-256 keys, so tests run the WebAuthn
// ceremonies end to end without a browser or a device.
package webauthntest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// Authenticator holds passkeys for one relying party, as a phone or a
// security key would.
type Authenticator struct {
	RPID   string // the server's host
	Origin string // the page's origin, such as https://araldo.test
	creds  []*credential
}

type credential struct {
	id, userHandle []byte
	key            *ecdsa.PrivateKey
	count          uint32
}

var b64 = base64.RawURLEncoding

const (
	flagUP = 0x01 // user present
	flagUV = 0x04 // user verified
	flagAT = 0x40 // attested credential data included
)

// Create makes a passkey from navigator.credentials.create options (as
// Araldo sends them: {"publicKey": {...}}) and returns the browser's
// response JSON.
func (a *Authenticator) Create(options []byte) ([]byte, error) {
	var opts struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RP        struct {
				ID string `json:"id"`
			} `json:"rp"`
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &opts); err != nil {
		return nil, err
	}
	if opts.PublicKey.RP.ID != a.RPID {
		return nil, fmt.Errorf("webauthntest: options for %q, not %q", opts.PublicKey.RP.ID, a.RPID)
	}
	handle, err := b64.DecodeString(opts.PublicKey.User.ID)
	if err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	c := &credential{id: make([]byte, 32), userHandle: handle, key: key}
	_, _ = rand.Read(c.id)
	a.creds = append(a.creds, c)

	pub, err := key.PublicKey.ECDH()
	if err != nil {
		return nil, err
	}
	point := pub.Bytes() // 0x04 || x || y
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: point[1:33], -3: point[33:]})
	if err != nil {
		return nil, err
	}
	auth := a.authData(flagUP|flagUV|flagAT, 0)
	auth = append(auth, make([]byte, 16)...)                      // AAGUID
	auth = binary.BigEndian.AppendUint16(auth, uint16(len(c.id))) //nolint:gosec // G115: the ID is 32 bytes
	auth = append(auth, c.id...)
	auth = append(auth, cose...)
	attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": auth})
	if err != nil {
		return nil, err
	}
	client, err := a.clientData("webauthn.create", opts.PublicKey.Challenge)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(c.id), "rawId": b64.EncodeToString(c.id), "type": "public-key", "authenticatorAttachment": "platform",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(client), "attestationObject": b64.EncodeToString(attestation),
			"transports": []string{"internal"}},
		"clientExtensionResults": map[string]any{},
	})
}

// Get signs in with a passkey from navigator.credentials.get options and
// returns the browser's response JSON. With allowCredentials it uses one of
// those; without (a discoverable sign-in) the first it holds.
func (a *Authenticator) Get(options []byte) ([]byte, error) {
	var opts struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RPID      string `json:"rpId"`
			Allow     []struct {
				ID string `json:"id"`
			} `json:"allowCredentials"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &opts); err != nil {
		return nil, err
	}
	if opts.PublicKey.RPID != "" && opts.PublicKey.RPID != a.RPID {
		return nil, fmt.Errorf("webauthntest: options for %q, not %q", opts.PublicKey.RPID, a.RPID)
	}
	c := a.pick(opts.PublicKey.Allow)
	if c == nil {
		return nil, errors.New("webauthntest: no passkey for these options")
	}
	c.count++
	auth := a.authData(flagUP|flagUV, c.count)
	client, err := a.clientData("webauthn.get", opts.PublicKey.Challenge)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(client)
	digest := sha256.Sum256(append(append([]byte{}, auth...), sum[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, c.key, digest[:])
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"id": b64.EncodeToString(c.id), "rawId": b64.EncodeToString(c.id), "type": "public-key", "authenticatorAttachment": "platform",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(client), "authenticatorData": b64.EncodeToString(auth),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(c.userHandle)},
		"clientExtensionResults": map[string]any{},
	})
}

func (a *Authenticator) pick(allow []struct {
	ID string `json:"id"`
}) *credential {
	if len(allow) == 0 && len(a.creds) > 0 {
		return a.creds[0]
	}
	for _, al := range allow {
		for _, c := range a.creds {
			if b64.EncodeToString(c.id) == al.ID {
				return c
			}
		}
	}
	return nil
}

func (a *Authenticator) authData(flags byte, count uint32) []byte {
	rp := sha256.Sum256([]byte(a.RPID))
	out := append(rp[:], flags)
	return binary.BigEndian.AppendUint32(out, count)
}

func (a *Authenticator) clientData(typ, challenge string) ([]byte, error) {
	return json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": a.Origin, "crossOrigin": false})
}
