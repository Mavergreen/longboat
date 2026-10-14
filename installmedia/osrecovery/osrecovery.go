// Package osrecovery is how 10.9's InstallESD.dmg is fetched: Apple's
// osrecovery handshake, then a verified download with the token it hands
// out. Fetch is an installmedia.Recipe. The package imports download and
// the standard library only.
package osrecovery

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Mavergreen/longboat/installmedia/download"
)

const (
	DefaultBase = "http://osrecovery.apple.com"
	// From Mavericks Forever's get.sh, donated by dosdude1 from a broken
	// Mac (INHERITED). They are not this machine's and are not secrets.
	boardSerial = "C0243070168G3M91F"
	boardID     = "Mac-3CBD00234E554E41"
	rom         = "003EE1E6AC14"
)

// Recovery is Apple's osrecovery service. The transfer is plain HTTP by
// Apple's design (the token is a cookie and the payload is unencrypted),
// so the checksum is the only thing between this and a middlebox.
//
// Every request it makes -- the handshake's two and Probe's HEAD -- has
// its own timeout, and a transient failure (a network error, a timeout, a
// 5xx) is retried the way a download is.
type Recovery struct {
	Base   string       // "" means DefaultBase
	Client *http.Client // nil means http.DefaultClient
	Rand   io.Reader    // nil means crypto/rand

	Retries int                           // as Getter's: 0 means 3, negative means none
	Backoff time.Duration                 // as Getter's: 0 means 1s
	Timeout time.Duration                 // per request; 0 (or negative) means 1 minute
	Log     func(format string, a ...any) // retries are reported here; nil means nowhere
}

// defaultRequestTimeout bounds each of Recovery's requests. They are
// small -- a cookie, a few lines of text, a HEAD -- so a minute is
// generous; without it, a server that accepts and never answers would
// hang the handshake for good.
const defaultRequestTimeout = time.Minute

func (rc Recovery) timeout() time.Duration {
	if rc.Timeout <= 0 {
		return defaultRequestTimeout
	}
	return rc.Timeout
}

// retryable reports whether a failed request is worth another attempt:
// never once ctx itself is done (a signal), otherwise as classifyErr says
// -- which includes the per-request timeout firing.
func retryable(ctx context.Context, err error) bool {
	return download.Transient(ctx, err)
}

func (rc Recovery) base() string {
	if rc.Base == "" {
		return DefaultBase
	}
	return rc.Base
}

func (rc Recovery) client() *http.Client {
	if rc.Client == nil {
		return http.DefaultClient
	}
	return rc.Client
}

// deriveKey is SHA-256 over client id, the server id's hex half, the ROM,
// SHA-256(serial+board id) and ten 0xCC bytes, as uppercase hex.
func deriveKey(clientID, serverID string) (string, error) {
	_, half, ok := strings.Cut(serverID, "~")
	if !ok {
		return "", fmt.Errorf("osrecovery returned no usable session id (got %q)", serverID)
	}
	h := sha256.New()
	for _, x := range []string{clientID, half, rom} {
		b, err := hex.DecodeString(x)
		if err != nil {
			return "", fmt.Errorf("bad hex %q: %w", x, err)
		}
		h.Write(b)
	}
	inner := sha256.Sum256([]byte(boardSerial + boardID))
	h.Write(inner[:])
	h.Write([]byte(strings.Repeat("\xcc", 10)))
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil))), nil
}

// Handshake asks osrecovery for the installer's URL and a download token.
// A transient failure retries the whole handshake -- the session request
// and the payload request -- with the same client id.
func (rc Recovery) Handshake(ctx context.Context) (assetURL, token string, err error) {
	rnd := rc.Rand
	if rnd == nil {
		rnd = rand.Reader
	}
	cid := make([]byte, 8)
	if _, err := io.ReadFull(rnd, cid); err != nil {
		return "", "", err
	}
	clientID := strings.ToUpper(hex.EncodeToString(cid))
	err = download.Retry(ctx, rc.Retries, rc.Backoff, rc.Log, "osrecovery handshake", func() (bool, error) {
		var retry bool
		var err error
		assetURL, token, retry, err = rc.handshakeOnce(ctx, clientID)
		return retry, err
	})
	if err != nil {
		return "", "", err
	}
	return assetURL, token, nil
}

