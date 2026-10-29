package client_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/invopop/gobl.netsuite/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeM2M acts as NetSuite's token endpoint, checking the signed request with
// the certificate's public key, and as a REST endpoint requiring the token.
type fakeM2M struct {
	t        *testing.T
	pub      *ecdsa.PublicKey
	requests atomic.Int32
	fail     string
	srv      *httptest.Server
}

func newFakeM2M(t *testing.T, certPEM []byte) *fakeM2M {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	f := &fakeM2M{t: t, pub: cert.PublicKey.(*ecdsa.PublicKey)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeM2M) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		f.requests.Add(1)
		if f.fail != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"` + f.fail + `"}`))
			return
		}
		assert.Equal(f.t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
		require.NoError(f.t, r.ParseForm())
		assert.Equal(f.t, "client_credentials", r.Form.Get("grant_type"))
		assert.Equal(f.t, "urn:ietf:params:oauth:client-assertion-type:jwt-bearer", r.Form.Get("client_assertion_type"))
		f.checkAssertion(r.Form.Get("client_assertion"))
		_, _ = w.Write([]byte(`{"access_token":"access-` + strconv.Itoa(int(f.requests.Load())) + `","expires_in":3600,"token_type":"Bearer"}`))
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer access-") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	_, _ = w.Write([]byte(`{"id":"1","auth":"` + r.Header.Get("Authorization") + `"}`))
}

func (f *fakeM2M) checkAssertion(jwt string) {
	parts := strings.Split(jwt, ".")
	require.Len(f.t, parts, 3)
	dec := base64.RawURLEncoding
	var header, claims map[string]any
	h, _ := dec.DecodeString(parts[0])
	c, _ := dec.DecodeString(parts[1])
	require.NoError(f.t, json.Unmarshal(h, &header))
	require.NoError(f.t, json.Unmarshal(c, &claims))
	assert.Equal(f.t, map[string]any{"typ": "JWT", "alg": "ES256", "kid": "cert-1"}, header)
	assert.Equal(f.t, "client-1", claims["iss"])
	assert.Equal(f.t, "rest_webservices,restlets", claims["scope"])
	assert.Equal(f.t, f.srv.URL+"/token", claims["aud"])
	iat, exp := claims["iat"].(float64), claims["exp"].(float64)
	assert.Less(f.t, exp-iat, float64(3600), "under an hour")

	sig, _ := dec.DecodeString(parts[2])
	require.Len(f.t, sig, 64)
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	assert.True(f.t, ecdsa.Verify(f.pub, digest[:], r, s), "signature is valid")
}

func newM2M(t *testing.T) (*client.M2M, *client.Certificate) {
	t.Helper()
	cert, err := client.GenerateCertificate("Invopop", 0)
	require.NoError(t, err)
	key, err := client.ParsePrivateKey(cert.PrivateKeyPEM)
	require.NoError(t, err)
	return &client.M2M{ClientID: "client-1", CertificateID: "cert-1", PrivateKey: key}, cert
}

func TestM2M(t *testing.T) {
	auth, cert := newM2M(t)
	f := newFakeM2M(t, cert.CertificatePEM)
	auth.TokenURL = f.srv.URL + "/token"

	nc, err := client.New("1234567", auth, client.WithBaseURL(f.srv.URL))
	require.NoError(t, err)
	for range 3 {
		out := struct {
			Auth string `json:"auth"`
		}{}
		require.NoError(t, nc.GetRecord(t.Context(), "invoice", "1", false, &out))
		assert.Equal(t, "Bearer access-1", out.Auth)
	}
	assert.Equal(t, int32(1), f.requests.Load(), "tokens are reused until they expire")
}

func TestM2MTokenError(t *testing.T) {
	auth, cert := newM2M(t)
	f := newFakeM2M(t, cert.CertificatePEM)
	f.fail = "The certificate is revoked"
	auth.TokenURL = f.srv.URL + "/token"

	nc, err := client.New("1234567", auth, client.WithBaseURL(f.srv.URL))
	require.NoError(t, err)
	err = nc.GetRecord(t.Context(), "invoice", "1", false, nil)
	var te *client.TokenError
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "invalid_grant", te.Code)
	assert.Contains(t, err.Error(), "The certificate is revoked")
}

func TestM2MMissingDetails(t *testing.T) {
	auth, _ := newM2M(t)
	auth.CertificateID = ""
	_, err := auth.Token(t.Context(), "1234567", http.DefaultTransport)
	assert.ErrorContains(t, err, "certificate ID and private key are required")
}

func TestGenerateCertificate(t *testing.T) {
	cert, err := client.GenerateCertificate("Invopop test", 30*24*time.Hour)
	require.NoError(t, err)
	block, _ := pem.Decode(cert.CertificatePEM)
	require.NotNil(t, block)
	assert.Equal(t, "CERTIFICATE", block.Type)
	x, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "Invopop test", x.Subject.CommonName)
	assert.WithinDuration(t, time.Now().Add(30*24*time.Hour), x.NotAfter, time.Minute)

	cert, err = client.GenerateCertificate("Invopop", 10*365*24*time.Hour)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(client.MaxCertificateValidity), cert.NotAfter, time.Minute, "capped at two years")
}

func TestParsePrivateKey(t *testing.T) {
	_, err := client.ParsePrivateKey([]byte("nope"))
	assert.ErrorContains(t, err, "no PEM data")

	// RSA keys need at least 3072 bits to be used.
	small, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der := x509.MarshalPKCS1PrivateKey(small)
	key, err := client.ParsePrivateKey(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
	require.NoError(t, err)
	auth := &client.M2M{ClientID: "c", CertificateID: "k", PrivateKey: key}
	_, err = auth.Token(t.Context(), "1", http.DefaultTransport)
	assert.ErrorContains(t, err, "at least 3072 bits")
}

func TestM2MRSA(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	require.NoError(t, err)
	var assertion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		assertion = r.Form.Get("client_assertion")
		_, _ = w.Write([]byte(`{"access_token":"a","expires_in":3600}`))
	}))
	defer srv.Close()

	auth := &client.M2M{ClientID: "c", CertificateID: "k", PrivateKey: key, TokenURL: srv.URL}
	_, err = auth.Token(t.Context(), "1", http.DefaultTransport)
	require.NoError(t, err)

	parts := strings.Split(assertion, ".")
	require.Len(t, parts, 3)
	h, _ := base64.RawURLEncoding.DecodeString(parts[0])
	assert.Contains(t, string(h), `"alg":"PS256"`)
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	assert.NoError(t, rsa.VerifyPSS(&key.PublicKey, crypto.SHA256, digest[:], sig, nil))
}
