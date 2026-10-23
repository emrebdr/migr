package zerodowntime

import (
	"fmt"
	"time"
)

// DefaultBudget is the longest a client query may wait while an operation that promised no wait runs.
const DefaultBudget = 1500 * time.Millisecond

// BudgetVariable overrides the budget, as a Go duration, for a machine slower than the one it was set on.
const BudgetVariable = "MIGR_ZERODOWNTIME_BUDGET"

// budgetFrom reads the budget from the variable's value, the default where it is empty.
func budgetFrom(value string) (time.Duration, error) {
	if value == "" {
		return DefaultBudget, nil
	}

	budget, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a Go duration: %w", BudgetVariable, value, err)
	}

	if budget <= 0 {
		return 0, fmt.Errorf("%s=%q is no budget at all, and every query would exceed it", BudgetVariable, value)
	}

	return budget, nil
}
