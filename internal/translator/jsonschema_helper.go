// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"google.golang.org/genai"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

// Constants for safety limits
const (
	jsonSchemaMaxRecursionDepth = 100
)

// Errors for safety violations
var (
	errJSONSchemaMaxRecursionDepthExceeded = fmt.Errorf("maximum recursion depth exceeded")
	errInvalidJSONSchema                   = fmt.Errorf("invalid JSON schema")
)

// jsonSchemaDeepCopyMapStringAny creates a safe deep copy of a map[string]any.
func jsonSchemaDeepCopyMapStringAny(original map[string]any) (map[string]any, error) {
	if original == nil {
		return nil, nil
	}
	return jsonSchemaDeepCopyMapStringAnySafe(original, 0)
}

// jsonSchemaDeepCopyMapStringAnySafe performs the actual deep copy with recursion depth checks.
func jsonSchemaDeepCopyMapStringAnySafe(original map[string]any, depth int) (map[string]any, error) {
	if original == nil {
		return nil, nil
	}

	if depth >= jsonSchemaMaxRecursionDepth {
		return nil, fmt.Errorf("%w: depth %d", errJSONSchemaMaxRecursionDepthExceeded, depth)
	}

	copied := make(map[string]any, len(original))
	for key, value := range original {
		copiedValue, err := jsonSchemaDeepCopyAnySafe(value, depth+1)
		if err != nil {
			return nil, err
		}
		copied[key] = copiedValue
	}
	return copied, nil
}

// jsonSchemaDeepCopyAny performs a safe deep copy of any value.
func jsonSchemaDeepCopyAny(value any) (any, error) {
	return jsonSchemaDeepCopyAnySafe(value, 0)
}

// jsonSchemaDeepCopyAnySafe performs the actual deep copy with recursion depth checks.
func jsonSchemaDeepCopyAnySafe(value any, depth int) (any, error) {
	if depth >= jsonSchemaMaxRecursionDepth {
		return nil, fmt.Errorf("%w: depth %d", errJSONSchemaMaxRecursionDepthExceeded, depth)
	}

	switch v := value.(type) {
	case map[string]any:
		return jsonSchemaDeepCopyMapStringAnySafe(v, depth)
	case []any:
		copiedSlice := make([]any, len(v))
		for i, elem := range v {
			copiedElem, err := jsonSchemaDeepCopyAnySafe(elem, depth+1)
			if err != nil {
				return nil, err
			}
			copiedSlice[i] = copiedElem
		}
		return copiedSlice, nil
	default:
		// For primitive types (int, bool, string, etc.) and other value types,
		// direct assignment performs a copy.
		return value, nil
	}
}

// jsonSchemaRetrieveRef safely fetches a deeply-nested reference from a schema map.
// It includes input validation and protection against path traversal attacks.
func jsonSchemaRetrieveRef(path string, schema map[string]any) (any, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: ref path cannot be empty", errInvalidJSONSchema)
	}

	if schema == nil {
		return nil, fmt.Errorf("%w: schema cannot be nil", errInvalidJSONSchema)
	}

	// Validate path format and prevent path traversal
	if !strings.HasPrefix(path, "#/") {
		return nil, fmt.Errorf("%w: ref paths must start with '#/', got: %s", errInvalidJSONSchema, path)
	}

	// Split and validate path components
	components := strings.Split(path, "/")
	if len(components) < 2 {
		return nil, fmt.Errorf("%w: invalid ref path format: %s", errInvalidJSONSchema, path)
	}

	// Remove the "#" prefix
	components = components[1:]

	current := schema
	for i, component := range components {
		if component == "" {
			return nil, fmt.Errorf("%w: ref path contains empty component at position %d", errInvalidJSONSchema, i+1)
		}

		// Prevent directory traversal attempts
		if strings.Contains(component, "..") || strings.Contains(component, "./") {
			return nil, fmt.Errorf("%w: ref path contains invalid characters: %s", errInvalidJSONSchema, component)
		}

		// Type assertion to ensure `current` is a map
		val, exists := current[component]
		if !exists {
			return nil, fmt.Errorf("%w: reference '%s' not found: component '%s' does not exist", errInvalidJSONSchema, path, component)
		}

		// Check if we're at the last component
		if i == len(components)-1 {
			// Create and return a deep copy to prevent mutation of the original schema
			deepCopy, err := jsonSchemaDeepCopyAny(val)
			if err != nil {
				return nil, fmt.Errorf("failed to create deep copy: %w", err)
			}
			return deepCopy, nil
		}

		// For intermediate components, ensure they are maps
		nextMap, ok := val.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: reference '%s' invalid: intermediate component '%s' is not a map (got %T)", errInvalidJSONSchema, path, component, val)
		}
		current = nextMap
	}

	// This should never be reached due to the loop structure above
	return nil, fmt.Errorf("%w: unexpected end of ref path traversal for: %s", errInvalidJSONSchema, path)
}

