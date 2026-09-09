// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nke/controller/internal/auth"
	"github.com/NVIDIA/nke/controller/internal/config"
	"github.com/NVIDIA/nke/controller/internal/db"
	"github.com/NVIDIA/nke/controller/internal/server"
	"github.com/NVIDIA/nke/controller/internal/testutil"
)

type launchLayerPublicKey struct{ key crypto.PublicKey }

func (source launchLayerPublicKey) KeyFor(context.Context, string) (crypto.PublicKey, error) {
	return source.key, nil
}

func TestLaunchLayerAuthenticatedHTTPAdmission(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	verifier, err := auth.NewVerifier(testAuthConfig(), map[string]auth.KeySource{
		"test-kubernetes": launchLayerPublicKey{key: &key.PublicKey},
	})
	require.NoError(t, err)
	authn, err := auth.NewAuthenticator(verifier, auth.ModeEnforcing, discardLogger(), nil)
	require.NoError(t, err)
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://kubernetes.default.svc.cluster.local",
		"sub": "system:serviceaccount:nke-clients:runner",
		"aud": []string{"https://cwe.test.invalid"},
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"kubernetes.io": map[string]any{"namespace": "nke-clients", "serviceaccount": map[string]any{"name": "runner"}},
	})
	token.Header["kid"] = "example-test-key"
	signed, err := token.SignedString(key)
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		bearer  string
		invalid bool
		status  int
	}{
		{"unauthenticated", "", true, http.StatusUnauthorized},
		{"authenticated invalid intent", signed, true, http.StatusBadRequest},
		{"authenticated accepted intent", signed, false, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &launchLayerRecordingTx{}
			database := &testutil.MockDB{GetTx: func() db.Tx { return tx }}
			if tc.status == http.StatusCreated {
				database.On("RunInTx", mock.Anything, mock.Anything).Return(nil).Once()
			}
			srv, err := server.NewServer(config.Config{}, discardLogger(), database, nil, authn)
			require.NoError(t, err)
			body := launchLayerCreateRequest(false, false)
			if tc.invalid {
				delete(body.Infra.Cluster["spec"].(map[string]any), "owner")
			}
			data, err := json.Marshal(body)
			require.NoError(t, err)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/clusters", bytes.NewReader(data))
			req.Header.Set("Content-Type", "application/json")
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			response := httptest.NewRecorder()
			srv.Handler.ServeHTTP(response, req)
			require.Equal(t, tc.status, response.Code, response.Body.String())
			if tc.status != http.StatusCreated {
				database.AssertNotCalled(t, "RunInTx", mock.Anything, mock.Anything)
				require.Empty(t, tx.clusters)
				require.Empty(t, tx.outboxes)
			} else {
				require.Len(t, tx.clusters, 1)
			}
			database.AssertExpectations(t)
		})
	}
}
