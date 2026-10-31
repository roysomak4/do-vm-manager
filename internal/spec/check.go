package spec

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/digitalocean/godo"

	"github.com/roysomak4/do-vm-manager/internal/cloud"
)

// Estimate is the projected cost of a spec.
type Estimate struct {
	Droplets     int
	MonthlyPrice float64
	HourlyPrice  float64
}

// Check validates the spec against live account data: the region, every size,
// the image and every SSH key must exist and be usable together. On success it
// returns the cost estimate.
func (s *Spec) Check(cat *cloud.Catalog) (*Estimate, error) {
	var errs []error

	region := findRegion(cat.Regions, s.Region)
	switch {
	case region == nil:
		errs = append(errs, fmt.Errorf("region %q does not exist", s.Region))
	case !region.Available:
		errs = append(errs, fmt.Errorf("region %q is not currently available", s.Region))
	}

	est := &Estimate{}
	for _, h := range s.Hosts() {
		est.Droplets++
		size := findSize(cat.Sizes, h.Size)
		if size == nil {
			continue
		}
		est.MonthlyPrice += size.PriceMonthly
		est.HourlyPrice += size.PriceHourly
	}

	for _, name := range s.roleNames() {
		slug := s.Roles[name].Size
		size := findSize(cat.Sizes, slug)
		switch {
		case size == nil:
			errs = append(errs, fmt.Errorf("role %q: size %q does not exist", name, slug))
		case !size.Available:
			errs = append(errs, fmt.Errorf("role %q: size %q is not currently available", name, slug))
		case region != nil && !slices.Contains(size.Regions, s.Region):
			errs = append(errs, fmt.Errorf("role %q: size %q is not offered in region %q", name, slug, s.Region))
		}
	}

	switch img := cat.Image; {
	case img == nil:
		errs = append(errs, fmt.Errorf("image %q does not exist", s.Image))
	case img.Status != "" && img.Status != "available":
		errs = append(errs, fmt.Errorf("image %q is %s", s.Image, img.Status))
	case len(img.Regions) > 0 && !slices.Contains(img.Regions, s.Region):
		errs = append(errs, fmt.Errorf("image %q is not available in region %q", s.Image, s.Region))
	}

	for _, k := range s.SSHKeys {
		if !hasKey(cat.SSHKeys, k) {
			errs = append(errs, fmt.Errorf("SSH key %q is not registered with this account", k))
		}
	}

	if s.Project != "" {
		if _, err := FindProject(cat.Projects, s.Project); err != nil {
			errs = append(errs, err)
		}
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return est, nil
}

// FindProject finds a project by ID or exact name.
func FindProject(projects []godo.Project, ref string) (*godo.Project, error) {
	var byName []*godo.Project
	var similar []string
	for i := range projects {
		p := &projects[i]
		switch {
		case p.ID == ref:
			return p, nil
		case p.Name == ref:
			byName = append(byName, p)
		case strings.EqualFold(p.Name, ref):
			similar = append(similar, p.Name)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		msg := fmt.Sprintf("project %q does not exist", ref)
		if len(similar) > 0 {
			msg += fmt.Sprintf(" (did you mean %q? names are case-sensitive)", similar[0])
		}
		return nil, errors.New(msg)
	}
	return nil, fmt.Errorf("%d projects are named %q; use the project's ID instead", len(byName), ref)
}

func findRegion(regions []godo.Region, slug string) *godo.Region {
	for i := range regions {
		if regions[i].Slug == slug {
			return &regions[i]
		}
	}
	return nil
}

func findSize(sizes []godo.Size, slug string) *godo.Size {
	for i := range sizes {
		if sizes[i].Slug == slug {
			return &sizes[i]
		}
	}
	return nil
}

// hasKey matches an SSH key reference by numeric ID or fingerprint.
func hasKey(keys []godo.Key, ref string) bool {
	id, idErr := strconv.Atoi(ref)
	for _, k := range keys {
		if k.Fingerprint == ref || (idErr == nil && k.ID == id) {
			return true
		}
	}
	return false
}
