package deployer

import (
	"testing"
	"time"
)

func TestParseDeployConfig(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   DeployConfig
	}{
		{
			"defaults",
			map[string]string{},
			DeployConfig{Strategy: "rolling", ShiftInterval: 30 * time.Second, ShiftStep: 20, ErrorThresh: 5, Rollback: true},
		},
		{
			"blue-green custom",
			map[string]string{
				labelStrategy:      "blue-green",
				labelShiftInterval: "1m",
				labelShiftStep:     "10",
				labelErrorThresh:   "3.5",
				labelRollback:      "false",
			},
			DeployConfig{Strategy: "blue-green", ShiftInterval: time.Minute, ShiftStep: 10, ErrorThresh: 3.5, Rollback: false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDeployConfig(tt.labels)
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
