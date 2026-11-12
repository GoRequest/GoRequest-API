package main

import (
	"testing"

	"github.com/MarlonBBo/GoRequest/internal/handler"
)

func TestExecutionLimitsFromEnv(t *testing.T) {
	t.Setenv("EXECUTE_REQUESTS_PER_MINUTE", "30")
	t.Setenv("EXECUTE_CONCURRENT_PER_IP", "3")
	t.Setenv("EXECUTE_CONCURRENT_TOTAL", "8")
	limits, err := executionLimitsFromEnv()
	if err != nil || limits != (handler.ExecutionLimits{RequestsPerMinute: 30, ConcurrentPerIP: 3, ConcurrentTotal: 8}) {
		t.Fatalf("limits=%+v error=%v", limits, err)
	}
	for _, key := range []string{"EXECUTE_REQUESTS_PER_MINUTE", "EXECUTE_CONCURRENT_PER_IP", "EXECUTE_CONCURRENT_TOTAL"} {
		for _, value := range []string{"", "0", "-1", "abc", "1.5", "99999999999999999999999999"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				t.Setenv(key, value)
				if _, err := executionLimitsFromEnv(); err == nil {
					t.Fatalf("accepted %s=%q", key, value)
				}
			})
		}
	}
}
