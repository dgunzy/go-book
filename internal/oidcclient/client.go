// Package oidcclient provides a provider-neutral OIDC authorization-code client.
package oidcclient

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type Config struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	HTTPTimeout  time.Duration
}

type Claims struct {
	Subject       string
	Email         string
	EmailVerified bool
	DisplayName   string
	PictureURL    string
}

type Client struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	client   *http.Client
}

func New(ctx context.Context, config Config) (*Client, error) {
	timeout := config.HTTPTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	httpClient := &http.Client{Timeout: timeout}
	discoveryContext := oidc.ClientContext(ctx, httpClient)
	provider, err := oidc.NewProvider(discoveryContext, config.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	return &Client{
		oauth: oauth2.Config{
			ClientID: config.ClientID, ClientSecret: config.ClientSecret,
			RedirectURL: config.RedirectURL, Endpoint: provider.Endpoint(),
			Scopes: []string{oidc.ScopeOpenID, "profile", "email"},
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: config.ClientID}),
		client:   httpClient,
	}, nil
}

func (client *Client) AuthorizationURL(state, nonce, verifier string) string {
	return client.oauth.AuthCodeURL(
		state,
		oauth2.AccessTypeOnline,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
	)
}

func (client *Client) Exchange(ctx context.Context, code, verifier, expectedNonce string) (Claims, error) {
	requestContext := oidc.ClientContext(ctx, client.client)
	token, err := client.oauth.Exchange(requestContext, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Claims{}, fmt.Errorf("exchange authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return Claims{}, fmt.Errorf("OIDC response did not contain an ID token")
	}
	idToken, err := client.verifier.Verify(requestContext, rawIDToken)
	if err != nil {
		return Claims{}, fmt.Errorf("verify ID token: %w", err)
	}
	if !constantTimeEqual(idToken.Nonce, expectedNonce) {
		return Claims{}, fmt.Errorf("ID token nonce did not match the login attempt")
	}

	var rawClaims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
	}
	if err := idToken.Claims(&rawClaims); err != nil {
		return Claims{}, fmt.Errorf("decode ID token claims: %w", err)
	}
	claims := Claims{
		Subject: idToken.Subject, Email: strings.ToLower(strings.TrimSpace(rawClaims.Email)),
		EmailVerified: rawClaims.EmailVerified, DisplayName: strings.TrimSpace(rawClaims.Name),
		PictureURL: strings.TrimSpace(rawClaims.Picture),
	}
	if err := claims.Validate(); err != nil {
		return Claims{}, err
	}
	return claims, nil
}

func (claims Claims) Validate() error {
	if strings.TrimSpace(claims.Subject) == "" {
		return fmt.Errorf("ID token subject is required")
	}
	if claims.Email == "" || !claims.EmailVerified {
		return fmt.Errorf("a verified email is required")
	}
	return nil
}

func constantTimeEqual(left, right string) bool {
	if len(left) != len(right) || len(left) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
