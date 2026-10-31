package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"charm.land/huh/v2"
	"github.com/digitalocean/godo"

	"github.com/roysomak4/do-vm-manager/internal/cloud"
	"github.com/roysomak4/do-vm-manager/internal/spec"
)

// WizardCatalog is the account data the wizard offers as choices.
type WizardCatalog struct {
	Regions []godo.Region
	Sizes   []godo.Size
	Images  []godo.Image // distribution images
	SSHKeys []godo.Key
	// Projects is nil when the token cannot list projects; the wizard then
	// leaves droplets in the default project without asking.
	Projects []godo.Project
}

// ErrAborted is returned when the user quits the wizard or declines to save.
var ErrAborted = errors.New("aborted")

const (
	defaultImage = "ubuntu-24-04-x64"
	defaultSize  = "s-2vcpu-4gb"
)

// Wizard asks for a cluster spec, choosing from live account data, and checks
// the result. It returns ErrAborted if the user quits or declines to save to path.
func Wizard(ctx context.Context, cat WizardCatalog, path string) (*spec.Spec, error) {
	s := &spec.Spec{Monitoring: true, Roles: map[string]spec.Role{}}
	run := func(groups ...*huh.Group) error {
		err := huh.NewForm(groups...).RunWithContext(ctx)
		if errors.Is(err, huh.ErrUserAborted) {
			return ErrAborted
		}
		return err
	}

	regions := regionOptions(cat.Regions)
	if len(regions) == 0 {
		return nil, errors.New("no regions are available to this account")
	}
	s.Region = pick(regions, "nyc3")
	if err := run(huh.NewGroup(
		huh.NewInput().Title("Cluster name").
			Description("Droplets are tagged dovm:<name> so dovm can find them later.").
			Placeholder("my-cluster").Value(&s.Name).Validate(spec.ValidateName),
		huh.NewSelect[string]().Title("Region").Height(10).
			Options(regions...).Value(&s.Region),
	)); err != nil {
		return nil, err
	}

	images := imageOptions(cat.Images, s.Region)
	sizes := sizeOptions(cat.Sizes, s.Region)
	if len(images) == 0 || len(sizes) == 0 {
		return nil, fmt.Errorf("region %s offers no images or no sizes to this account; choose another region", s.Region)
	}
	s.Image = pick(images, defaultImage)
	var tags string
	fields := []huh.Field{
		huh.NewSelect[string]().Title("Image").Height(10).Options(images...).Value(&s.Image),
	}
	if projects := projectOptions(cat.Projects); len(projects) > 1 {
		fields = append(fields, huh.NewSelect[string]().Title("Project").
			Description("The DigitalOcean project the droplets are put in.").Height(8).
			Options(projects...).Value(&s.Project))
	}
	if len(cat.SSHKeys) > 0 {
		fields = append(fields, huh.NewMultiSelect[string]().Title("SSH keys").
			Description("space to select, enter to continue").Height(8).
			Options(keyOptions(cat.SSHKeys)...).Value(&s.SSHKeys))
	}
	fields = append(fields,
		huh.NewInput().Title("Extra tags for every droplet").
			Description("Optional, comma-separated.").Value(&tags).Validate(validateTags),
		huh.NewConfirm().Title("Enable monitoring?").Value(&s.Monitoring),
		huh.NewConfirm().Title("Enable IPv6?").Value(&s.IPv6),
	)
	if err := run(huh.NewGroup(fields...)); err != nil {
		return nil, err
	}
	s.Tags = splitList(tags)

	for {
		if err := askRole(run, s, sizes); err != nil {
			return nil, err
		}
		more := false
		if err := run(huh.NewGroup(huh.NewConfirm().Title("Add another role?").Value(&more))); err != nil {
			return nil, err
		}
		if !more {
			break
		}
	}

	if err := s.Validate(); err != nil {
		return nil, err
	}
	est, err := s.Check(checkCatalog(cat, s.Image))
	if err != nil {
		return nil, err
	}

	save := true
	if err := run(huh.NewGroup(
		huh.NewNote().Title("Summary").Description(summary(s, est)),
		huh.NewConfirm().Title("Save to "+path+"?").Value(&save),
	)); err != nil {
		return nil, err
	}
	if !save {
		return nil, ErrAborted
	}
	return s, nil
}

