# GOBL <-> NetSuite Convertor

Convert NetSuite records to GOBL documents, including a minimal client for the NetSuite REST web services used to fetch them.

Copyright [Invopop Ltd.](https://invopop.com) 2026. Released publicly under the [Apache License Version 2.0](LICENSE). For commercial licenses please contact the [dev team at invopop](mailto:dev@invopop.com). In order to accept contributions to this library we will require transferring copyrights to Invopop Ltd.

## Status

Early development. NetSuite invoices and credit memos can be converted into GOBL invoices and credit notes for accounts that:

- use **legacy tax** (a tax code per line) rather than SuiteTax,
- are **OneWorld**, as the supplier is taken from the invoice's subsidiary.

Discount lines become line discounts when they directly follow a line with the same tax code, and invoice discounts with their own tax code otherwise (e.g. after a subtotal), matching how NetSuite taxes them. Header discounts are shared between tax codes in proportion to their net amounts, as NetSuite does. Totals use GOBL's `currency` rounding, as NetSuite rounds line amounts and the tax of each rate before summing.

Credit memos refer to the invoices they were created from (`createdFrom`) or applied to (`apply`) as preceding documents. A credit memo with neither is reported as unmapped, as some regimes require the preceding document.

Not converted yet, reported as unmapped and so causing the totals check to fail: shipping costs, markup and payment lines, and discounts applied after tax.

**Header discount rounding:** with a header discount across several tax rates, NetSuite rounds the tax of the lines and of the discount separately, so its tax total can differ by up to a cent per rate from the tax of the net base calculated by GOBL. `CheckTotals` accepts these differences, reporting them in `Result.Warnings`.

## Packages

- `github.com/invopop/gobl.netsuite` - conversion between NetSuite records and GOBL.
- `github.com/invopop/gobl.netsuite/client` - NetSuite REST web services client supporting the record and SuiteQL APIs.

## Usage

### Go Package

Conversion works on a `Bundle`: the raw NetSuite transaction record, plus the customer, subsidiary, currency and tax code records it refers to, and for credit memos the related invoices. Records are kept exactly as returned by the REST API, including custom fields.

```go
import (
	"github.com/invopop/gobl"
	netsuite "github.com/invopop/gobl.netsuite"
	"github.com/invopop/gobl.netsuite/client"
)

nc, _ := client.New(accountID, &client.TBA{ /* credentials */ })

b, err := netsuite.FetchInvoice(ctx, nc, "1234") // or FetchCreditMemo
if err != nil {
	return err
}

res, err := netsuite.Convert(b) // or FromInvoice, FromCreditMemo
if err != nil {
	return err
}

// Mappings have been applied, so calculate and validate.
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
- `Unmapped`: source data that was not converted, such as custom fields (`custbody*`, `custcol*`) or unsupported line types.
- `Warnings`: differences with NetSuite's totals accepted by `CheckTotals`.
- `Mapping`: the effective mapping used, after merging presets and mappings.

### Mappings

A `Mapping` customises the conversion with a table of tax codes and a list of rules. Presets provide mappings for regions, embedded in the library under `mappings/`, and an account can extend or override them with its own:

```go
res, err := netsuite.Convert(b,
	netsuite.WithMapping(accountMapping), // merged after the presets
)
```

By default the `default` preset is used, followed by a preset named after the supplier's country if there is one. `WithPresets(names...)` chooses presets instead, and `WithPresets()` disables them. Mappings are merged in order: tax codes with the same `id`, `code` or `match`, and rules with the same `id`, replace earlier ones, or remove them with `"disabled": true`.

```json
{
  "tax_codes": [
    { "match": { "exempt": true, "nexusCountry.id": "ES" }, "combo": { "key": "exempt", "ext": { "es-verifactu-exempt": "E6" } } },
    { "id": "42", "combo": { "cat": "VAT", "key": "reverse-charge" } }
  ],
  "rules": [
    { "id": "po-number", "jq": ".ordering.code = $source.transaction.custbody_po" },
    { "id": "cn-code", "scope": "line", "jq": "if $src.custcol_cn_code then .item.identities += [{type: \"CN\", code: $src.custcol_cn_code}] end" }
  ]
}
```

**Tax codes** map NetSuite tax codes to a GOBL tax combo. Each entry matches tax codes in one of three ways, in order of priority:

1. `id`: the internal ID of a tax code, specific to an account.
2. `code`: the name of a tax code (its `itemId`, e.g. `S-ES`), which accounts can change. Wildcards can be used, e.g. `UNDEF-*`, and when several match, the one defined last is used.
3. `match`: the fields of the tax code record, e.g. `{"exempt": true, "nexusCountry.id": "ES"}`, including custom fields. Nested fields use dots, missing fields match `false` or `null`, and an empty match applies to all tax codes. When several apply, the entry with the most criteria is used, or if tied, the one defined last.

Instead of a `combo`, an entry can set `reject` with a message, so that lines with the tax code fail the conversion rather than being converted.

Presets use `match`, as NetSuite identifies tax codes by their properties rather than their names, which vary between accounts. The `default` preset converts the standard properties (Exempt, Export, EC Code and Reverse Charge Code) to GOBL tax keys in every country. It also rejects NetSuite's undefined tax codes (`UNDEF-*`), which NetSuite uses when it cannot determine the tax code, for example for imported transactions. They can only be identified by name, as their properties are those of a zero rate. When a combo has neither a `percent` nor a `rate` and is standard or has no key, the percent is taken from the NetSuite line, and the category defaults to the tax code's tax type. Codes without an entry are taxed at the line's rate, and reported as unmapped.

Tax code properties are set consistently in the countries supported by NetSuite's International Tax Reports SuiteApp, which creates the tax codes when a subsidiary is added. Of those, GOBL has a tax regime for: Austria, Belgium, Colombia, Denmark, Finland, France, Germany, Ireland, Italy, Netherlands, New Zealand, Norway, Peru, Poland, Portugal, Singapore, Slovakia, Spain, Sweden, Switzerland and the United Kingdom. Country presets should only be needed for requirements beyond these properties, such as surcharges.

**Rules** are [jq](https://jqlang.org) programs, run with [gojq](https://github.com/itchyny/gojq), that replace part of the converted document with their single output. The `scope` determines what the rule applies to, with `.` the GOBL element and `$src` the NetSuite data it was converted from:

| Scope | `.` | `$src` |
| --- | --- | --- |
| `document` (default) | the invoice | `null` |
| `line` | each line | the item line |
| `line-discount` | each line discount | the discount item line |
| `discount` | each invoice discount | the discount item line, or the transaction for header discounts |
| `customer` | the customer | the customer record |
| `supplier` | the supplier | the subsidiary record |
| `preceding` | each preceding document | the related invoice |

`$source` is always the whole bundle, and `$scope` the rule's scope. Rules run by scope in the order above, and in mapping order within a scope, so document rules see the result of all the others and are the only ones that should add or remove elements. Custom fields referred to by a rule are not reported as unmapped.

Rules cannot access the environment or files, and each run on an element is limited to one second by default (`WithRuleTimeout`). As jq numbers are floating point, rules should move or look up amounts, and leave calculations to GOBL.

### Examples

`examples/netsuite` contains bundles taken from a test account, with links removed to anonymise the account. The expected GOBL output for each is in `examples/netsuite/out`, and is regenerated with:

```bash
go test -run TestExamples -update
```

## NetSuite Setup

The client uses the [REST web services](https://docs.oracle.com/en/cloud/saas/netsuite/ns-online-help/chapter_1540391670.html) hosted at `https://<account>.suitetalk.api.netsuite.com`. Two authentication methods are supported:

- **OAuth 2.0 client credentials (M2M)**, `client.M2M`: the recommended method, and the only one NetSuite allows for new integrations from 2027.1.
- **Token-Based Authentication (TBA)**, `client.TBA`: for existing integrations, until NetSuite ends support, planned for 2028.2.

### OAuth 2.0 client credentials (M2M)

Access tokens are requested with a JWT signed by a private key, whose certificate is uploaded to NetSuite. There are no refresh tokens or user sessions: tokens can be requested for as long as the certificate is valid, up to two years.

1. Enable **Setup > Company > Enable Features > SuiteCloud**: _REST Web Services_ and _OAuth 2.0_.
2. On the **Integration** record (Setup > Integration > Manage Integrations), check _Client Credentials (Machine to Machine) Grant_ and the _REST Web Services_ and _RESTlets_ scopes. Note the client ID.
3. Generate a key and certificate with `go run ./cmd/gobl.netsuite probe cert -o probe/`, which writes `netsuite.key` (keep it secret) and `netsuite.crt`.
4. In **Setup > Integration > Manage Authentication > OAuth 2.0 Client Credentials (M2M) Setup**, create a mapping for the entity, role and integration, upload `netsuite.crt`, and note the **certificate ID**.

| Variable                    | Where it comes from                 |
| --------------------------- | ----------------------------------- |
| `NETSUITE_ACCOUNT_ID`       | Setup > Company > Company Information |
| `NETSUITE_CLIENT_ID`        | Integration record (step 2)         |
| `NETSUITE_CERTIFICATE_ID`   | M2M mapping (step 4)                |
| `NETSUITE_PRIVATE_KEY_FILE` | Path to `netsuite.key` (step 3)     |

The probe uses M2M when `NETSUITE_CERTIFICATE_ID` is set.

### Token-Based Authentication (TBA)

Five values are needed:

| Variable                   | Where it comes from                                                    |
| -------------------------- | ---------------------------------------------------------------------- |
| `NETSUITE_ACCOUNT_ID`      | Setup > Company > Company Information, e.g. `1234567` or `1234567_SB1` |
| `NETSUITE_CONSUMER_KEY`    | Integration record (step 2)                                            |
| `NETSUITE_CONSUMER_SECRET` | Integration record (step 2)                                            |
| `NETSUITE_TOKEN_ID`        | Access token (step 3)                                                  |
| `NETSUITE_TOKEN_SECRET`    | Access token (step 3)                                                  |

The secrets are only displayed once, when the record is saved. If one is lost, use _Reset Credentials_ on the integration, or revoke the token and create a new one.

#### 1. Enable features

**Setup > Company > Enable Features > SuiteCloud**, then check:

- _SuiteTalk (Web Services)_: **REST Web Services**
- _Manage Authentication_: **Token-Based Authentication**

#### 2. Create an integration record

**Setup > Integration > Manage Integrations > New**:

- Name it (e.g. "Invopop") and set the state to _Enabled_.
- On the _Authentication_ tab, check **Token-Based Authentication**. _TBA: Authorization Flow_ and _Authorization Code Grant_ are not required.
- Save, and copy the **Consumer Key** and **Consumer Secret** shown at the bottom of the page.

#### 3. Create an access token

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

> Token-Based Authentication (TBA) can't be used by new integrations from NetSuite 2027.1, and support is planned to end in 2028.2. Use OAuth 2.0 client credentials for new integrations.

## Command Line

Copy `.env.example` to `.env` and fill in the credentials, then:

```bash
# List recently modified invoices
go run ./cmd/gobl.netsuite probe invoices

# Fetch an invoice or credit memo bundle, or convert it directly into GOBL
go run ./cmd/gobl.netsuite probe invoice 1234 -o probe/invoice_1234.json
go run ./cmd/gobl.netsuite probe creditmemo 1235 --convert

# Create a new example fixture
go run ./cmd/gobl.netsuite probe invoice 1234 --strip-links -o examples/netsuite/invoice_new.json

# Create or update test data, printing the internal ID of new records
go run ./cmd/gobl.netsuite probe create invoice invoice.json
go run ./cmd/gobl.netsuite probe transform invoice 1234 creditmemo credit.json
go run ./cmd/gobl.netsuite probe update creditmemo 1235 --replace item lines.json

# Convert a bundle file into a GOBL envelope, optionally with mappings
go run ./cmd/gobl.netsuite convert examples/netsuite/invoice_basic.json
go run ./cmd/gobl.netsuite convert examples/netsuite/invoice_basic.json -m account.json --no-presets

# List the mapping presets, or show one
go run ./cmd/gobl.netsuite presets
go run ./cmd/gobl.netsuite presets default

# Run any SuiteQL query
go run ./cmd/gobl.netsuite probe query "SELECT id, name FROM subsidiary"

# Fetch the account specific JSON Schema for a record type
go run ./cmd/gobl.netsuite probe schema invoice

# GET any path under /services/rest, e.g. a tax code or currency
go run ./cmd/gobl.netsuite probe get record/v1/salestaxitem/6
```
