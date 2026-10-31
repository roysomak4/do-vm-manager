package spec

import (
	"math"
	"strings"
	"testing"

	"github.com/digitalocean/godo"

	"github.com/roysomak4/do-vm-manager/internal/cloud"
)

func testCatalog() *cloud.Catalog {
	return &cloud.Catalog{
		Regions: []godo.Region{
			{Slug: "nyc3", Available: true},
			{Slug: "sfo1", Available: false},
		},
		Sizes: []godo.Size{
			{Slug: "s-2vcpu-2gb", Available: true, Regions: []string{"nyc3"}, PriceMonthly: 18, PriceHourly: 0.027},
			{Slug: "s-2vcpu-4gb", Available: true, Regions: []string{"nyc3"}, PriceMonthly: 24, PriceHourly: 0.036},
			{Slug: "gpu-only-tor", Available: true, Regions: []string{"tor1"}},
			{Slug: "retired", Available: false},
		},
		SSHKeys: []godo.Key{{ID: 3871344, Fingerprint: "aa:bb:cc"}},
		Image:   &godo.Image{Slug: "ubuntu-24-04-x64", Status: "available", Regions: []string{"nyc3"}},
	}
}

func TestCheckValid(t *testing.T) {
	s, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	est, err := s.Check(testCatalog())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if est.Droplets != 3 {
		t.Errorf("Droplets = %d, want 3", est.Droplets)
	}
	if est.MonthlyPrice != 18+24+24 {
		t.Errorf("MonthlyPrice = %v, want 66", est.MonthlyPrice)
	}
	if math.Abs(est.HourlyPrice-0.099) > 1e-9 {
		t.Errorf("HourlyPrice = %v, want 0.099", est.HourlyPrice)
	}
}

func TestCheckReportsAllMismatches(t *testing.T) {
	s := &Spec{
		Region:  "nyc3",
		Image:   "ubuntu-24-04-x64",
		SSHKeys: []string{"3871344", "99", "zz:zz"},
		Roles: map[string]Role{
			"a": {Size: "missing", Hosts: []string{"h1"}},
			"b": {Size: "retired", Hosts: []string{"h2"}},
			"c": {Size: "gpu-only-tor", Hosts: []string{"h3"}},
		},
	}
	cat := testCatalog()
	cat.Image.Regions = []string{"ams3"}

	_, err := s.Check(cat)
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{
		`role "a": size "missing" does not exist`,
		`role "b": size "retired" is not currently available`,
		`role "c": size "gpu-only-tor" is not offered in region "nyc3"`,
		`image "ubuntu-24-04-x64" is not available in region "nyc3"`,
		`SSH key "99" is not registered`,
		`SSH key "zz:zz" is not registered`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing error %q in:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), `"3871344"`) {
		t.Errorf("valid key ID reported as missing:\n%v", err)
	}
}

func TestCheckRegionAndImage(t *testing.T) {
	cases := []struct {
		name   string
		region string
		image  *godo.Image
		want   string
	}{
		{"unknown region", "xyz9", testCatalog().Image, `region "xyz9" does not exist`},
		{"unavailable region", "sfo1", testCatalog().Image, `region "sfo1" is not currently available`},
		{"missing image", "nyc3", nil, `image "img" does not exist`},
		{"deleted image", "nyc3", &godo.Image{Status: "deleted"}, `image "img" is deleted`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Spec{Region: tc.region, Image: "img", Roles: map[string]Role{"w": {Size: "s-2vcpu-2gb", Hosts: []string{"h"}}}}
			cat := testCatalog()
			cat.Image = tc.image
			_, err := s.Check(cat)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestFindProject(t *testing.T) {
	projects := []godo.Project{
		{ID: "id-1", Name: "Default", IsDefault: true},
		{ID: "id-2", Name: "Lab"},
		{ID: "id-3", Name: "Twin"},
		{ID: "id-4", Name: "Twin"},
	}
	for ref, wantID := range map[string]string{"Lab": "id-2", "id-3": "id-3", "Default": "id-1"} {
		if p, err := FindProject(projects, ref); err != nil || p.ID != wantID {
			t.Errorf("FindProject(%q) = %v, %v; want %s", ref, p, err, wantID)
		}
	}
	for ref, want := range map[string]string{
		"lab":  `project "lab" does not exist (did you mean "Lab"?`,
		"Nope": `project "Nope" does not exist`,
		"Twin": `2 projects are named "Twin"; use the project's ID`,
	} {
		if _, err := FindProject(projects, ref); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("FindProject(%q) error = %v, want %q", ref, err, want)
		}
	}
}

func TestCheckProject(t *testing.T) {
	s, err := Parse([]byte(validYAML + "project: Lab\n"))
	if err != nil {
		t.Fatal(err)
	}
	cat := testCatalog()
	if _, err := s.Check(cat); err == nil || !strings.Contains(err.Error(), `project "Lab" does not exist`) {
		t.Errorf("missing project: %v", err)
	}
	cat.Projects = []godo.Project{{ID: "p", Name: "Lab"}}
	if _, err := s.Check(cat); err != nil {
		t.Errorf("Check: %v", err)
	}
}
