package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Stellatinus/Stellatinus-Installer/internal/manifest"
)

// ErrHashMismatch is returned when a download does not match its sha256.
var ErrHashMismatch = errors.New("the download does not match its sha256")

// retryable marks a failure worth another attempt (a dropped connection, a
// stall, a server error). Anything else, such as a wrong hash or a server
// sending more than the manifest says, stops at once.
type retryable struct{ error }

func (e retryable) Unwrap() error { return e.error }

// httpsOnly returns a copy of c that refuses any redirect to a URL that is not
// https and follows at most five redirects. A nil c gets timeouts that suit
// large downloads: no overall limit, but a bound on connecting, on the TLS
// handshake and on waiting for the first byte.
func httpsOnly(c *http.Client) *http.Client {
	var cp http.Client
	if c == nil {
		cp = http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ForceAttemptHTTP2:     true,
		}}
	} else {
		cp = *c
	}
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("refusing a redirect to a non-https address (%s)", req.URL.Scheme)
		}
		return nil
	}
	return &cp
}

// fetch downloads a component to part and checks its sha256. A dropped
// download is resumed from what is already on disk, but only when the server
// answers the range request consistently; the whole file is hashed at the end
// either way, so a resumed file is trusted no more than a fresh one.
func (r *runner) fetch(ctx context.Context, idx int, c manifest.Component, part string) error {
	if !strings.HasPrefix(c.URL, "https://") {
		return fmt.Errorf("component %q: the address must start with https://", c.ID)
	}
	for attempt := 1; ; attempt++ {
		err := r.fetchOnce(ctx, idx, c, part)
		if err == nil {
			break
		}
		var re retryable
		if !errors.As(err, &re) || attempt >= r.opt.Attempts || ctx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.As(err, &re) {
				return fmt.Errorf("downloading %s failed after %d attempts: %w", c.Label, attempt, re.error)
			}
			return err
		}
		r.logf("download of %s interrupted (%v); trying again", c.Label, re.error)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.opt.Backoff(attempt)):
		}
	}

	r.event(idx, c, PhaseVerify, c.URL, 0, c.Size)
	sum, err := fileSHA256(part)
	if err != nil {
		return err
	}
	if sum != c.SHA256 {
		_ = os.Remove(part)
		return fmt.Errorf("%w: %s should be %s but is %s", ErrHashMismatch, c.Label, c.SHA256, sum)
	}
	r.logf("sha256 verified for %s", c.URL)
	return nil
}

func (r *runner) fetchOnce(ctx context.Context, idx int, c manifest.Component, part string) error {
	var have int64
	if fi, err := os.Stat(part); err == nil {
		have = fi.Size()
	}
	if have > c.Size {
		_ = os.Remove(part)
		have = 0
	}
	if have == c.Size {
		r.logf("%s is already downloaded in full", part)
		r.event(idx, c, PhaseDownload, c.URL, c.Size, c.Size)
		return nil
	}

	// A stall is no bytes for IdleTimeout, including while waiting for the
	// first byte; it cancels the request.
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := time.AfterFunc(r.opt.IdleTimeout, cancel)
	defer stall.Stop()

	req, err := http.NewRequestWithContext(rctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "stellatinus-installer")
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
		r.logf("GET %s (resuming at byte %d of %d)", c.URL, have, c.Size)
	} else {
		r.logf("GET %s (%d bytes)", c.URL, c.Size)
	}
	resp, err := r.opt.Client.Do(req)
	if err != nil {
		return r.networkError(ctx, rctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	flags := os.O_WRONLY | os.O_CREATE
	switch resp.StatusCode {
	case http.StatusOK:
		if resp.ContentLength >= 0 && resp.ContentLength != c.Size {
			return fmt.Errorf("the server announces %d bytes but the manifest says %d; not downloading", resp.ContentLength, c.Size)
		}
		flags |= os.O_TRUNC // the server ignored the range, or this is a fresh download
		have = 0
	case http.StatusPartialContent:
		if have == 0 {
			return errors.New("the server answered a range that was not asked for")
		}
		start, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != have || total != c.Size {
			return fmt.Errorf("the server's Content-Range %q does not match what was asked (from byte %d of %d); not resuming", resp.Header.Get("Content-Range"), have, c.Size)
		}
		flags |= os.O_APPEND
	case http.StatusRequestedRangeNotSatisfiable:
		_ = os.Remove(part)
		return retryable{errors.New("the server refused the resume; starting again")}
	default:
		err := fmt.Errorf("the server answered %s", resp.Status)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return retryable{err}
		}
		return err
	}

	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	written := have
	last := time.Now()
	buf := make([]byte, 64<<10)
	// One byte more than expected, to notice a body that is too long.
	body := io.LimitReader(resp.Body, c.Size-have+1)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			stall.Reset(r.opt.IdleTimeout)
			if _, werr := f.Write(buf[:n]); werr != nil {
				_ = f.Close()
				return werr
			}
			written += int64(n)
			if written > c.Size {
				_ = f.Close()
				_ = os.Remove(part)
				return fmt.Errorf("the server sent more than the %d bytes the manifest says", c.Size)
			}
			if time.Since(last) >= 100*time.Millisecond {
				r.event(idx, c, PhaseDownload, c.URL, written, c.Size)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = f.Close()
			return r.networkError(ctx, rctx, rerr)
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	r.event(idx, c, PhaseDownload, c.URL, written, c.Size)
	if written < c.Size {
		return retryable{io.ErrUnexpectedEOF}
	}
	return nil
}

// networkError turns a transport error into a retryable one, or into the
// context's error when the caller cancelled.
func (r *runner) networkError(parent, attempt context.Context, err error) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if attempt.Err() != nil {
		return retryable{fmt.Errorf("no data for %s: the server stalled", r.opt.IdleTimeout)}
	}
	return retryable{err}
}

// parseContentRange reads "bytes START-END/TOTAL".
func parseContentRange(h string) (start, total int64, ok bool) {
	rest, found := strings.CutPrefix(h, "bytes ")
	if !found {
		return 0, 0, false
	}
	rng, tot, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, false
	}
	s, _, found := strings.Cut(rng, "-")
	if !found {
		return 0, 0, false
	}
	var err1, err2 error
	start, err1 = strconv.ParseInt(s, 10, 64)
	total, err2 = strconv.ParseInt(tot, 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return start, total, true
}
