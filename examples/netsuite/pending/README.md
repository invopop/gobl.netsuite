# Pending examples

Bundles taken from NetSuite that the conversion does not handle yet, kept here so they are not part of the `TestExamples` run.

- `creditmemo_basic.json`: a raw credit memo record (not a bundle), as credit memos are not supported yet.
- `invoice_header_discount_mixed.json`: a header discount shared between five tax codes. NetSuite calculates the tax of the lines and of the discount separately, rounding each per rate before subtracting, so its tax total (197.55) is one cent more than the tax of the net base calculated by GOBL (197.54): at 4%, NetSuite has 1.44 - 0.09 = 1.35, where 33.60 × 4% = 1.34.
