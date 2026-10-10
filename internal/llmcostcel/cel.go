// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package llmcostcel provides functions to create and evaluate CEL programs to calculate costs.
//
// This exists as a separate package to be used both in the controller to validate the expression
// and in the external processor to evaluate the expression.
package llmcostcel

import (
	"fmt"

	"github.com/google/cel-go/cel"
)

const (
	celModelNameKey                = "model"
	celBackendKey                  = "backend"
	celRouteNameKey                = "route_name"
	celInputTokensKey              = "input_tokens"
	celCachedInputTokensKey        = "cached_input_tokens"         // #nosec G101
	celCacheCreationInputTokensKey = "cache_creation_input_tokens" // #nosec G101
	// Breakdown of cache_creation_input_tokens by cache TTL. Providers price
	// the two differently -- Anthropic charges 2x the base input rate for a
	// 1 hour cache write against 1.25x for 5 minutes -- so an expression that
	// sees only the combined figure cannot price a cached request correctly.
	// Both are 0 on backends that do not report the breakdown.
	celCacheCreation5mInputTokensKey = "cache_creation_5m_input_tokens" // #nosec G101
	celCacheCreation1hInputTokensKey = "cache_creation_1h_input_tokens" // #nosec G101
	celOutputTokensKey               = "output_tokens"
	celTotalTokensKey                = "total_tokens"
	celReasoningTokensKey            = "reasoning_tokens"
)

var env *cel.Env

func init() {
	var err error
	env, err = cel.NewEnv(
		cel.Variable(celModelNameKey, cel.StringType),
		cel.Variable(celBackendKey, cel.StringType),
		cel.Variable(celRouteNameKey, cel.StringType),
		cel.Variable(celInputTokensKey, cel.UintType),
		cel.Variable(celCachedInputTokensKey, cel.UintType),
		cel.Variable(celCacheCreationInputTokensKey, cel.UintType),
		cel.Variable(celCacheCreation5mInputTokensKey, cel.UintType),
		cel.Variable(celCacheCreation1hInputTokensKey, cel.UintType),
		cel.Variable(celOutputTokensKey, cel.UintType),
		cel.Variable(celTotalTokensKey, cel.UintType),
		cel.Variable(celReasoningTokensKey, cel.UintType),
	)
	if err != nil {
		panic(fmt.Sprintf("cannot create CEL environment: %v", err))
	}
}

// NewProgram creates a new CEL program from the given expression.
func NewProgram(expr string) (prog cel.Program, err error) {
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		err = issues.Err()
		return nil, fmt.Errorf("cannot compile CEL expression: %w", err)
	}
	prog, err = env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("cannot create CEL program: %w", err)
	}

	// Sanity check by evaluating the expression with some dummy values.
	_, err = EvaluateProgram(prog, "dummy", "dummy", "dummy", 0, 0, 0, 0, 0, 0, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate CEL expression: %w", err)
	}
	return prog, nil
}

// EvaluateProgram evaluates the given CEL program with the given variables.
func EvaluateProgram(prog cel.Program, modelName, backend, routeName string, inputTokens, cachedInputTokens, cacheCreationInputTokens, cacheCreation5mInputTokens, cacheCreation1hInputTokens, outputTokens, totalTokens, reasoningTokens uint32) (uint64, error) {
	out, _, err := prog.Eval(map[string]any{
		celModelNameKey:                  modelName,
		celBackendKey:                    backend,
		celRouteNameKey:                  routeName,
		celInputTokensKey:                inputTokens,
		celCachedInputTokensKey:          cachedInputTokens,
		celCacheCreationInputTokensKey:   cacheCreationInputTokens,
		celCacheCreation5mInputTokensKey: cacheCreation5mInputTokens,
		celCacheCreation1hInputTokensKey: cacheCreation1hInputTokens,
		celOutputTokensKey:               outputTokens,
		celTotalTokensKey:                totalTokens,
		celReasoningTokensKey:            reasoningTokens,
	})
	if err != nil || out == nil {
		return 0, fmt.Errorf("failed to evaluate CEL expression: %w", err)
	}

	switch out.Type() {
	case cel.IntType:
		result := out.Value().(int64)
		if result < 0 {
			return 0, fmt.Errorf("CEL expression result is negative (%d)", result)
		}
		return uint64(result), nil
	case cel.UintType:
		return out.Value().(uint64), nil
	default:
		return 0, fmt.Errorf("CEL expression result is not an integer, got %v", out.Type())
	}
}
