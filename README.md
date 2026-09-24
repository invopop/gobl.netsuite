# GOBL <-> NetSuite Convertor

Convert NetSuite records to GOBL documents, including a minimal client for the NetSuite REST web services used to fetch them.

Copyright [Invopop Ltd.](https://invopop.com) 2026. Released publicly under the [Apache License Version 2.0](LICENSE). For commercial licenses please contact the [dev team at invopop](mailto:dev@invopop.com). In order to accept contributions to this library we will require transferring copyrights to Invopop Ltd.

## Status

Early development. The REST client and a `probe` command for exploring account data are available; conversion to GOBL is not yet implemented.

## Packages

- `github.com/invopop/gobl.netsuite` - conversion between NetSuite records and GOBL (coming soon).
- `github.com/invopop/gobl.netsuite/client` - NetSuite REST web services client supporting the record and SuiteQL APIs.

## NetSuite Setup

The client uses the [REST web services](https://docs.oracle.com/en/cloud/saas/netsuite/ns-online-help/chapter_1540391670.html) hosted at `https://<account>.suitetalk.api.netsuite.com`, authenticated with Token-Based Authentication (TBA). Five values are needed:

| Variable                   | Where it comes from                                                    |
| -------------------------- | ---------------------------------------------------------------------- |
| `NETSUITE_ACCOUNT_ID`      | Setup > Company > Company Information, e.g. `1234567` or `1234567_SB1` |
| `NETSUITE_CONSUMER_KEY`    | Integration record (step 2)                                            |
| `NETSUITE_CONSUMER_SECRET` | Integration record (step 2)                                            |
| `NETSUITE_TOKEN_ID`        | Access token (step 3)                                                  |
| `NETSUITE_TOKEN_SECRET`    | Access token (step 3)                                                  |

The secrets are only displayed once, when the record is saved. If one is lost, use _Reset Credentials_ on the integration, or revoke the token and create a new one.

### 1. Enable features

**Setup > Company > Enable Features > SuiteCloud**, then check:

- _SuiteTalk (Web Services)_: **REST Web Services**
- _Manage Authentication_: **Token-Based Authentication**

### 2. Create an integration record

**Setup > Integration > Manage Integrations > New**:

- Name it (e.g. "Invopop") and set the state to _Enabled_.
- On the _Authentication_ tab, check **Token-Based Authentication**. _TBA: Authorization Flow_ and _Authorization Code Grant_ are not required.
- Save, and copy the **Consumer Key** and **Consumer Secret** shown at the bottom of the page.

### 3. Create an access token

**For testing in your own account**, the quickest option is a token for your own user with the Administrator role. NetSuite does not offer the Administrator role in _Setup > Users/Roles > Access Tokens > New_, so instead:

1. Log in with the Administrator role.
2. On the home dashboard, open **Manage Access Tokens** from the _Settings_ portlet (add the portlet with _Personalize Dashboard_ if missing).
3. Click **New My Access Token**, select the integration from step 2, and save.
4. Copy the **Token ID** and **Token Secret**.

An Administrator token has full access to the account, so revoke it once testing is complete.

**For production use**, create a dedicated role (Setup > Users/Roles > Manage Roles > New) with only the permissions required:

- _Setup_: Log in using Access Tokens, REST Web Services
- _Transactions_: Invoice (View), Find Transaction (View, needed for SuiteQL)
- _Lists_: Customers, Subsidiaries, Items (View)

Assign the role to the integration user (Lists > Employees > Access > Roles), then create the token in **Setup > Users/Roles > Access Tokens > New**, selecting the integration, user and role.

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