func askRole(run func(...*huh.Group) error, s *spec.Spec, sizes []huh.Option[string]) error {
	name := "worker"
	if len(s.Roles) > 0 {
		name = ""
	}
	size := pick(sizes, defaultSize)
	var hosts string
	err := run(huh.NewGroup(
		huh.NewInput().Title("Role name").
			Description("Groups hosts that share a size; also added as a tag.").
			Value(&name).Validate(func(v string) error {
			if !spec.ValidTag(v) {
				return errors.New("use letters, digits, dashes, underscores or colons")
			}
			if _, dup := s.Roles[v]; dup {
				return fmt.Errorf("role %q already added", v)
			}
			return nil
		}),
		huh.NewSelect[string]().Title("Droplet size").
			Description("Type / to filter.").Height(10).Options(sizes...).Value(&size),
		huh.NewInput().Title("Hostnames").
			Description("Comma-separated (web01, web02), or prefix*count (worker*3 → worker01..worker03).").
			Value(&hosts).Validate(func(v string) error {
			names, err := expandHosts(v)
			if err != nil {
				return err
			}
			for _, r := range s.Roles {
				for _, h := range names {
					if slices.Contains(r.Hosts, h) {
						return fmt.Errorf("hostname %q is already in another role", h)
					}
				}
			}
			return nil
		}),
	))
	if err != nil {
		return err
	}
	names, _ := expandHosts(hosts)
	s.Roles[name] = spec.Role{Size: size, Hosts: names}
	return nil
}

// pick returns want if it is one of opts, otherwise the first option.
func pick(opts []huh.Option[string], want string) string {
	for _, o := range opts {
		if o.Value == want {
			return want
		}
	}
	return opts[0].Value
}

var countRe = regexp.MustCompile(`^([a-zA-Z0-9][a-zA-Z0-9.-]*)\*([0-9]+)$`)

// expandHosts parses "a, b c" or "prefix*N" into hostnames.
func expandHosts(input string) ([]string, error) {
	input = strings.TrimSpace(input)
	if m := countRe.FindStringSubmatch(input); m != nil {
		n, _ := strconv.Atoi(m[2])
		if n < 1 || n > 100 {
			return nil, errors.New("count must be between 1 and 100")
		}
		width := max(2, len(strconv.Itoa(n)))
		names := make([]string, n)
		for i := range n {
			names[i] = fmt.Sprintf("%s%0*d", m[1], width, i+1)
		}
		return names, nil
	}
	names := splitList(input)
	if len(names) == 0 {
		return nil, errors.New("enter at least one hostname")
	}
	seen := map[string]bool{}
	for _, n := range names {
		if !spec.ValidHostname(n) {
			return nil, fmt.Errorf("%q is not a valid hostname: use letters, digits, periods and dashes", n)
		}
		if seen[n] {
			return nil, fmt.Errorf("%q is listed twice", n)
		}
		seen[n] = true
	}
	return names, nil
}

func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}

func validateTags(v string) error {
	for _, t := range splitList(v) {
		if !spec.ValidTag(t) {
			return fmt.Errorf("%q is not a valid tag", t)
		}
	}
	return nil
}

func regionOptions(regions []godo.Region) []huh.Option[string] {
	regions = slices.Clone(regions)
	slices.SortFunc(regions, func(a, b godo.Region) int { return cmp.Compare(a.Slug, b.Slug) })
	var opts []huh.Option[string]
	for _, r := range regions {
		if r.Available {
			opts = append(opts, huh.NewOption(fmt.Sprintf("%-5s %s", r.Slug, r.Name), r.Slug))
		}
	}
	return opts
}

// imageOptions lists available images in region, grouped by distribution with
// newest versions first.
func imageOptions(images []godo.Image, region string) []huh.Option[string] {
	var usable []godo.Image
	for _, img := range images {
		if img.Slug != "" && (img.Status == "" || img.Status == "available") &&
			(len(img.Regions) == 0 || slices.Contains(img.Regions, region)) {
			usable = append(usable, img)
		}
	}
	slices.SortFunc(usable, func(a, b godo.Image) int {
		return cmp.Or(cmp.Compare(a.Distribution, b.Distribution), naturalCompare(b.Name, a.Name))
	})
	var opts []huh.Option[string]
	for _, img := range usable {
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s %s  (%s)", img.Distribution, img.Name, img.Slug), img.Slug))
	}
	return opts
}

