package cost

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFuzzyMatching(t *testing.T) {
	// Create a cost tracker with some test pricing data
	ct := NewCostTracker()

	// Add some test pricing data
	ct.SetPricingForModel("openai", "gpt-4", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 0.03, Output: 0.06},
		},
	})
	ct.SetPricingForModel("openai", "gpt-3.5-turbo", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 0.0015, Output: 0.002},
		},
	})
	ct.SetPricingForModel("anthropic", "claude-3-opus", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 0.015, Output: 0.075},
		},
	})

	tests := []struct {
		name           string
		provider       string
		model          string
		expectedMatch  string
		shouldEstimate bool
		expectError    bool
	}{
		{
			name:           "exact match should not be estimate",
			provider:       "openai",
			model:          "gpt-4",
			expectedMatch:  "gpt-4",
			shouldEstimate: false,
			expectError:    false,
		},
		{
			name:           "close match should be estimate",
			provider:       "openai",
			model:          "gpt4", // Close to gpt-4
			expectedMatch:  "gpt-4",
			shouldEstimate: true,
			expectError:    false,
		},
		{
			name:           "another close match",
			provider:       "openai",
			model:          "gpt-3.5-turbo-16k", // Close to gpt-3.5-turbo
			expectedMatch:  "gpt-3.5-turbo",
			shouldEstimate: true,
			expectError:    false,
		},
		{
			name:           "very different model should fail",
			provider:       "openai",
			model:          "completely-different-model",
			expectedMatch:  "",
			shouldEstimate: false,
			expectError:    true,
		},
		{
			name:           "non-existent provider should fail",
			provider:       "nonexistent",
			model:          "gpt-4",
			expectedMatch:  "",
			shouldEstimate: false,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pricing, matchedModel, isEstimate, err := ct.GetPricingForModelWithFuzzyMatch(tt.provider, tt.model, 1000)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedMatch, matchedModel)
			assert.Equal(t, tt.shouldEstimate, isEstimate)
			assert.NotNil(t, pricing)
		})
	}
}

func TestFuzzyMatchingThreshold(t *testing.T) {
	ct := NewCostTracker()

	// Add test pricing data
	ct.SetPricingForModel("test", "exact-match", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 0.01, Output: 0.02},
		},
	})

	// Test that very different strings don't match
	_, _, _, err := ct.GetPricingForModelWithFuzzyMatch("test", "completely-different", 1000)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no close match found")
}

func TestCalculateCostWithFuzzyMatch(t *testing.T) {
	ct := NewCostTracker()

	// Add test pricing data
	ct.SetPricingForModel("openai", "gpt-4", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 0.03, Output: 0.06},
		},
	})

	// Test exact match
	bd, err := ct.CalculateCostWithFuzzyMatch("openai", "gpt-4", 1000, 500, 0, 0)
	assert.NoError(t, err)
	assert.Equal(t, "gpt-4", bd.MatchedModel)
	assert.False(t, bd.IsEstimate)
	assert.Greater(t, bd.TotalCost, 0.0)

	// Test fuzzy match
	bd2, err := ct.CalculateCostWithFuzzyMatch("openai", "gpt4", 1000, 500, 0, 0)
	assert.NoError(t, err)
	assert.Equal(t, "gpt-4", bd2.MatchedModel)
	assert.True(t, bd2.IsEstimate)
	assert.Greater(t, bd2.TotalCost, 0.0)

	// The fuzzy match should produce the same costs as the exact match since it's using the same pricing
	assert.Equal(t, bd.InputCost, bd2.InputCost)
	assert.Equal(t, bd.OutputCost, bd2.OutputCost)
	assert.Equal(t, bd.TotalCost, bd2.TotalCost)
}

