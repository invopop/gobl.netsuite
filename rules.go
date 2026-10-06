package netsuite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/itchyny/gojq"
)

// defaultRuleTimeout bounds how long a single rule may run on one element.
// jq programs cannot be limited in memory, so the timeout is the main
// defence against runaway programs, along with the size of the input.
const defaultRuleTimeout = time.Second

// element is a part of the GOBL document along with the NetSuite data it was
// converted from, recorded during conversion for rules to use.
type element struct {
	scope  Scope
	target any // pointer to the GOBL struct
	src    json.RawMessage
}

func (r *Result) track(scope Scope, target any, src json.RawMessage) {
	r.elements = append(r.elements, &element{scope: scope, target: target, src: src})
}

// applyRules runs the mapping's rules, scope by scope, with document rules
// last as they may change the structure of the document.
func (r *Result) applyRules(timeout time.Duration) error {
	if r.Mapping == nil || len(r.Mapping.Rules) == 0 {
		return nil
	}
	source, err := toValue(r.Source)
	if err != nil {
		return fmt.Errorf("preparing source: %w", err)
	}
	for _, scope := range scopes {
		for _, rule := range r.Mapping.Rules {
			if rule.scope() != scope || rule.direction() != RuleReceive {
				continue
			}
			code, err := compileRule(rule)
			if err != nil {
				return fmt.Errorf("rule %q: %w", rule.ID, err)
			}
			if scope == ScopeDocument {
				if err := applyRule(code, r.Invoice, source, nil, scope, timeout); err != nil {
					return fmt.Errorf("rule %q: %w", rule.ID, err)
				}
				continue
			}
			n := 0
			for _, el := range r.elements {
				if el.scope != scope {
					continue
				}
				var src any
				if len(el.src) > 0 {
					if err := json.Unmarshal(el.src, &src); err != nil {
						return fmt.Errorf("rule %q: preparing source: %w", rule.ID, err)
					}
				}
				if err := applyRule(code, el.target, source, src, scope, timeout); err != nil {
					return fmt.Errorf("rule %q: %s %d: %w", rule.ID, scope, n, err)
				}
				n++
			}
		}
	}
	return nil
}

// sendElement is part of a NetSuite record prepared from a GOBL document,
// with the GOBL element it was prepared from, for send rules.
type sendElement struct {
	scope  Scope
	target *map[string]any
	src    any
}

// applySendRules runs a mapping's send rules on the record being prepared,
// scope by scope, with document rules last. The external ID can't be
// changed, as it's how a record is found again when a job is retried.
func applySendRules(m *Mapping, body *map[string]any, source any, elements []*sendElement) error {
	if m == nil || len(m.Rules) == 0 {
		return nil
	}
	src, err := toValue(source)
	if err != nil {
		return fmt.Errorf("preparing source: %w", err)
	}
	externalID := (*body)["externalId"]
	for _, scope := range sendScopes {
		for _, rule := range m.Rules {
			if rule.Disabled || rule.direction() != RuleSend || rule.scope() != scope {
				continue
			}
			code, err := compileRule(rule)
			if err != nil {
				return fmt.Errorf("rule %q: %w", rule.ID, err)
			}
			n := 0
			for _, el := range elements {
				if el.scope != scope {
					continue
				}
				elSrc, err := toValue(el.src)
				if err != nil {
					return fmt.Errorf("rule %q: preparing source: %w", rule.ID, err)
				}
				if err := applyRule(code, el.target, src, elSrc, scope, defaultRuleTimeout); err != nil {
					return fmt.Errorf("rule %q: %s %d: %w", rule.ID, scope, n, err)
				}
				n++
			}
		}
	}
	if (*body)["externalId"] != externalID {
		return errors.New("rules can't change the externalId")
	}
	return nil
}

// applyRule runs the program on the JSON form of the target, and replaces
// the target with the single object it outputs.
func applyRule(code *gojq.Code, target, source, src any, scope Scope, timeout time.Duration) error {
	in, err := toValue(target)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	iter := code.RunWithContext(ctx, in, source, src, string(scope))
	out, ok := iter.Next()
	if !ok {
		return errors.New("no output")
	}
	if err, ok := out.(error); ok {
		return err
	}
	if _, ok := iter.Next(); ok {
		return errors.New("more than one output")
	}
	if _, ok := out.(map[string]any); !ok {
		return fmt.Errorf("output must be an object, got %s", jqType(out))
	}

	data, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("encoding output: %w", err)
	}
	v := reflect.New(reflect.TypeOf(target).Elem())
	if err := json.Unmarshal(data, v.Interface()); err != nil {
		return fmt.Errorf("decoding output: %w", err)
	}
	reflect.ValueOf(target).Elem().Set(v.Elem())
	return nil
}

// toValue converts a value into the plain JSON types used by jq.
func toValue(v any) (any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func jqType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	default:
		return "number"
	}
}
