package mcp

import (
	"fmt"
	"math"
	"time"
)

const (
	minShellExecuteTimeoutMs     = 1
	maxShellExecuteTimeoutMs     = 30 * 60 * 1000
	shellExecuteSettlementMargin = 10 * time.Second
)

func requiredShellExecuteTimeoutMs(args map[string]interface{}) (int, error) {
	value, ok := args["timeout_ms"]
	if !ok {
		return 0, fmt.Errorf("timeout_ms is required")
	}
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number {
		return 0, fmt.Errorf("timeout_ms must be an integer")
	}
	if number < float64(minShellExecuteTimeoutMs) || number > float64(maxShellExecuteTimeoutMs) {
		return 0, fmt.Errorf(
			"timeout_ms must be between %d and %d",
			minShellExecuteTimeoutMs,
			maxShellExecuteTimeoutMs,
		)
	}
	return int(number), nil
}

func validateShellExecuteTimeoutMs(timeoutMs int) error {
	if timeoutMs < minShellExecuteTimeoutMs || timeoutMs > maxShellExecuteTimeoutMs {
		return fmt.Errorf(
			"timeout_ms must be between %d and %d",
			minShellExecuteTimeoutMs,
			maxShellExecuteTimeoutMs,
		)
	}
	return nil
}

func shellExecuteRequestTimeout(timeoutMs int) (time.Duration, error) {
	if err := validateShellExecuteTimeoutMs(timeoutMs); err != nil {
		return 0, err
	}
	return time.Duration(timeoutMs)*time.Millisecond + shellExecuteSettlementMargin, nil
}
