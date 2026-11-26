package client

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Scopes that can be requested with OAuth 2.0.
const (
	ScopeRESTWebServices = "rest_webservices"
	ScopeRESTlets        = "restlets"
)

// JWT signing algorithms accepted by NetSuite.
const (
	algES256 = "ES256"
	algES384 = "ES384"
	algES512 = "ES512"
	algPS256 = "PS256"
)

// tokenRefreshMargin is how long before expiry an access token is replaced.
const tokenRefreshMargin = 5 * time.Minute

// assertionLifetime is how long the signed request for a token is valid,
// which NetSuite requires to be under an hour.
const assertionLifetime = 5 * time.Minute

// MaxCertificateValidity is the longest validity NetSuite accepts for client
// credentials certificates.
const MaxCertificateValidity = 2 * 365 * 24 * time.Hour

// M2M provides OAuth 2.0 client credentials authentication, NetSuite's
// machine to machine flow. Access tokens are requested with a JWT signed by
// the private key of a certificate that the account's administrator uploads
// in NetSuite, under Setup > Integration > Manage Authentication > OAuth 2.0
// Client Credentials (M2M) Setup, where it's mapped to an entity, role and
// integration. There are no refresh tokens: new access tokens are requested
// when needed, for as long as the certificate is valid.
type M2M struct {
	// ClientID is the client ID of the integration record.
	ClientID string
	// CertificateID is the ID NetSuite assigns to the uploaded certificate.
	CertificateID string
	// PrivateKey is the key of the certificate: ECDSA P-256, P-384 or P-521,
	// or RSA with at least 3072 bits.
	PrivateKey crypto.Signer
	// Scopes to request, REST web services and RESTlets by default.
	Scopes []string
	// TokenURL overrides the account's token endpoint, for tests.
	TokenURL string
	// Transport overrides the HTTP transport used, http.DefaultTransport by
	// default.
	Transport http.RoundTripper

	mu     sync.Mutex
	tokens map[string]*accessToken
}

type accessToken struct {
	value   string
	expires time.Time
}

// HTTPClient returns an HTTP client that authenticates requests with access
// tokens for the account.
func (a *M2M) HTTPClient(_ context.Context, accountID string) *http.Client {
	base := a.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{Transport: &m2mTransport{auth: a, accountID: accountID, base: base}}
}

type m2mTransport struct {
	auth      *M2M
	accountID string
	base      http.RoundTripper
}

func (t *m2mTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := t.auth.Token(req.Context(), t.accountID, t.base)
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+tok)
	return t.base.RoundTrip(r)
}

// TokenError is returned when NetSuite refuses to issue an access token.
type TokenError struct {
	StatusCode  int
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

// Error implements the error interface.
func (e *TokenError) Error() string {
	msg := fmt.Sprintf("netsuite: token request: %d", e.StatusCode)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Description != "" {
		msg += ": " + e.Description
	}
	return msg
}

// Token provides a valid access token for the account, requesting a new one
// when there is none or it is about to expire.
func (a *M2M) Token(ctx context.Context, accountID string, rt http.RoundTripper) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.tokens[accountID]; ok && time.Now().Add(tokenRefreshMargin).Before(t.expires) {
		return t.value, nil
	}
	t, err := a.requestToken(ctx, accountID, rt)
	if err != nil {
		return "", err
	}
	if a.tokens == nil {
		a.tokens = make(map[string]*accessToken)
	}
	a.tokens[accountID] = t
	return t.value, nil
}

func (a *M2M) tokenURL(accountID string) string {
	if a.TokenURL != "" {
		return a.TokenURL
	}
	return fmt.Sprintf(defaultHostPattern, AccountHost(accountID)) + "/services/rest/auth/oauth2/v1/token"
}

func (a *M2M) requestToken(ctx context.Context, accountID string, rt http.RoundTripper) (*accessToken, error) {
	tokenURL := a.tokenURL(accountID)
	assertion, err := a.assertion(tokenURL, time.Now())
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	res, err := rt.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("netsuite: token request: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("netsuite: token request: %w", err)
	}
	if res.StatusCode >= http.StatusBadRequest {
		te := &TokenError{StatusCode: res.StatusCode}
		if json.Unmarshal(body, te) != nil || (te.Code == "" && te.Description == "") {
			te.Description = strings.TrimSpace(string(body))
		}
		return nil, te
	}
	out := struct {
		AccessToken string      `json:"access_token"`
		ExpiresIn   json.Number `json:"expires_in"`
	}{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("netsuite: token response: %w", err)
	}
	if out.AccessToken == "" {
		return nil, errors.New("netsuite: token response without an access token")
	}
	secs, err := out.ExpiresIn.Int64()
	if err != nil || secs <= 0 {
		secs = 3600
	}
	return &accessToken{value: out.AccessToken, expires: start.Add(time.Duration(secs) * time.Second)}, nil
}