// jsonSchemaDereferenceHelper recursively dereferences JSON schema references.
func jsonSchemaDereferenceHelper(
	obj any,
	fullSchema map[string]any,
	skipKeys []string,
	processedRefs map[string]struct{},
	depth int,
) (any, error) {
	// Check recursion depth
	if depth >= jsonSchemaMaxRecursionDepth {
		return nil, fmt.Errorf("%w: depth %d", errJSONSchemaMaxRecursionDepthExceeded, depth)
	}

	// Handle dictionaries (maps)
	if dict, ok := obj.(map[string]any); ok {
		objOut := make(map[string]any)

		for k, v := range dict {
			// Check if key should be skipped
			shouldSkip := slices.Contains(skipKeys, k)
			if shouldSkip {
				objOut[k] = v
				continue
			}

			// Handle reference key "$ref"
			if k == "$ref" {
				refPath, isString := v.(string)
				if !isString {
					return nil, fmt.Errorf("%w: '$ref' value must be a string, got %T", errInvalidJSONSchema, v)
				}

				// Check for circular references
				if _, exists := processedRefs[refPath]; exists {
					return nil, fmt.Errorf("%w: circular reference detected: %s", errInvalidJSONSchema, refPath)
				}

				// Mark as being processed
				processedRefs[refPath] = struct{}{}

				ref, err := jsonSchemaRetrieveRef(refPath, fullSchema)
				if err != nil {
					delete(processedRefs, refPath) // Clean up on error
					return nil, fmt.Errorf("failed to retrieve reference %s: %w", refPath, err)
				}

				fullRef, err := jsonSchemaDereferenceHelper(ref, fullSchema, skipKeys, processedRefs, depth+1)
				if err != nil {
					delete(processedRefs, refPath) // Clean up on error
					return nil, fmt.Errorf("failed to dereference %s: %w", refPath, err)
				}

				// Remove from processed set after successful resolution
				delete(processedRefs, refPath)
				return fullRef, nil
			}

			// Recurse on nested structures
			if _, isDict := v.(map[string]any); isDict {
				res, err := jsonSchemaDereferenceHelper(v, fullSchema, skipKeys, processedRefs, depth+1)
				if err != nil {
					return nil, err
				}
				objOut[k] = res
			} else if _, isList := v.([]any); isList {
				res, err := jsonSchemaDereferenceHelper(v, fullSchema, skipKeys, processedRefs, depth+1)
				if err != nil {
					return nil, err
				}
				objOut[k] = res
			} else {
				objOut[k] = v
			}
		}
		return objOut, nil
	}

	// Handle lists (slices)
	if list, ok := obj.([]any); ok {
		listOut := make([]any, len(list))
		for i, el := range list {
			res, err := jsonSchemaDereferenceHelper(el, fullSchema, skipKeys, processedRefs, depth+1)
			if err != nil {
				return nil, err
			}
			listOut[i] = res
		}
		return listOut, nil
	}

	// Return non-dictionary and non-list types as-is
	return obj, nil
}

