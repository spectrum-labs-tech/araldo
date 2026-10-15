// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/keyring"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Channels, ad accounts, analytics sources and mail accounts keep the
// same two halves of what a provider needs: settings in the clear, and
// secrets encrypted as one JSON object (ADR 0008). These put them
// together and apart.

// mergeCredentials is settings with secrets over them, as adapters take
// them.
func mergeCredentials(settings, secrets map[string]string) platform.Credentials {
	creds := platform.Credentials{}
	maps.Copy(creds, settings)
	maps.Copy(creds, secrets)
	return creds
}

// sealSecrets encrypts secrets for the row aad names; none seal to nil.
func sealSecrets(ctx context.Context, keys *keyring.Keyring, orgID uuid.UUID, aad string, secrets map[string]string) ([]byte, error) {
	if len(secrets) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(secrets)
	if err != nil {
		return nil, err
	}
	return keys.Encrypt(ctx, orgID, aad, raw)
}

// openSecrets decrypts what sealSecrets sealed; nil opens to no secrets.
func openSecrets(ctx context.Context, keys *keyring.Keyring, orgID uuid.UUID, aad string, sealed []byte) (map[string]string, error) {
	secrets := map[string]string{}
	if len(sealed) == 0 {
		return secrets, nil
	}
	raw, err := keys.Decrypt(ctx, orgID, aad, sealed)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &secrets); err != nil {
		return nil, err
	}
	return secrets, nil
}

// openCredentials is the settings with the sealed secrets merged in.
func openCredentials(ctx context.Context, keys *keyring.Keyring, orgID uuid.UUID, aad string, settings map[string]string,
	sealed []byte) (platform.Credentials, error) {
	secrets, err := openSecrets(ctx, keys, orgID, aad, sealed)
	if err != nil {
		return nil, err
	}
	return mergeCredentials(settings, secrets), nil
}
