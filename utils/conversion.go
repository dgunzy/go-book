package utils

import (
	"math"
	"strconv"
)

// AmericanToDecimal converts American odds to decimal odds
func AmericanToDecimal(americanOdds int) float64 {
	if americanOdds > 0 {
		return float64(americanOdds)/100.0 + 1.0
	} else if americanOdds < 0 {
		return (100.0 / float64(math.Abs(float64(americanOdds)))) + 1.0
	}
	return 1.0 // For American odds of 0 (which is not standard)
}

// DecimalToAmerican converts decimal odds to American odds
func DecimalToAmerican(decimalOdds float64) int {
	if decimalOdds >= 2.0 {
		return int(math.Round((decimalOdds - 1.0) * 100.0))
	} else if decimalOdds > 1.0 && decimalOdds < 2.0 {
		return int(math.Round(-100.0 / (decimalOdds - 1.0)))
	}
	return 0 // For decimal odds of 1.0 or less (which is not standard)
}

// AmericanStringToDecimal converts American odds from string to decimal odds
func AmericanStringToDecimal(americanOdds string) (float64, error) {
	odds, err := strconv.Atoi(americanOdds)
	if err != nil {
		return 0, err
	}
	return AmericanToDecimal(odds), nil
}

// DecimalStringToAmerican converts decimal odds from string to American odds
func DecimalStringToAmerican(decimalOdds string) (int, error) {
	odds, err := strconv.ParseFloat(decimalOdds, 64)
	if err != nil {
		return 0, err
	}
	return DecimalToAmerican(odds), nil
}
