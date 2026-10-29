// Package cloud wraps the DigitalOcean API with retries, pagination and the
// lookups dovm needs.
package cloud

import (
	"context"
	"errors"
	"net/http"
	"strconv"
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

// Client is a DigitalOcean API client. 429 and 5xx responses are retried with
// exponential backoff, honoring Retry-After.
type Client struct {
	godo *godo.Client
}

// New builds a client authenticated with token.
func New(token string, opts Options) (*Client, error) {
	if token == "" {
		return nil, errors.New("no DigitalOcean API token provided")
	}
	opts = opts.withDefaults()

	// godo's retry layer requires the underlying client to use an oauth2 transport.
	httpClient := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))
	httpClient.Timeout = opts.Timeout

	waitMin := opts.RetryWaitMin.Seconds()
	waitMax := opts.RetryWaitMax.Seconds()
	clientOpts := []godo.ClientOpt{
		godo.SetUserAgent(userAgent),
		godo.WithRetryAndBackoffs(godo.RetryConfig{
			RetryMax:     opts.RetryMax,
			RetryWaitMin: &waitMin,
			RetryWaitMax: &waitMax,
		}),
	}
	if opts.BaseURL != "" {
		clientOpts = append(clientOpts, godo.SetBaseURL(opts.BaseURL))
	}

	g, err := godo.New(httpClient, clientOpts...)
	if err != nil {
		return nil, err
	}
	return &Client{godo: g}, nil
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

// Catalog is the live account data a spec is checked against.
type Catalog struct {
	Regions []godo.Region
	Sizes   []godo.Size
	SSHKeys []godo.Key
	Image   *godo.Image // nil when the requested image does not exist
}

// Catalog fetches regions, sizes, SSH keys and the given image concurrently.
func (c *Client) Catalog(ctx context.Context, image string) (*Catalog, error) {
	var cat Catalog
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { cat.Regions, err = c.Regions(ctx); return })
	g.Go(func() (err error) { cat.Sizes, err = c.Sizes(ctx); return })
	g.Go(func() (err error) { cat.SSHKeys, err = c.SSHKeys(ctx); return })
	g.Go(func() (err error) { cat.Image, err = c.Image(ctx, image); return })
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return &cat, nil
}

func isNotFound(err error) bool {
	var apiErr *godo.ErrorResponse
	return errors.As(err, &apiErr) && apiErr.Response != nil && apiErr.Response.StatusCode == http.StatusNotFound
}