// jsonSchemaSkipKeys recursively traverses a schema to find keys that should be skipped.
func jsonSchemaSkipKeys(
	obj any,
	fullSchema map[string]any,
	processedRefs map[string]struct{},
	depth int,
) ([]string, error) {
	// Check recursion depth
	if depth >= jsonSchemaMaxRecursionDepth {
		return nil, fmt.Errorf("%w: depth %d", errJSONSchemaMaxRecursionDepthExceeded, depth)
	}

	var keys []string

	// Handle dictionaries (maps)
	if dict, ok := obj.(map[string]any); ok {
		for k, v := range dict {
			if k == "$ref" {
				refPath, isString := v.(string)
				if !isString {
					return nil, fmt.Errorf("%w: '$ref' value must be a string, got %T", errInvalidJSONSchema, v)
				}

				// Skip if reference has already been processed (circular reference protection)
				if _, exists := processedRefs[refPath]; exists {
					return nil, fmt.Errorf("%w: circular reference detected: %s", errInvalidJSONSchema, refPath)
				}
				processedRefs[refPath] = struct{}{}

				ref, err := jsonSchemaRetrieveRef(refPath, fullSchema)
				if err != nil {
					delete(processedRefs, refPath) // Clean up on error
					return nil, err
				}

				// Add the top-level key of the reference to the list
				// This relies on the reference path format, e.g., "#/components/..."
				components := strings.Split(refPath, "/")
				if len(components) > 1 {
					keys = append(keys, components[1])
				}

				// Recurse on the referenced schema
				nestedKeys, err := jsonSchemaSkipKeys(ref, fullSchema, processedRefs, depth+1)
				if err != nil {
					delete(processedRefs, refPath) // Clean up on error
					return nil, err
				}
				keys = append(keys, nestedKeys...)

				// Clean up after processing
				delete(processedRefs, refPath)
			} else if _, isDict := v.(map[string]any); isDict {
				nestedKeys, err := jsonSchemaSkipKeys(v, fullSchema, processedRefs, depth+1)
				if err != nil {
					return nil, err
				}
				keys = append(keys, nestedKeys...)
			} else if _, isList := v.([]any); isList {
				nestedKeys, err := jsonSchemaSkipKeys(v, fullSchema, processedRefs, depth+1)
				if err != nil {
					return nil, err
				}
				keys = append(keys, nestedKeys...)
			}
		}
	} else if list, ok := obj.([]any); ok {
		// Handle lists (slices)
		for _, el := range list {
			nestedKeys, err := jsonSchemaSkipKeys(el, fullSchema, processedRefs, depth+1)
			if err != nil {
				return nil, err
			}
			keys = append(keys, nestedKeys...)
		}
	}

	return keys, nil
}

// jsonSchemaDereference substitutes $refs in a JSON Schema object.
func jsonSchemaDereference(schemaObj map[string]any) (any, error) {
	if schemaObj == nil {
		return nil, fmt.Errorf("%w: schema object cannot be nil", errInvalidJSONSchema)
	}

	processedRefs := make(map[string]struct{})
	skipKeys, err := jsonSchemaSkipKeys(schemaObj, schemaObj, processedRefs, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to determine skip keys: %w", err)
	}

	// Reset processed refs for the actual dereferencing
	processedRefs = make(map[string]struct{})

	// Call the recursive helper function to perform the dereferencing
	return jsonSchemaDereferenceHelper(schemaObj, schemaObj, skipKeys, processedRefs, 0)
}

