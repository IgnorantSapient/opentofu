// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0
// Copyright (c) 2023 HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package objchange

import (
	"github.com/opentofu/opentofu/internal/configs/configschema"
	"github.com/zclconf/go-cty/cty"
)

// NormalizeObjectFromLegacySDK takes an object that may have been generated
// by the legacy Terraform SDK (i.e. returned from a provider with the
// LegacyTypeSystem opt-out set) and does its best to normalize it for the
// assumptions we would normally enforce if the provider had not opted out.
//
// In particular, this function guarantees that a value representing a nested
// block will never itself be unknown or null, instead representing that as
// a non-null value that may contain null/unknown values.
//
// The input value must still conform to the implied type of the given schema,
// or else this function may produce garbage results or panic. This is usually
// okay because type consistency is enforced when deserializing the value
// returned from the provider over the RPC wire protocol anyway.
func NormalizeObjectFromLegacySDK(val cty.Value, schema *configschema.Block) cty.Value {
	val, valMarks := val.UnmarkDeepWithPaths()
	val = normalizeObjectFromLegacySDK(val, schema)
	return val.MarkWithPaths(valMarks)
}

func normalizeObjectFromLegacySDK(val cty.Value, schema *configschema.Block) cty.Value {
	if val == cty.NilVal || val.IsNull() {
		// This should never happen in reasonable use, but we'll allow it
		// and normalize to a null of the expected type rather than panicking
		// below.
		return cty.NullVal(schema.ImpliedType())
	}

	vals := make(map[string]cty.Value)
	for name := range schema.Attributes {
		// No normalization for attributes, since them being type-conformant
		// is all that we require.
		vals[name] = val.GetAttr(name)
	}
	for name, blockS := range schema.BlockTypes {
		lv := val.GetAttr(name)

		// Legacy SDK never generates dynamically-typed attributes and so our
		// normalization code doesn't deal with them, but we need to make sure
		// we still pass them through properly so that we don't interfere with
		// objects generated by other SDKs.
		if ty := blockS.Block.ImpliedType(); ty.HasDynamicTypes() {
			vals[name] = lv
			continue
		}

		switch blockS.Nesting {
		case configschema.NestingSingle, configschema.NestingGroup:
			if lv.IsKnown() {
				if lv.IsNull() && blockS.Nesting == configschema.NestingGroup {
					vals[name] = blockS.EmptyValue()
				} else {
					vals[name] = normalizeObjectFromLegacySDK(lv, &blockS.Block)
				}
			} else {
				vals[name] = unknownBlockStub(&blockS.Block)
			}
		case configschema.NestingList:
			switch {
			case !lv.IsKnown():
				vals[name] = cty.ListVal([]cty.Value{unknownBlockStub(&blockS.Block)})
			case lv.IsNull() || lv.LengthInt() == 0:
				vals[name] = cty.ListValEmpty(blockS.Block.ImpliedType())
			default:
				subVals := make([]cty.Value, 0, lv.LengthInt())
				for it := lv.ElementIterator(); it.Next(); {
					_, subVal := it.Element()
					subVals = append(subVals, normalizeObjectFromLegacySDK(subVal, &blockS.Block))
				}
				vals[name] = cty.ListVal(subVals)
			}
		case configschema.NestingSet:
			switch {
			case !lv.IsKnown():
				vals[name] = cty.SetVal([]cty.Value{unknownBlockStub(&blockS.Block)})
			case lv.IsNull() || lv.LengthInt() == 0:
				vals[name] = cty.SetValEmpty(blockS.Block.ImpliedType())
			default:
				subVals := make([]cty.Value, 0, lv.LengthInt())
				for it := lv.ElementIterator(); it.Next(); {
					_, subVal := it.Element()
					subVals = append(subVals, normalizeObjectFromLegacySDK(subVal, &blockS.Block))
				}
				vals[name] = cty.SetVal(subVals)
			}
		default:
			// The legacy SDK doesn't support NestingMap, so we just assume
			// maps are always okay. (If not, we would've detected and returned
			// an error to the user before we got here.)
			vals[name] = lv
		}
	}
	return cty.ObjectVal(vals)
}

// unknownBlockStub constructs an object value that approximates an unknown
// block by producing a known block object with all of its leaf attribute
// values set to unknown.
//
// Blocks themselves cannot be unknown, so if the legacy SDK tries to return
// such a thing, we'll use this result instead. This convention mimics how
// the dynamic block feature deals with being asked to iterate over an unknown
// value, because our value-checking functions already accept this convention
// as a special case.
func unknownBlockStub(schema *configschema.Block) cty.Value {
	vals := make(map[string]cty.Value)
	for name, attrS := range schema.Attributes {
		vals[name] = cty.UnknownVal(attrS.Type)
	}
	for name, blockS := range schema.BlockTypes {
		switch blockS.Nesting {
		case configschema.NestingSingle, configschema.NestingGroup:
			vals[name] = unknownBlockStub(&blockS.Block)
		case configschema.NestingList:
			// In principle we may be expected to produce a tuple value here,
			// if there are any dynamically-typed attributes in our nested block,
			// but the legacy SDK doesn't support that, so we just assume it'll
			// never be necessary to normalize those. (Incorrect usage in any
			// other SDK would be caught and returned as an error before we
			// get here.)
			vals[name] = cty.ListVal([]cty.Value{unknownBlockStub(&blockS.Block)})
		case configschema.NestingSet:
			vals[name] = cty.SetVal([]cty.Value{unknownBlockStub(&blockS.Block)})
		case configschema.NestingMap:
			// A nesting map can never be unknown since we then wouldn't know
			// what the keys are. (Legacy SDK doesn't support NestingMap anyway,
			// so this should never arise.)
			vals[name] = cty.MapValEmpty(blockS.Block.ImpliedType())
		}
	}
	return cty.ObjectVal(vals)
}
