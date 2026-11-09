package adminaccess

import "testing"

func TestRequestTimingRoutesHaveExplicitPermissions(t *testing.T) {
	routes := map[string]string{
		"GET /usage/:id/timing":                    "usage.read",
		"GET /ops/dashboard/request-timing-trend":  "ops.read",
		"GET /ops/dashboard/request-timing-groups": "ops.read",
		"GET /ops/requests/slow-timing":            "ops.errors.read",
	}
	for route, want := range routes {
		found := ""
		for _, d := range definitions {
			for _, r := range d.Routes {
				if r == route {
					found = d.ID
				}
			}
		}
		if found != want {
			t.Fatalf("%s permission=%q want %q", route, found, want)
		}
	}
}