// TestAnthropicCachedCostCalculation tests that cached and cache-creation tokens
// are priced correctly using Anthropic's rates (cached = 10% of input, creation = 125% of input).
func TestAnthropicCachedCostCalculation(t *testing.T) {
	ct := NewCostTracker()

	// Claude Sonnet 4 pricing: input=$3.00, output=$15.00, cached=$0.30, creation=$3.75 per 1M tokens
	ct.SetPricingForModel("anthropic", "claude-sonnet-4-20250514", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 3.00, Output: 15.00, CachedInput: 0.30, CacheCreationInput: 3.75},
		},
	})

	// Scenario: 5000 total input, 4500 cached read, 200 cache creation, 300 non-cached, 100 output
	bd, err := ct.CalculateCostWithFuzzyMatch("anthropic", "claude-sonnet-4-20250514", 5000, 100, 4500, 200)
	assert.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-20250514", bd.MatchedModel)
	assert.False(t, bd.IsEstimate)

	// nonCachedInput = 5000 - 4500 - 200 = 300
	// inputCost = 300 / 1M * 3.00 = 0.0009 → ceil(9.0)/10000 = 0.0009
	// cachedInputCost = 4500 / 1M * 0.30 = 0.00135 → ceil(13.5)/10000 = 0.0014
	// cacheCreationInputCost = 200 / 1M * 3.75 = 0.00075 → ceil(7.5)/10000 = 0.0008
	// outputCost = 100 / 1M * 15.00 = 0.0015 → ceil(15.0)/10000 = 0.0015
	assert.Equal(t, 0.0009, bd.InputCost, "non-cached input cost")
	assert.Equal(t, 0.0014, bd.CachedInputCost, "cached input cost")
	assert.Equal(t, 0.0008, bd.CacheCreationInputCost, "cache creation input cost")
	assert.Equal(t, 0.0015, bd.OutputCost, "output cost")

	// totalCost = raw sum 0.0009 + 0.00135 + 0.00075 + 0.0015 = 0.0045 → ceil(45.0)/10000 = 0.0045
	// But rounding is applied to each component first, then to the sum:
	// 0.0009 + 0.0014 + 0.0008 + 0.0015 = 0.0046 → ceil(46.0)/10000 = 0.0046
	assert.Equal(t, 0.0046, bd.TotalCost, "total cost")
}

// TestCachedCostFallbackToInputRate verifies that when cached_input rate is 0
// (not configured), the full input rate is used instead (backward compatible).
func TestCachedCostFallbackToInputRate(t *testing.T) {
	ct := NewCostTracker()

	// Model with no cached pricing configured (CachedInput=0, CacheCreationInput=0)
	ct.SetPricingForModel("openai", "gpt-4-no-cache-pricing", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 30.00, Output: 60.00},
		},
	})

	// 1000 total input, 800 cached, 0 creation, 200 non-cached, 500 output
	bd, err := ct.CalculateCostWithFuzzyMatch("openai", "gpt-4-no-cache-pricing", 1000, 500, 800, 0)
	assert.NoError(t, err)

	// With fallback, cached tokens are charged at the full input rate (30.00)
	// nonCachedInput = 1000 - 800 - 0 = 200
	// inputCost = 200 / 1M * 30.00 = 0.006 → roundUp = 0.006
	// cachedInputCost = 800 / 1M * 30.00 = 0.024 → roundUp = 0.024
	// cacheCreationInputCost = 0 / 1M * 30.00 = 0.0
	// outputCost = 500 / 1M * 60.00 = 0.03 → roundUp = 0.03
	assert.Equal(t, 0.006, bd.InputCost, "non-cached input cost (full rate)")
	assert.Equal(t, 0.024, bd.CachedInputCost, "cached should use full input rate when not configured")
	assert.Equal(t, 0.0, bd.CacheCreationInputCost, "no cache creation tokens")
	assert.Equal(t, 0.03, bd.OutputCost, "output cost")
	assert.Equal(t, 0.06, bd.TotalCost, "total cost")
}

// TestNoCachedTokensZeroCachedCost verifies that when no cached tokens are reported,
// cached cost fields are zero (backward compatible with pre-caching behavior).
func TestNoCachedTokensZeroCachedCost(t *testing.T) {
	ct := NewCostTracker()

	ct.SetPricingForModel("anthropic", "claude-sonnet-4-20250514", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 3.00, Output: 15.00, CachedInput: 0.30, CacheCreationInput: 3.75},
		},
	})

	// No cached tokens at all
	bd, err := ct.CalculateCostWithFuzzyMatch("anthropic", "claude-sonnet-4-20250514", 1000, 500, 0, 0)
	assert.NoError(t, err)

	// All input charged at full rate
	// inputCost = 1000 / 1M * 3.00 = 0.003 → roundUp = 0.003
	// outputCost = 500 / 1M * 15.00 = 0.0075 → roundUp = 0.0075
	assert.Equal(t, 0.003, bd.InputCost)
	assert.Equal(t, 0.0, bd.CachedInputCost, "no cached tokens → zero cached cost")
	assert.Equal(t, 0.0, bd.CacheCreationInputCost, "no cache creation tokens → zero cost")
	assert.Equal(t, 0.0075, bd.OutputCost)
	assert.Equal(t, 0.0105, bd.TotalCost)
}

