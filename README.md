# GOBL <-> NetSuite Convertor

Convert NetSuite records to GOBL documents, including a minimal client for the NetSuite REST web services used to fetch them.

Copyright [Invopop Ltd.](https://invopop.com) 2026. Released publicly under the [Apache License Version 2.0](LICENSE). For commercial licenses please contact the [dev team at invopop](mailto:dev@invopop.com). In order to accept contributions to this library we will require transferring copyrights to Invopop Ltd.

## Status

Early development. The REST client and a `probe` command for exploring account data are available; conversion to GOBL is not yet implemented.

## Packages

- `github.com/invopop/gobl.netsuite` - conversion between NetSuite records and GOBL (coming soon).
- `github.com/invopop/gobl.netsuite/client` - NetSuite REST web services client supporting the record and SuiteQL APIs.

## NetSuite Setup

The client uses the [REST web services](https://docs.oracle.com/en/cloud/saas/netsuite/ns-online-help/chapter_1540391670.html) hosted at `https://<account>.suitetalk.api.netsuite.com`. In the NetSuite account:

1. Enable **Setup > Company > Enable Features > SuiteCloud**: _REST Web Services_ and _Token-Based Authentication_.
2. Create an **Integration** record (Setup > Integration > Manage Integrations) with _Token-Based Authentication_ checked and _REST Web Services_ in scope. Note the consumer key and secret.
3. Use a role with the _REST Web Services_ and _Log in using Access Tokens_ permissions, plus view access to Transactions (Invoice), Customers and Subsidiaries.
4. Create an **Access Token** (Setup > Users/Roles > Access Tokens) for the integration, user and role. Note the token ID and secret.

> Token-Based Authentication (TBA) can't be used by new integrations from NetSuite 2027.1, and support is planned to end in 2028.2. OAuth 2.0 support will be added.

## Command Line

Copy `.env.example` to `.env` and fill in the credentials, then:

```bash
# List recently modified invoices
go run ./cmd/gobl.netsuite probe invoices

# Fetch an invoice with its customer and subsidiary records
go run ./cmd/gobl.netsuite probe invoice 1234
go run ./cmd/gobl.netsuite probe invoice 1234 -o probe/

# Run any SuiteQL query
go run ./cmd/gobl.netsuite probe query "SELECT id, name FROM subsidiary"

# GET any path under /services/rest, e.g. the record metadata
go run ./cmd/gobl.netsuite probe get record/v1/metadata-catalog/invoice
```