// sizeOptions lists sizes available in region, cheapest first.
func sizeOptions(sizes []godo.Size, region string) []huh.Option[string] {
	var usable []godo.Size
	for _, sz := range sizes {
		if sz.Available && slices.Contains(sz.Regions, region) {
			usable = append(usable, sz)
		}
	}
	slices.SortFunc(usable, func(a, b godo.Size) int {
		return cmp.Or(cmp.Compare(a.PriceMonthly, b.PriceMonthly), cmp.Compare(a.Slug, b.Slug))
	})
	var opts []huh.Option[string]
	for _, sz := range usable {
		label := fmt.Sprintf("%-22s %2d vCPU  %5s RAM  %4d GB disk  $%g/mo",
			sz.Slug, sz.Vcpus, formatMemory(sz.Memory), sz.Disk, sz.PriceMonthly)
		opts = append(opts, huh.NewOption(label, sz.Slug))
	}
	return opts
}

// naturalCompare compares strings with digit runs compared as numbers, so
// "10" sorts after "9".
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		da, db := digitPrefix(a), digitPrefix(b)
		if da != "" && db != "" {
			na, _ := strconv.Atoi(da)
			nb, _ := strconv.Atoi(db)
			if c := cmp.Compare(na, nb); c != 0 {
				return c
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if c := cmp.Compare(a[0], b[0]); c != 0 {
			return c
		}
		a, b = a[1:], b[1:]
	}
	return cmp.Compare(len(a), len(b))
}

func digitPrefix(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

func formatMemory(mb int) string {
	if mb < 1024 {
		return fmt.Sprintf("%dMB", mb)
	}
	return fmt.Sprintf("%gGB", float64(mb)/1024)
}

// projectOptions lists the default project first, as the empty value, which
// leaves droplets where DigitalOcean puts them. A project whose name is shared
// with another is chosen by ID.
func projectOptions(projects []godo.Project) []huh.Option[string] {
	projects = slices.Clone(projects)
	slices.SortFunc(projects, func(a, b godo.Project) int {
		return cmp.Or(-cmp.Compare(boolInt(a.IsDefault), boolInt(b.IsDefault)), cmp.Compare(a.Name, b.Name))
	})
	count := map[string]int{}
	for _, p := range projects {
		count[p.Name]++
	}
	var opts []huh.Option[string]
	for _, p := range projects {
		switch {
		case p.IsDefault:
			opts = append(opts, huh.NewOption(p.Name+"  (default)", ""))
		case count[p.Name] > 1:
			opts = append(opts, huh.NewOption(fmt.Sprintf("%s  (%s)", p.Name, p.ID), p.ID))
		default:
			opts = append(opts, huh.NewOption(p.Name, p.Name))
		}
	}
	return opts
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func keyOptions(keys []godo.Key) []huh.Option[string] {
	var opts []huh.Option[string]
	for _, k := range keys {
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s  (%s)", k.Name, k.Fingerprint), strconv.Itoa(k.ID)))
	}
	return opts
}

// checkCatalog adapts the wizard's data to what spec.Check expects.
func checkCatalog(cat WizardCatalog, image string) *cloud.Catalog {
	c := &cloud.Catalog{Regions: cat.Regions, Sizes: cat.Sizes, SSHKeys: cat.SSHKeys, Projects: cat.Projects}
	for i := range cat.Images {
		if cat.Images[i].Slug == image {
			c.Image = &cat.Images[i]
		}
	}
	return c
}

func summary(s *spec.Spec, est *spec.Estimate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s in %s, image %s\n", s.Name, s.Region, s.Image)
	if s.Project != "" {
		fmt.Fprintf(&b, "project %s\n", s.Project)
	}
	b.WriteString("\n")
	roles := make([]string, 0, len(s.Roles))
	for r := range s.Roles {
		roles = append(roles, r)
	}
	slices.Sort(roles)
	for _, r := range roles {
		role := s.Roles[r]
		fmt.Fprintf(&b, "%s (%s): %s\n", r, role.Size, strings.Join(role.Hosts, ", "))
	}
	fmt.Fprintf(&b, "\n%d droplets, estimated $%.2f/month", est.Droplets, est.MonthlyPrice)
	return b.String()
}