// TestOpenAICachedCostCalculation tests OpenAI cached pricing (50% of input rate).
func TestOpenAICachedCostCalculation(t *testing.T) {
	ct := NewCostTracker()

	// GPT-4o pricing: input=$2.50, output=$10.00, cached=$1.25 per 1M tokens
	ct.SetPricingForModel("openai", "gpt-4o-2024-08-06", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 2.50, Output: 10.00, CachedInput: 1.25},
		},
	})

	// 10000 total input, 8000 cached, 0 creation, 2000 non-cached, 1000 output
	bd, err := ct.CalculateCostWithFuzzyMatch("openai", "gpt-4o-2024-08-06", 10000, 1000, 8000, 0)
	assert.NoError(t, err)
	assert.False(t, bd.IsEstimate)

	// nonCachedInput = 10000 - 8000 - 0 = 2000
	// inputCost = 2000 / 1M * 2.50 = 0.005 → roundUp = 0.005
	// cachedInputCost = 8000 / 1M * 1.25 = 0.01 → roundUp = 0.01
	// outputCost = 1000 / 1M * 10.00 = 0.01 → roundUp = 0.01
	assert.Equal(t, 0.005, bd.InputCost, "non-cached input cost")
	assert.Equal(t, 0.01, bd.CachedInputCost, "cached input at 50% rate")
	assert.Equal(t, 0.0, bd.CacheCreationInputCost, "OpenAI has no cache creation concept")
	assert.Equal(t, 0.01, bd.OutputCost, "output cost")
	assert.Equal(t, 0.025, bd.TotalCost, "total cost")
}

// TestGeminiCachedCostCalculation tests Gemini cached pricing (~25% of input rate).
func TestGeminiCachedCostCalculation(t *testing.T) {
	ct := NewCostTracker()

	// Gemini 2.0 Flash pricing: input=$0.10, output=$0.40, cached=$0.025 per 1M tokens
	ct.SetPricingForModel("gemini", "gemini-2.0-flash", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 0.10, Output: 0.40, CachedInput: 0.025},
		},
	})

	// 100000 total input, 90000 cached, 10000 non-cached, 5000 output
	bd, err := ct.CalculateCostWithFuzzyMatch("gemini", "gemini-2.0-flash", 100000, 5000, 90000, 0)
	assert.NoError(t, err)

	// nonCachedInput = 100000 - 90000 - 0 = 10000
	// inputCost = 10000 / 1M * 0.10 = 0.001 → roundUp = 0.001
	// cachedInputCost = 90000 / 1M * 0.025 = 0.00225 → roundUp = 0.0023
	// outputCost = 5000 / 1M * 0.40 = 0.002 → roundUp = 0.002
	assert.Equal(t, 0.001, bd.InputCost, "non-cached input cost")
	assert.Equal(t, 0.0023, bd.CachedInputCost, "cached input at 25% rate")
	assert.Equal(t, 0.0, bd.CacheCreationInputCost, "Gemini has no cache creation concept")
	assert.Equal(t, 0.002, bd.OutputCost, "output cost")

	// totalCost = 0.001 + 0.00225 + 0.002 = 0.00525 → roundUp = 0.0053
	assert.Equal(t, 0.0053, bd.TotalCost, "total cost")
}

// TestCachedInputExceedsTotalInput verifies the guard that if cachedInput + cacheCreation
// somehow exceeds total input, nonCachedInput clamps to 0 (no negative costs).
func TestCachedInputExceedsTotalInput(t *testing.T) {
	ct := NewCostTracker()

	ct.SetPricingForModel("anthropic", "claude-test", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 3.00, Output: 15.00, CachedInput: 0.30, CacheCreationInput: 3.75},
		},
	})

	// Anomalous: cached(800) + creation(300) = 1100 > total input(1000)
	bd, err := ct.CalculateCostWithFuzzyMatch("anthropic", "claude-test", 1000, 100, 800, 300)
	assert.NoError(t, err)

	// nonCachedInput should clamp to 0, not go negative
	assert.Equal(t, 0.0, bd.InputCost, "non-cached input cost should be 0 when clamped")
	assert.True(t, bd.CachedInputCost > 0, "cached cost should still be calculated")
	assert.True(t, bd.CacheCreationInputCost > 0, "creation cost should still be calculated")
	assert.True(t, bd.TotalCost > 0, "total should be positive")
}
