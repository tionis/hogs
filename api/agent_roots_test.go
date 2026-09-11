package api

import "testing"

func TestWritableRootRelMapsDataRootToDot(t *testing.T) {
	for _, test := range []struct {
		name    string
		data    string
		allowed string
		want    string
		wantOK  bool
	}{
		{"data root itself", "/srv/valheim/data", "/srv/valheim/data", ".", true},
		{"subdirectory", "/srv/valheim/data", "/srv/valheim/data/worlds_local", "worlds_local", true},
		{"nested", "/srv/mc/cog", "/srv/mc/cog/config", "config", true},
		{"sibling escapes", "/srv/valheim/data", "/srv/valheim/other", "", false},
		{"parent escapes", "/srv/valheim/data", "/srv/valheim", "", false},
		{"absolute escape", "/srv/valheim/data", "/etc/passwd", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := writableRootRel(test.data, test.allowed)
			if got != test.want || ok != test.wantOK {
				t.Fatalf("writableRootRel=%q,%v", got, ok)
			}
		})
	}
}
