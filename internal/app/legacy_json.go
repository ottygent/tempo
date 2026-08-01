package app

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// legacyAuthFile is read only during an explicit migration. Normal runtime
// persistence never reads from or writes to JSON files.
type legacyAuthFile struct {
	Username        string `json:"username"`
	Email           string `json:"email,omitempty"`
	Salt            string `json:"salt"`
	PasswordHash    string `json:"passwordHash"`
	SessionSecret   string `json:"sessionSecret"`
	PBKDFIterations int    `json:"pbkdfIterations"`
}

func loadLegacyStateFile(path string) (State, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(body, &state); err != nil {
		return State{}, fmt.Errorf("decode legacy state: %w", err)
	}
	return state, nil
}

func loadLegacyAuthFile(path string) (authCredentials, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return authCredentials{}, err
	}
	var legacy legacyAuthFile
	if err := json.Unmarshal(body, &legacy); err != nil {
		return authCredentials{}, fmt.Errorf("decode legacy auth: %w", err)
	}
	salt, err := hex.DecodeString(legacy.Salt)
	if err != nil {
		return authCredentials{}, errorsWithContext("decode legacy auth salt", err)
	}
	hash, err := hex.DecodeString(legacy.PasswordHash)
	if err != nil {
		return authCredentials{}, errorsWithContext("decode legacy password hash", err)
	}
	secret, err := hex.DecodeString(legacy.SessionSecret)
	if err != nil {
		return authCredentials{}, errorsWithContext("decode legacy session secret", err)
	}
	return authCredentials{
		Username: legacy.Username,
		Email:    legacy.Email,
		Password: passwordDigest{
			Algorithm:       passwordAlgorithmPBKDF2,
			Salt:            salt,
			Hash:            hash,
			PBKDFIterations: legacy.PBKDFIterations,
		},
		SessionSecret: secret,
	}, nil
}

func errorsWithContext(message string, err error) error {
	return fmt.Errorf("%s: %w", message, err)
}
