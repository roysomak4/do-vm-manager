// Package cloud wraps the DigitalOcean API with retries, pagination and the
// lookups dovm needs.
package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"
	"golang.org/x/sync/errgroup"
)

const (
	userAgent = "dovm"
	perPage   = 200
)

// Options tunes the API client. Zero values fall back to defaults.
type Options struct {
	BaseURL      string        // override the API endpoint, used by tests
	Timeout      time.Duration // per HTTP attempt
	RetryMax     int
	RetryWaitMin time.Duration
	RetryWaitMax time.Duration
}

func (o Options) withDefaults() Options {
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	if o.RetryMax == 0 {
		o.RetryMax = 5
	}
	if o.RetryWaitMin == 0 {
		o.RetryWaitMin = time.Second
	}
	if o.RetryWaitMax == 0 {
		o.RetryWaitMax = 30 * time.Second
	}
	return o
}

// Client is a DigitalOcean API client. Reads and deletes retry 429 and 5xx
// responses with exponential backoff, honoring Retry-After. Creates are never
// retried here: a 5xx may mean the droplets were created anyway, so the caller
// must check what exists before trying again.
type Client struct {
	godo   *godo.Client // retrying, for idempotent requests
	create *godo.Client // non-retrying, for droplet creation
}

// New builds a client authenticated with token.
func New(token string, opts Options) (*Client, error) {
	if token == "" {
		return nil, errors.New("no DigitalOcean API token provided")
	}
	opts = opts.withDefaults()

	newGodo := func(extra ...godo.ClientOpt) (*godo.Client, error) {
		// godo's retry layer requires the underlying client to use an oauth2 transport.
		httpClient := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
		httpClient.Timeout = opts.Timeout
		clientOpts := []godo.ClientOpt{godo.SetUserAgent(userAgent)}
		if opts.BaseURL != "" {
			clientOpts = append(clientOpts, godo.SetBaseURL(opts.BaseURL))
		}
		return godo.New(httpClient, append(clientOpts, extra...)...)
	}

	waitMin := opts.RetryWaitMin.Seconds()
	waitMax := opts.RetryWaitMax.Seconds()
	retrying, err := newGodo(godo.WithRetryAndBackoffs(godo.RetryConfig{
		RetryMax:     opts.RetryMax,
		RetryWaitMin: &waitMin,
		RetryWaitMax: &waitMax,
	}))
	if err != nil {
		return nil, err
	}
	create, err := newGodo()
	if err != nil {
		return nil, err
	}
	return &Client{godo: retrying, create: create}, nil
}

// listAll follows pagination until the last page.
func listAll[T any](ctx context.Context, list func(context.Context, *godo.ListOptions) ([]T, *godo.Response, error)) ([]T, error) {
	var all []T
	opt := &godo.ListOptions{Page: 1, PerPage: perPage}
	for {
		items, resp, err := list(ctx, opt)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			return all, nil
		}
		opt.Page++
	}
}

// Droplets lists every droplet in the account.
func (c *Client) Droplets(ctx context.Context) ([]godo.Droplet, error) {
	return listAll(ctx, c.godo.Droplets.List)
}

// DropletsByTag lists the droplets carrying tag.
func (c *Client) DropletsByTag(ctx context.Context, tag string) ([]godo.Droplet, error) {
	return listAll(ctx, func(ctx context.Context, opt *godo.ListOptions) ([]godo.Droplet, *godo.Response, error) {
		return c.godo.Droplets.ListByTag(ctx, tag, opt)
	})
}

// CreateDroplets creates up to 10 droplets in one request. It makes exactly one
// attempt; see Client.
func (c *Client) CreateDroplets(ctx context.Context, req *godo.DropletMultiCreateRequest) ([]godo.Droplet, error) {
	droplets, _, err := c.create.Droplets.CreateMultiple(ctx, req)
	return droplets, err
}

// DeleteDroplet deletes a droplet. Deleting one that is already gone succeeds.
func (c *Client) DeleteDroplet(ctx context.Context, id int) error {
	_, err := c.godo.Droplets.Delete(ctx, id)
	if isNotFound(err) {
		return nil
	}
	return err
}

// Regions lists every region.
func (c *Client) Regions(ctx context.Context) ([]godo.Region, error) {
	return listAll(ctx, c.godo.Regions.List)
}

// Sizes lists every droplet size.
func (c *Client) Sizes(ctx context.Context) ([]godo.Size, error) {
	return listAll(ctx, c.godo.Sizes.List)
}

