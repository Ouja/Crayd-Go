package models

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Money is an amount in cents. Integer cents avoid float rounding bugs.
type Money int64

// String formats the amount for display, e.g. "$12.30".
func (m Money) String() string {
	sign := ""
	if m < 0 {
		sign, m = "-", -m
	}
	return fmt.Sprintf("%s$%d.%02d", sign, m/100, m%100)
}

// ParseMoney reads a decimal amount such as "19.99" or "5" into cents.
func ParseMoney(s string) (Money, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1e9 {
		return 0, false
	}
	return Money(math.Round(f * 100)), true
}
