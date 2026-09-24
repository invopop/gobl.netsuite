package client

import (
	"context"
	"net/http"

	"github.com/dghubble/oauth1"
)

// Auth provides an HTTP client that signs or otherwise authenticates
// requests to a NetSuite account.
type Auth interface {
	HTTPClient(ctx context.Context, accountID string) *http.Client
}

// TBA holds the credentials for NetSuite Token-Based Authentication
// (OAuth 1.0a with HMAC-SHA256 signatures).
//
// NetSuite will not allow new integrations to use TBA from 2027.1, with
// full end of support planned for 2028.2, so an OAuth 2.0 implementation
// of Auth will be needed for new accounts.
type TBA struct {
	ConsumerKey    string
	ConsumerSecret string
	TokenID        string
	TokenSecret    string
}

// HTTPClient returns an HTTP client that signs requests using TBA.
func (a *TBA) HTTPClient(ctx context.Context, accountID string) *http.Client {
	conf := oauth1.Config{
		ConsumerKey:    a.ConsumerKey,
		ConsumerSecret: a.ConsumerSecret,
		Signer: &oauth1.HMAC256Signer{
			ConsumerSecret: a.ConsumerSecret,
		},
		Realm: AccountRealm(accountID),
	}
	return conf.Client(ctx, oauth1.NewToken(a.TokenID, a.TokenSecret))
}