// SSHKeys lists the SSH keys registered with the account.
func (c *Client) SSHKeys(ctx context.Context) ([]godo.Key, error) {
	return listAll(ctx, c.godo.Keys.List)
}

// DistributionImages lists the public OS images, such as Ubuntu and Debian.
func (c *Client) DistributionImages(ctx context.Context) ([]godo.Image, error) {
	return listAll(ctx, c.godo.Images.ListDistribution)
}

// Image looks up an image by numeric ID or slug. It returns nil, nil when the
// image does not exist.
func (c *Client) Image(ctx context.Context, ref string) (*godo.Image, error) {
	var (
		img *godo.Image
		err error
	)
	if id, convErr := strconv.Atoi(ref); convErr == nil {
		img, _, err = c.godo.Images.GetByID(ctx, id)
	} else {
		img, _, err = c.godo.Images.GetBySlug(ctx, ref)
	}
	if isNotFound(err) {
		return nil, nil
	}
	return img, err
}

// Projects lists the account's projects.
func (c *Client) Projects(ctx context.Context) ([]godo.Project, error) {
	return listAll(ctx, c.godo.Projects.List)
}

// ProjectDropletIDs lists the IDs of the droplets assigned to a project.
func (c *Client) ProjectDropletIDs(ctx context.Context, projectID string) ([]int, error) {
	resources, err := listAll(ctx, func(ctx context.Context, opt *godo.ListOptions) ([]godo.ProjectResource, *godo.Response, error) {
		return c.godo.Projects.ListResources(ctx, projectID, opt)
	})
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, r := range resources {
		if id, ok := strings.CutPrefix(r.URN, "do:droplet:"); ok {
			if n, err := strconv.Atoi(id); err == nil {
				ids = append(ids, n)
			}
		}
	}
	return ids, nil
}

// maxAssign caps the droplets moved into a project per request.
const maxAssign = 50

// AssignDroplets moves droplets into a project. Assigning a droplet that is
// already in the project changes nothing, so this is safe to repeat.
func (c *Client) AssignDroplets(ctx context.Context, projectID string, ids []int) error {
	for chunk := range slices.Chunk(ids, maxAssign) {
		urns := make([]any, len(chunk))
		for i, id := range chunk {
			urns[i] = godo.ToURN("droplet", id)
		}
		resources, _, err := c.godo.Projects.AssignResources(ctx, projectID, urns...)
		if err != nil {
			return err
		}
		var errs []error
		for _, r := range resources {
			if r.Status != "" && r.Status != "ok" && r.Status != "assigned" {
				errs = append(errs, fmt.Errorf("%s: %s", r.URN, r.Status))
			}
		}
		if err := errors.Join(errs...); err != nil {
			return err
		}
	}
	return nil
}

// Catalog is the live account data a spec is checked against.
type Catalog struct {
	Regions  []godo.Region
	Sizes    []godo.Size
	SSHKeys  []godo.Key
	Image    *godo.Image    // nil when the requested image does not exist
	Projects []godo.Project // only fetched when asked for
}

// Catalog fetches regions, sizes, SSH keys and the given image concurrently,
// plus the account's projects if projects is set. Projects are optional so
// that tokens without project access still work for specs that use none.
func (c *Client) Catalog(ctx context.Context, image string, projects bool) (*Catalog, error) {
	var cat Catalog
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { cat.Regions, err = c.Regions(ctx); return })
	g.Go(func() (err error) { cat.Sizes, err = c.Sizes(ctx); return })
	g.Go(func() (err error) { cat.SSHKeys, err = c.SSHKeys(ctx); return })
	g.Go(func() (err error) { cat.Image, err = c.Image(ctx, image); return })
	if projects {
		g.Go(func() (err error) { cat.Projects, err = c.Projects(ctx); return })
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return &cat, nil
}

func isNotFound(err error) bool {
	return statusCode(err) == http.StatusNotFound
}

// statusCode returns the HTTP status of an API error, or 0 if err did not come
// from an API response.
func statusCode(err error) int {
	var apiErr *godo.ErrorResponse
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		return apiErr.Response.StatusCode
	}
	return 0
}

// IsTransient reports whether a failed request may succeed if repeated: rate
// limiting, server errors, and network failures. Context cancellation is not
// transient.
func IsTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	code := statusCode(err)
	return code == 0 || code == http.StatusTooManyRequests || code >= 500
}
