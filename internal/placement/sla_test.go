package placement

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestJobFromMicroservice(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		check   func(t *testing.T, j Resources)
		wantErr []string
	}{
		{
			name: "container becomes docker and vram implies one GPU",
			in:   `{"microservice_name":"llm","virtualization":"container","code":"x","memory":4096,"vcpus":4,"vram":8000}`,
			check: func(t *testing.T, j Resources) {
				if !slices.Equal(j.Virtualization, []string{"docker"}) || j.Vgpus != 1 || j.Vram != 8000 || j.Memory != 4096 {
					t.Errorf("unexpected job %+v", j)
				}
			},
		},
		{
			name: "vgpus 0 with vram is bumped like Python truthiness",
			in:   `{"virtualization":"container","memory":1,"vcpus":1,"vgpus":0,"vram":10}`,
			check: func(t *testing.T, j Resources) {
				if j.Vgpus != 1 {
					t.Errorf("vgpus = %d", j.Vgpus)
				}
			},
		},
		{
			name: "crosvm keeps vgpus untouched",
			in:   `{"virtualization":"crosvm","memory":1,"vcpus":1,"vram":10}`,
			check: func(t *testing.T, j Resources) {
				if j.Vgpus != 0 || RequiredGPUs(j) != 0 {
					t.Errorf("unexpected job %+v", j)
				}
			},
		},
		{
			name: "constraints, volumes and stored id are kept",
			in: `{"_id":"s1","virtualization":"docker","memory":1,"vcpus":1,
				"constraints":[{"type":"direct","cluster":"c1","node":"w1"}],
				"volumes":[{"volume_id":"v","csi_driver":"nfs.csi.k8s.io"}]}`,
			check: func(t *testing.T, j Resources) {
				if j.ID != "s1" || len(j.Constraints) != 1 || j.Volumes[0].CSIDriver != "nfs.csi.k8s.io" {
					t.Errorf("unexpected job %+v", j)
				}
				if n, ok := PinnedName(j, LevelNode); !ok || n != "w1" {
					t.Errorf("pinned node %q", n)
				}
			},
		},
		{
			name:    "missing everything",
			in:      `{"microservice_name":"x"}`,
			wantErr: []string{"virtualization is missing", "memory is missing", "vcpus is missing"},
		},
		{
			name:    "wrong types",
			in:      `{"virtualization":5,"memory":"1G","vcpus":1.5,"vgpus":-1}`,
			wantErr: []string{"virtualization must be a string", "memory must be a number", "vcpus must be a non-negative integer", "vgpus must not be negative"},
		},
		{
			name:    "unikernel needs arch",
			in:      `{"virtualization":"unikernel","memory":1,"vcpus":1}`,
			wantErr: []string{"arch is required"},
		},
		{
			name:    "not an object",
			in:      `[1]`,
			wantErr: []string{"must be a JSON object"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j, err := JobFromMicroservice(json.RawMessage(tt.in))
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("expected error, got job %+v", j)
				}
				for _, w := range tt.wantErr {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("error %q lacks %q", err, w)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, j)
		})
	}
}