// assertion builds the signed JWT used to request an access token.
func (a *M2M) assertion(tokenURL string, now time.Time) (string, error) {
	if a.ClientID == "" || a.CertificateID == "" || a.PrivateKey == nil {
		return "", errors.New("netsuite: client ID, certificate ID and private key are required")
	}
	alg, err := signingAlgorithm(a.PrivateKey)
	if err != nil {
		return "", err
	}
	scopes := a.Scopes
	if len(scopes) == 0 {
		scopes = []string{ScopeRESTWebServices, ScopeRESTlets}
	}
	header, err := json.Marshal(map[string]string{"typ": "JWT", "alg": alg, "kid": a.CertificateID})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss":   a.ClientID,
		"scope": strings.Join(scopes, ","),
		"aud":   tokenURL,
		"iat":   now.Unix(),
		"exp":   now.Add(assertionLifetime).Unix(),
	})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	sig, err := sign(a.PrivateKey, alg, []byte(signing))
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// signingAlgorithm picks the JWT algorithm for the key, as accepted by
// NetSuite: ES256/384/512 for EC keys, and PS256 for RSA keys.
func signingAlgorithm(key crypto.Signer) (string, error) {
	switch k := key.Public().(type) {
	case *ecdsa.PublicKey:
		switch k.Curve {
		case elliptic.P256():
			return algES256, nil
		case elliptic.P384():
			return algES384, nil
		case elliptic.P521():
			return algES512, nil
		}
		return "", errors.New("netsuite: unsupported EC curve, use P-256, P-384 or P-521")
	case *rsa.PublicKey:
		if k.N.BitLen() < 3072 {
			return "", errors.New("netsuite: RSA keys must have at least 3072 bits")
		}
		return algPS256, nil
	}
	return "", errors.New("netsuite: unsupported private key type")
}

func sign(key crypto.Signer, alg string, data []byte) ([]byte, error) {
	switch alg {
	case algPS256:
		h := sha256.Sum256(data)
		return key.Sign(rand.Reader, h[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: crypto.SHA256})
	case algES256, algES384, algES512:
		k, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("netsuite: EC signing needs an *ecdsa.PrivateKey")
		}
		var digest []byte
		switch alg {
		case algES256:
			h := sha256.Sum256(data)
			digest = h[:]
		case algES384:
			h := sha512.Sum384(data)
			digest = h[:]
		default:
			h := sha512.Sum512(data)
			digest = h[:]
		}
		r, s, err := ecdsa.Sign(rand.Reader, k, digest)
		if err != nil {
			return nil, err
		}
		// JWS uses the fixed size concatenation of r and s.
		size := (k.Curve.Params().BitSize + 7) / 8
		sig := make([]byte, 2*size)
		r.FillBytes(sig[:size])
		s.FillBytes(sig[size:])
		return sig, nil
	}
	return nil, fmt.Errorf("netsuite: unsupported algorithm %s", alg)
}

// ParsePrivateKey reads a PEM encoded private key, in PKCS #8, SEC 1 (EC)
// or PKCS #1 (RSA) form.
func ParsePrivateKey(data []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("netsuite: no PEM data in private key")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if s, ok := k.(crypto.Signer); ok {
			return s, nil
		}
		return nil, errors.New("netsuite: unsupported private key type")
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, errors.New("netsuite: could not parse private key")
}

// Certificate is a key pair for the client credentials flow: the private key
// is kept by the application, and the certificate uploaded to NetSuite.
type Certificate struct {
	PrivateKeyPEM  []byte
	CertificatePEM []byte
	NotAfter       time.Time
}

// GenerateCertificate creates an ECDSA P-256 key and a self-signed
// certificate for it, valid for the period given, up to the two years
// NetSuite allows.
func GenerateCertificate(commonName string, validity time.Duration) (*Certificate, error) {
	if validity <= 0 || validity > MaxCertificateValidity {
		validity = MaxCertificateValidity
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &Certificate{
		PrivateKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		CertificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		NotAfter:       tmpl.NotAfter,
	}, nil
}
