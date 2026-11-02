package tui

import (
	"slices"
	"strings"
	"testing"

	"charm.land/huh/v2"
	"github.com/digitalocean/godo"
)

func TestExpandHosts(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		err  string
	}{
		{in: "web01, web02 web03", want: []string{"web01", "web02", "web03"}},
		{in: "worker*3", want: []string{"worker01", "worker02", "worker03"}},
		{in: "n*100", want: []string{"n001"}}, // checked by prefix below
		{in: "  ", err: "at least one"},
		{in: "bad_name", err: "not a valid hostname"},
		{in: "a, a", err: "listed twice"},
		{in: "x*0", err: "between 1 and 100"},
		{in: "x*101", err: "between 1 and 100"},
	}
	for _, tc := range cases {
		got, err := expandHosts(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%q: err = %v, want %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if tc.in == "n*100" {
			if len(got) != 100 || got[0] != "n001" || got[99] != "n100" {
				t.Errorf("n*100 = %v…", got[:3])
			}
			continue
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%q = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func values(opts []huh.Option[string]) []string {
	var out []string
	for _, o := range opts {
		out = append(out, o.Value)
	}
	return out
}

func TestSizeOptionsFilterAndSort(t *testing.T) {
	sizes := []godo.Size{
		{Slug: "big", Available: true, Regions: []string{"nyc3"}, PriceMonthly: 48, Memory: 8192, Vcpus: 4},
		{Slug: "small", Available: true, Regions: []string{"nyc3"}, PriceMonthly: 4, Memory: 512, Vcpus: 1},
		{Slug: "elsewhere", Available: true, Regions: []string{"ams3"}, PriceMonthly: 1},
		{Slug: "retired", Available: false, Regions: []string{"nyc3"}},
	}
	opts := sizeOptions(sizes, "nyc3")
	if got := values(opts); !slices.Equal(got, []string{"small", "big"}) {
		t.Errorf("sizes = %v", got)
	}
	if !strings.Contains(opts[0].Key, "512MB") || !strings.Contains(opts[1].Key, "8GB") || !strings.Contains(opts[1].Key, "$48/mo") {
		t.Errorf("labels = %q, %q", opts[0].Key, opts[1].Key)
	}
}

func TestImageOptions(t *testing.T) {
	images := []godo.Image{
		{Slug: "ubuntu-22-04-x64", Distribution: "Ubuntu", Name: "22.04 (LTS) x64", Status: "available", Regions: []string{"nyc3"}},
		{Slug: "ubuntu-24-04-x64", Distribution: "Ubuntu", Name: "24.04 (LTS) x64", Status: "available", Regions: []string{"nyc3"}},
		{Slug: "debian-12-x64", Distribution: "Debian", Name: "12 x64", Status: "available", Regions: []string{"nyc3"}},
		{Slug: "other-region", Distribution: "Fedora", Name: "40", Regions: []string{"ams3"}},
		{Slug: "", Distribution: "Custom", Name: "no slug"},
	}
	if got := values(imageOptions(images, "nyc3")); !slices.Equal(got, []string{"debian-12-x64", "ubuntu-24-04-x64", "ubuntu-22-04-x64"}) {
		t.Errorf("images = %v", got)
	}
}

func TestRegionOptionsAndPick(t *testing.T) {
	opts := regionOptions([]godo.Region{
		{Slug: "sfo3", Name: "San Francisco 3", Available: true},
		{Slug: "ams3", Name: "Amsterdam 3", Available: true},
		{Slug: "old1", Available: false},
	})
	if got := values(opts); !slices.Equal(got, []string{"ams3", "sfo3"}) {
		t.Errorf("regions = %v", got)
	}
	if pick(opts, "sfo3") != "sfo3" || pick(opts, "nyc3") != "ams3" {
		t.Error("pick should return the wanted value if offered, else the first option")
	}
}

func TestValidateTags(t *testing.T) {
	if err := validateTags("prod, k8s:cp"); err != nil {
		t.Error(err)
	}
	if err := validateTags("ok, not ok!"); err == nil {
		t.Error("expected invalid tag error")
	}
}

func TestNaturalCompare(t *testing.T) {
	names := []string{"AlmaLinux 9", "AlmaLinux 10", "AlmaLinux 8", "24.04 (LTS) x64", "22.04 (LTS) x64", "24.10 x64"}
	slices.SortFunc(names, func(a, b string) int { return naturalCompare(b, a) })
	want := []string{"AlmaLinux 10", "AlmaLinux 9", "AlmaLinux 8", "24.10 x64", "24.04 (LTS) x64", "22.04 (LTS) x64"}
	if !slices.Equal(names, want) {
		t.Errorf("got %v\nwant %v", names, want)
	}
}

func TestProjectOptions(t *testing.T) {
	opts := projectOptions([]godo.Project{
		{ID: "z", Name: "Zeta"},
		{ID: "d", Name: "My Default", IsDefault: true},
		{ID: "t1", Name: "Twin"},
		{ID: "t2", Name: "Twin"},
		{ID: "a", Name: "Alpha"},
	})
	var got []string
	for _, o := range opts {
		got = append(got, o.Key+"="+o.Value)
	}
	want := []string{"My Default  (default)=", "Alpha=Alpha", "Twin  (t1)=t1", "Twin  (t2)=t2", "Zeta=Zeta"}
	if !slices.Equal(got, want) {
		t.Errorf("projectOptions =\n%v\nwant\n%v", got, want)
	}
	if len(projectOptions(nil)) != 0 {
		t.Error("no projects should give no options")
	}
}

func TestValidateKeys(t *testing.T) {
	if validateKeys(nil) == nil {
		t.Error("no keys should be rejected")
	}
	if err := validateKeys([]string{"123"}); err != nil {
		t.Error(err)
	}
}