// handshakeOnce is one attempt at Handshake, each request under its own
// timeout. It says whether a failure is worth retrying.
func (rc Recovery) handshakeOnce(ctx context.Context, clientID string) (assetURL, token string, retry bool, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, rc.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rc.base()+"/", nil)
	if err != nil {
		return "", "", false, err
	}
	resp, err := rc.client().Do(req)
	if err != nil {
		return "", "", retryable(ctx, err), fmt.Errorf("cannot reach osrecovery for a session id: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", resp.StatusCode >= 500, fmt.Errorf("osrecovery refused the session request: %s", resp.Status)
	}
	serverID := ""
	for _, c := range resp.Cookies() {
		serverID = c.Value // the jar's last cookie, unless one is "session"
		if c.Name == "session" {
			break
		}
	}
	key, err := deriveKey(clientID, serverID)
	if err != nil {
		return "", "", false, err
	}
	body := fmt.Sprintf("cid=%s\nsn=%s\nbid=%s\nk=%s", clientID, boardSerial, boardID, key)

	reqCtx, cancel = context.WithTimeout(ctx, rc.timeout())
	defer cancel()
	req, err = http.NewRequestWithContext(reqCtx, http.MethodPost, rc.base()+"/InstallationPayload/OSInstaller", strings.NewReader(body))
	if err != nil {
		return "", "", false, err
	}
	req.Header.Set("Content-Type", "text/plain")
	req.AddCookie(&http.Cookie{Name: "session", Value: serverID})
	resp, err = rc.client().Do(req)
	if err != nil {
		return "", "", retryable(ctx, err), fmt.Errorf("InstallationPayload request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", resp.StatusCode >= 500, fmt.Errorf("InstallationPayload request was refused: %s", resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "AU: "); ok {
			assetURL = v
		}
		if v, ok := strings.CutPrefix(sc.Text(), "AT: "); ok {
			token = v
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", retryable(ctx, err), fmt.Errorf("reading Apple's installation payload: %w", err)
	}
	if assetURL == "" || token == "" {
		return "", "", false, fmt.Errorf("the installation payload Apple sent had no asset URL or token")
	}
	return assetURL, token, false, nil
}

// offer is the handshake, refusing any installer but want.
func (rc Recovery) offer(ctx context.Context, want download.Item) (string, error) {
	url, token, err := rc.Handshake(ctx)
	if err != nil {
		return "", err
	}
	// The same handshake serves whatever OS Apple decides that board is
	// entitled to; silently installing a different one would be a long
	// afternoon (get.sh checks this too).
	if url != want.URL {
		return "", fmt.Errorf("the installer Apple offered is %s, not the Mavericks InstallESD URL %s", url, want.URL)
	}
	return token, nil
}

// Fetch is Apple's InstallESD.dmg, verified against want: from the cache,
// else fetched from Apple. The returned path is read-only (see
// download.Getter.Get); never open it for writing. It is an
// installmedia.Recipe.
func (rc Recovery) Fetch(ctx context.Context, g *download.Getter, want download.Item) (string, error) {
	// Cache first: no handshake for a file already here. A rotten cached
	// file is an error, never a reason to fetch 5 GB again.
	cached := want
	cached.CacheOnly = true
	p, err := g.Get(ctx, cached)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, download.ErrNotCached) {
		// A file already sits at the final name here (rotten, or the check
		// itself failed) -- "nothing was renamed" would be false, not
		// reassuring, so this error is returned as Get gave it.
		return "", err
	}
	token, err := rc.offer(ctx, want)
	if err != nil {
		return "", err
	}
	h := http.Header{}
	h.Set("Cookie", "AssetToken="+token)
	if g.Log != nil {
		g.Log("downloading InstallESD.dmg (about 5.2 GB, over plain HTTP)")
	}
	fetch := want
	// Replacing any Header on want is fine: a Recipe's want is a pin (name, URL,
	// sum, filename), and the token cookie is the only header Apple's CDN needs.
	fetch.Header = h
	p, err = g.Get(ctx, fetch)
	if err != nil {
		return "", withNothingRenamed(err)
	}
	return p, nil
}

// withNothingRenamed makes an error from a download explicit about the
// invariant Get already guarantees: nothing unverified is ever renamed to
// its final name. 5.2 GB failing partway through is exactly when that
// reassurance matters most.
func withNothingRenamed(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "nothing was renamed into place") {
		return err
	}
	return fmt.Errorf("%w (nothing was renamed into place)", err)
}

// Probe performs the handshake and asks the CDN for the size, downloading
// nothing: a measurement of Apple's side without 5 GB of traffic.
func (rc Recovery) Probe(ctx context.Context, want download.Item) (string, int64, error) {
	token, err := rc.offer(ctx, want)
	if err != nil {
		return "", 0, err
	}
	var size int64
	err = download.Retry(ctx, rc.Retries, rc.Backoff, rc.Log, "HEAD "+want.URL, func() (bool, error) {
		reqCtx, cancel := context.WithTimeout(ctx, rc.timeout())
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodHead, want.URL, nil)
		if err != nil {
			return false, err
		}
		req.Header.Set("Cookie", "AssetToken="+token)
		resp, err := rc.client().Do(req)
		if err != nil {
			return retryable(ctx, err), err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return resp.StatusCode >= 500, fmt.Errorf("HEAD %s: %s", want.URL, resp.Status)
		}
		if resp.ContentLength < 0 {
			return false, fmt.Errorf("HEAD %s: no Content-Length in the response", want.URL)
		}
		size = resp.ContentLength
		return false, nil
	})
	if err != nil {
		return "", 0, err
	}
	return want.URL, size, nil
}
