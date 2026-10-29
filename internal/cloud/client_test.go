package cloud

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/digitalocean/godo"
)

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New("test-token", Options{
		BaseURL:      srv.URL + "/",
		RetryMax:     3,
		RetryWaitMin: time.Millisecond,
		RetryWaitMax: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewRequiresToken(t *testing.T) {
	if _, err := New("", Options{}); err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestListAllFollowsPagination(t *testing.T) {
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/sizes", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		switch r.URL.Query().Get("page") {
		case "1":
			fmt.Fprintf(w, `{"sizes":[{"slug":"a"},{"slug":"b"}],"links":{"pages":{"next":"%s/v2/sizes?page=2"}}}`, srvURL)
		case "2":
			fmt.Fprintf(w, `{"sizes":[{"slug":"c"}],"links":{"pages":{"prev":"%s/v2/sizes?page=1"}}}`, srvURL)
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL
	c, err := New("test-token", Options{BaseURL: srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}

	sizes, err := c.Sizes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 3 || sizes[2].Slug != "c" {
		t.Fatalf("sizes = %+v", sizes)
	}
}

func TestRetriesRateLimitAndServerErrors(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			fmt.Fprint(w, `{"regions":[{"slug":"nyc3","available":true}]}`)
		}
	}))

	regions, err := c.Regions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 1 || calls.Load() != 3 {
		t.Fatalf("regions = %+v after %d calls", regions, calls.Load())
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"id":"unauthorized","message":"Unable to authenticate you"}`)
	}))

	if _, err := c.Regions(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Fatalf("401 was retried: %d calls", calls.Load())
	}
}

func TestImageNotFoundReturnsNil(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"id":"not_found","message":"The resource you were accessing could not be found."}`)
	}))

	img, err := c.Image(context.Background(), "no-such-image")
	if err != nil || img != nil {
		t.Fatalf("Image = %v, %v; want nil, nil", img, err)
	}
}

func TestCatalog(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/regions", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"regions":[{"slug":"nyc3","available":true}]}`)
	})
	mux.HandleFunc("/v2/sizes", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"sizes":[{"slug":"s-1vcpu-1gb","available":true}]}`)
	})
	mux.HandleFunc("/v2/account/keys", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ssh_keys":[{"id":1,"fingerprint":"aa"}]}`)
	})
	mux.HandleFunc("/v2/images/12345", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"image":{"id":12345,"slug":"","status":"available"}}`)
	})
	c := newTestClient(t, mux)

	cat, err := c.Catalog(context.Background(), "12345")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Regions) != 1 || len(cat.Sizes) != 1 || len(cat.SSHKeys) != 1 || cat.Image == nil || cat.Image.ID != 12345 {
		t.Fatalf("catalog = %+v", cat)
	}
}

func TestCreateIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"id":"server_error","message":"boom"}`)
	}))

	_, err := c.CreateDroplets(context.Background(), &godo.DropletMultiCreateRequest{Names: []string{"a"}})
	if err == nil || !IsTransient(err) {
		t.Fatalf("err = %v, want transient error", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("create sent %d times; a 5xx may already have created droplets", calls.Load())
	}
}

func TestDeleteDropletTreatsNotFoundAsSuccess(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v2/droplets/42" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"id":"not_found","message":"gone"}`)
	}))
	if err := c.DeleteDroplet(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
}

func TestIsTransient(t *testing.T) {
	apiErr := func(code int) error {
		req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
		return &godo.ErrorResponse{Response: &http.Response{StatusCode: code, Request: req}}
	}
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{apiErr(429), true},
		{apiErr(503), true},
		{apiErr(422), false},
		{apiErr(404), false},
		{fmt.Errorf("dial tcp: connection refused"), true},
		{context.Canceled, false},
		{fmt.Errorf("wrapped: %w", context.DeadlineExceeded), false},
	}
	for _, tc := range cases {
		if got := IsTransient(tc.err); got != tc.want {
			t.Errorf("IsTransient(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
