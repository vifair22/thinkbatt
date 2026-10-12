package caps

import (
	"testing"

	"git.airies.net/vifair22/thinkbatt/internal/battery"
)

func TestDetect(t *testing.T) {
	cases := []struct {
		name          string
		supported     []battery.Behaviour
		hasThresholds bool
		wantBand      bool
		wantCalib     bool
	}{
		{
			name:          "coreboot thinkpad: full set",
			supported:     []battery.Behaviour{battery.BehaviourAuto, battery.BehaviourInhibitCharge, battery.BehaviourForceDischarge},
			hasThresholds: true,
			wantBand:      true,
			wantCalib:     true,
		},
		{
			name:          "stock firmware: thresholds, no force-discharge",
			supported:     []battery.Behaviour{battery.BehaviourAuto},
			hasThresholds: true,
			wantBand:      true,
			wantCalib:     false,
		},
		{
			name:          "no thresholds, no behaviours",
			supported:     []battery.Behaviour{battery.BehaviourAuto},
			hasThresholds: false,
			wantBand:      false,
			wantCalib:     false,
		},
		{
			name:          "force-discharge but no thresholds cannot calibrate",
			supported:     []battery.Behaviour{battery.BehaviourAuto, battery.BehaviourForceDischarge},
			hasThresholds: false,
			wantBand:      false,
			wantCalib:     false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sim := battery.NewSim(battery.SimConfig{
				Supported:     c.supported,
				HasThresholds: c.hasThresholds,
			})
			p, err := Detect(sim)
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if p.CanManageBand() != c.wantBand {
				t.Errorf("CanManageBand = %t, want %t", p.CanManageBand(), c.wantBand)
			}
			if p.CanCalibrate() != c.wantCalib {
				t.Errorf("CanCalibrate = %t, want %t", p.CanCalibrate(), c.wantCalib)
			}
			if s := p.Summary(); s == "" {
				t.Errorf("empty summary")
			}
		})
	}
}