// jsonSchemaToGapic formats a JSON schema for a gapic request with improved safety.
// This is modified from the langchain-google implementation with enhanced error handling.
func jsonSchemaToGapic(schema map[string]any, allowedSchemaFieldsSet map[string]struct{}) (map[string]any, error) {
	if schema == nil {
		return nil, fmt.Errorf("%w: schema cannot be nil", errInvalidJSONSchema)
	}
	if allowedSchemaFieldsSet == nil {
		return nil, fmt.Errorf("%w: allowedSchemaFieldsSet cannot be nil", errInvalidJSONSchema)
	}

	convertedSchema := make(map[string]any)

	for key, value := range schema {
		switch key {
		case "$defs":
			// Skip $defs as they are handled separately
			continue

		case "items":
			subSchema, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%w: 'items' must be a dict, got %T", errInvalidJSONSchema, value)
			}
			convertedItems, err := jsonSchemaToGapic(subSchema, allowedSchemaFieldsSet)
			if err != nil {
				return nil, fmt.Errorf("failed to convert items schema: %w", err)
			}
			convertedSchema["items"] = convertedItems

		case "properties":
			properties, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%w: 'properties' must be a dict, got %T", errInvalidJSONSchema, value)
			}

			convertedProperties := make(map[string]any)
			for pkey, pvalue := range properties {
				pSubSchema, ok := pvalue.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%w: property '%s' must be a dict, got %T", errInvalidJSONSchema, pkey, pvalue)
				}

				convertedProperty, err := jsonSchemaToGapic(pSubSchema, allowedSchemaFieldsSet)
				if err != nil {
					return nil, fmt.Errorf("failed to convert property '%s': %w", pkey, err)
				}
				convertedProperties[pkey] = convertedProperty
			}
			convertedSchema["properties"] = convertedProperties

		case "type":
			convertedType, err := processJSONSchemaTypeField(value)
			if err != nil {
				return nil, err
			}
			// Merge the converted type result into the schema
			maps.Copy(convertedSchema, convertedType)

		case "allOf":
			convertedAllOf, err := processJSONSchemaAllOfField(value, allowedSchemaFieldsSet)
			if err != nil {
				return nil, err
			}
			return convertedAllOf, nil

		case "anyOf":
			convertedAnyOf, err := processJSONSchemaAnyOfField(value, allowedSchemaFieldsSet)
			if err != nil {
				return nil, err
			}
			// Merge the anyOf result
			maps.Copy(convertedSchema, convertedAnyOf)

		default:
			// Check if the key is in the allowed set
			if _, allowed := allowedSchemaFieldsSet[key]; allowed {
				convertedSchema[key] = value
			}
		}
	}
	return convertedSchema, nil
}

// processJSONSchemaTypeField handles the "type" field conversion with improved type safety
func processJSONSchemaTypeField(value any) (map[string]any, error) {
	result := make(map[string]any)

	switch typeValue := value.(type) {
	case []any:
		if len(typeValue) != 2 {
			return nil, fmt.Errorf("%w: if type is a list, length must be 2, got %d", errInvalidJSONSchema, len(typeValue))
		}

		hasNull := false
		var nonNullType any
		for _, t := range typeValue {
			if t == "null" {
				hasNull = true
			} else {
				nonNullType = t
			}
		}

		if !hasNull || nonNullType == nil {
			return nil, fmt.Errorf("%w: if type is a list, it must contain one non-null type and 'null'", errInvalidJSONSchema)
		}

		// Non-null types in JSON Schema should be strings, not maps
		if _, ok := nonNullType.(map[string]any); ok {
			return nil, fmt.Errorf("%w: unexpected map type in type array", errInvalidJSONSchema)
		}

		result["type"] = fmt.Sprintf("%v", nonNullType)
		result["nullable"] = true

	case string:
		result["type"] = typeValue

	default:
		return nil, fmt.Errorf("%w: 'type' must be a list or string, got %T", errInvalidJSONSchema, value)
	}

	return result, nil
}

// processJSONSchemaAllOfField handles the "allOf" field conversion
func processJSONSchemaAllOfField(value any, allowedSchemaFieldsSet map[string]struct{}) (map[string]any, error) {
	allOfList, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: 'allOf' must be a list, got %T", errInvalidJSONSchema, value)
	}

	if len(allOfList) == 0 {
		return nil, fmt.Errorf("%w: 'allOf' cannot be empty", errInvalidJSONSchema)
	}

	if len(allOfList) > 1 {
		return nil, fmt.Errorf("%w: only one value for 'allOf' key is supported, got %d", errInvalidJSONSchema, len(allOfList))
	}

	subSchema, ok := allOfList[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: item in 'allOf' must be an object, got %T", errInvalidJSONSchema, allOfList[0])
	}

	return jsonSchemaToGapic(subSchema, allowedSchemaFieldsSet)
}

