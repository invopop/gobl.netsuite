# GOBL <-> NetSuite Convertor

Convert NetSuite records to GOBL documents, including a minimal client for the NetSuite REST web services used to fetch them.

Copyright [Invopop Ltd.](https://invopop.com) 2026. Released publicly under the [Apache License Version 2.0](LICENSE). For commercial licenses please contact the [dev team at invopop](mailto:dev@invopop.com). In order to accept contributions to this library we will require transferring copyrights to Invopop Ltd.

## Status

Early development. NetSuite invoices can be converted into GOBL invoices for accounts that:

- use **legacy tax** (a tax code per line) rather than SuiteTax,
- are **OneWorld**, as the supplier is taken from the invoice's subsidiary.

Discount lines become line discounts when they directly follow a line with the same tax code, and invoice discounts with their own tax code otherwise (e.g. after a subtotal), matching how NetSuite taxes them. Header discounts are shared between tax codes in proportion to their net amounts, as NetSuite does. Totals use GOBL's `currency` rounding, as NetSuite rounds line amounts and the tax of each rate before summing.

Not converted yet, reported as unmapped and so causing the totals check to fail: shipping costs, markup and payment lines, and discounts applied after tax. Credit memos are not supported yet.

**Known difference:** with a header discount across several tax rates, NetSuite rounds the tax of the lines and of the discount separately, so its tax total can differ by a cent per rate from the tax of the net base calculated by GOBL. See `examples/netsuite/pending`.

## Packages

- `github.com/invopop/gobl.netsuite` - conversion between NetSuite records and GOBL.
- `github.com/invopop/gobl.netsuite/client` - NetSuite REST web services client supporting the record and SuiteQL APIs.

## Usage

### Go Package

Conversion works on a `Bundle`: the raw NetSuite invoice record, plus the customer, subsidiary, currency and tax code records it refers to. Records are kept exactly as returned by the REST API, including custom fields.

```go
nc, _ := client.New(accountID, &client.TBA{ /* credentials */ })

b, err := goblnetsuite.FetchInvoice(ctx, nc, "1234")
if err != nil {
	return err
}

res, err := goblnetsuite.FromInvoice(b)
if err != nil {
	return err
}

// Apply any mappings to res.Invoice here, then calculate and validate.
env, err := gobl.Envelop(res.Invoice)
if err != nil {
	return err
}
if err := env.Calculate(); err != nil {
	return err
}
if err := env.Validate(); err != nil {
	return err
}

// Compare the GOBL totals with those calculated by NetSuite.
if err := res.CheckTotals(); err != nil {
	return err
}
```

The `Result` also provides:

- `Source`: the bundle the invoice was converted from.
- `SourceLines`: the raw NetSuite item line for each GOBL line, in the same order, as some NetSuite lines (e.g. subtotals) do not produce a GOBL line.
- `Unmapped`: source data that was not converted, such as custom fields (`custbody*`, `custcol*`) or unsupported line types.

### Examples

`examples/netsuite` contains bundles taken from a test account, with links removed to anonymise the account. The expected GOBL output for each is in `examples/netsuite/out`, and is regenerated with:

```bash
go test -run TestExamples -update
```

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

# Fetch an invoice bundle, or convert it directly into GOBL
go run ./cmd/gobl.netsuite probe invoice 1234 -o probe/invoice_1234.json
go run ./cmd/gobl.netsuite probe invoice 1234 --convert

# Create a new example fixture
go run ./cmd/gobl.netsuite probe invoice 1234 --strip-links -o examples/netsuite/invoice_new.json

# Create a record from JSON, e.g. test data, printing its internal ID
go run ./cmd/gobl.netsuite probe create invoice invoice.json

# Convert a bundle file into a GOBL envelope
go run ./cmd/gobl.netsuite convert examples/netsuite/invoice_basic.json

# Run any SuiteQL query
go run ./cmd/gobl.netsuite probe query "SELECT id, name FROM subsidiary"

# Fetch the account specific JSON Schema for a record type
go run ./cmd/gobl.netsuite probe schema invoice

# GET any path under /services/rest, e.g. a tax code or currency
go run ./cmd/gobl.netsuite probe get record/v1/salestaxitem/6
```
