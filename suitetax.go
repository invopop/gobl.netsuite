package netsuite

import (
	"fmt"
)

// useTaxDetails tells whether the transaction's taxes come from its tax
// details, as in accounts using SuiteTax.
func (r *Result) useTaxDetails(engine TaxEngine) (bool, error) {
	details := r.records.transaction.TaxDetails
	has := details != nil && len(details.Items) > 0
	switch engine {
	case TaxEngineLegacy:
		return false, nil
	case TaxEngineSuiteTax:
		if !has && r.records.transaction.Item != nil {
			for _, it := range r.records.transaction.Item.Items {
				if it.TaxCode != nil && it.TaxCode.ID != "" {
					return false, fmt.Errorf("transaction %s has no tax details but its lines have tax codes, so the account doesn't seem to use SuiteTax", r.records.transaction.ID)
				}
			}
		}
		return true, nil
	case TaxEngineAuto:
		return has, nil
	}
	return false, fmt.Errorf("unknown tax engine %q", engine)
}

// applyTaxDetails sets each line's tax code and rate from its tax details,
// so lines are converted as with legacy tax. Lines with more than one tax,
// such as US state and county sales taxes, aren't supported yet.
func (r *Result) applyTaxDetails() error {
	src := r.records.transaction
	if src.Item == nil {
		return nil
	}
	byRef := make(map[string][]*TaxDetail)
	if src.TaxDetails != nil {
		for _, d := range src.TaxDetails.Items {
			if d.TaxDetailsReference != "" {
				byRef[d.TaxDetailsReference] = append(byRef[d.TaxDetailsReference], d)
			}
		}
	}
	for i, it := range src.Item.Items {
		path := fmt.Sprintf("transaction.item.items[%d]", i)
		// Lines keep a tax code of their own, if any, which tax details
		// replace, so SuiteTax lines aren't converted with a stale one.
		it.TaxCode, it.TaxRate1 = nil, ""
		ds := byRef[it.TaxDetailsReference]
		if it.TaxDetailsReference == "" || len(ds) == 0 {
			continue
		}
		if len(ds) > 1 {
			return fmt.Errorf("%s: line has %d taxes in its tax details, and only one per line is supported", path, len(ds))
		}
		if ds[0].TaxCode == nil || ds[0].TaxCode.ID == "" {
			return fmt.Errorf("%s: tax details have no tax code", path)
		}
		it.TaxCode = ds[0].TaxCode
		it.TaxRate1 = ds[0].TaxRate
	}
	return nil
}