// processJSONSchemaAnyOfField handles the "anyOf" field conversion
func processJSONSchemaAnyOfField(value any, allowedSchemaFieldsSet map[string]struct{}) (map[string]any, error) {
	anyOfList, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: 'anyOf' must be a list, got %T", errInvalidJSONSchema, value)
	}

	if len(anyOfList) == 0 {
		return nil, fmt.Errorf("%w: 'anyOf' cannot be empty", errInvalidJSONSchema)
	}

	result := make(map[string]any)
	anyOfResults := make([]any, 0)
	nullable := false

	for i, v := range anyOfList {
		subSchema, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: item %d in 'anyOf' must be a dict, got %T", errInvalidJSONSchema, i, v)
		}

		if t, exists := subSchema["type"]; exists && t == "null" {
			nullable = true
		} else {
			convertedSubSchema, err := jsonSchemaToGapic(subSchema, allowedSchemaFieldsSet)
			if err != nil {
				return nil, fmt.Errorf("failed to convert anyOf item %d: %w", i, err)
			}
			anyOfResults = append(anyOfResults, convertedSubSchema)
		}
	}

	if nullable {
		result["nullable"] = true
	}
	result["anyOf"] = anyOfResults

	return result, nil
}

// jsonSchemaMapToSchema safely converts a map[string]any to a genai.Schema struct.
// This function includes proper input validation and error handling.
func jsonSchemaMapToSchema(schemaMap map[string]any) (*genai.Schema, error) {
	if schemaMap == nil {
		return nil, fmt.Errorf("%w: schemaMap cannot be nil", errInvalidJSONSchema)
	}

	// Marshal the map to JSON
	jsonBytes, err := json.Marshal(schemaMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal map to JSON: %w", err)
	}

	// Unmarshal into the genai.Schema struct
	var genSchema genai.Schema
	if err := json.Unmarshal(jsonBytes, &genSchema); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON to Schema: %w", err)
	}

	return &genSchema, nil
}

// jsonSchemaToGemini converts a JSON schema to a Gemini Schema with comprehensive validation.
// This function combines dereferencing and GCP formatting with proper error handling.
func jsonSchemaToGemini(schema map[string]any) (*genai.Schema, error) {
	if schema == nil {
		return nil, fmt.Errorf("%w: schema cannot be nil", errInvalidJSONSchema)
	}

	// Step 1: Dereference the schema to resolve all $ref pointers
	dereferencedSchema, err := jsonSchemaDereference(schema)
	if err != nil {
		return nil, fmt.Errorf("failed to dereference schema: %w", err)
	}

	// Ensure the dereferenced result is a map
	dereferencedMap, ok := dereferencedSchema.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: dereferenced schema is not a map[string]any, got %T", errInvalidJSONSchema, dereferencedSchema)
	}

	// Step 2: Define allowed schema fields for genai.Schema
	// This list corresponds to the fields supported by the genai.Schema struct
	allowedSchemaFieldsSet := map[string]struct{}{
		"anyOf":            {},
		"default":          {},
		"description":      {},
		"enum":             {},
		"example":          {},
		"format":           {},
		"items":            {},
		"maxItems":         {},
		"maxLength":        {},
		"maxProperties":    {},
		"maximum":          {},
		"minItems":         {},
		"minLength":        {},
		"minProperties":    {},
		"minimum":          {},
		"nullable":         {},
		"pattern":          {},
		"properties":       {},
		"propertyOrdering": {},
		"required":         {},
		"title":            {},
		"type":             {},
	}

	// Step 3: Convert to GCP/Gapic compatible format
	schemaMap, err := jsonSchemaToGapic(dereferencedMap, allowedSchemaFieldsSet)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to Gapic format: %w", err)
	}

	// Step 4: Convert to genai.Schema struct
	retSchema, err := jsonSchemaMapToSchema(schemaMap)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to genai.Schema: %w", err)
	}

	return retSchema, nil
}
