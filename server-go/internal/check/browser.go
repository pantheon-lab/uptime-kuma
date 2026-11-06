package check

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// RemoteBrowser is a browser that real-browser monitors can use instead of a local one. URL is
// its Chrome DevTools Protocol address, such as ws://browserless:3000?token=... or
// http://chrome:9222.
type RemoteBrowser struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// BrowserStore is what real-browser monitors need from the database.
type BrowserStore interface {
	RemoteBrowser(ctx context.Context, id int32) (RemoteBrowser, error)
	// ChromeExecutable is the chromeExecutable setting.
	ChromeExecutable(ctx context.Context) (string, error)
	SaveScreenshot(ctx context.Context, monitorID int32, png []byte) error
}

// SetBrowsers sets where real-browser monitors find their settings and keep screenshots. Call it
// before checks start.
func (c *Checker) SetBrowsers(s BrowserStore) { c.browsers = s }

// The viewport of Playwright's default context, which the Node.js backend's screenshots had.
const (
	viewportWidth  = 1280
	viewportHeight = 720
)

// allowedChromes are the executables a local browser may be, as in Node.js on Linux. Setting
// UPTIME_KUMA_ALLOW_ALL_CHROME_EXEC=1 allows any.
var allowedChromes = []string{"chromium", "chromium-browser", "google-chrome", "/usr/bin/chromium",
	"/usr/bin/chromium-browser", "/usr/bin/google-chrome", "/snap/bin/chromium"}

// localBrowser is the one local browser that checks share, each in a context of its own.
var localBrowser struct {
	sync.Mutex
	path   string
	ctx    context.Context
	cancel context.CancelFunc
}

// chromePath decides which executable to run, as the Node.js backend did.
func chromePath(setting string) (string, error) {
	setting = strings.TrimSpace(setting)
	switch {
	case strings.EqualFold(setting, "#playwright_chromium"):
		return "", errors.New("Playwright's Chromium isn't available in this server; install Chromium, or use a remote browser") //nolint:staticcheck // Shown to users.
	case setting == "":
		for _, candidate := range allowedChromes {
			if path, err := exec.LookPath(candidate); err == nil {
				return path, nil
			}
		}
		return "", errors.New("Chromium not found, please specify Chromium executable path in the settings page.") //nolint:staticcheck // Shown to users, as in Node.js.
	case os.Getenv("UPTIME_KUMA_ALLOW_ALL_CHROME_EXEC") != "1" && !slices.Contains(allowedChromes, setting):
		return "", errors.New("This Chromium executable path is not allowed by default. If you are sure this is safe, " + //nolint:staticcheck // Shown to users, as in Node.js.
			"please add an environment variable UPTIME_KUMA_ALLOW_ALL_CHROME_EXEC=1 to allow it.")
	}
	return setting, nil
}

func chromeAllocator(path string) (context.Context, context.CancelFunc) {
	options := append(slices.Clone(chromedp.DefaultExecAllocatorOptions[:]),
		chromedp.ExecPath(path),
		chromedp.WindowSize(viewportWidth, viewportHeight),
		// Playwright, which the Node.js backend used, turns Chrome's sandbox off by default too.
		chromedp.NoSandbox,
	)
	return chromedp.NewExecAllocator(context.Background(), options...)
}

// sharedChrome returns the local browser, and starts it if it isn't running.
func sharedChrome(ctx context.Context, path string) (context.Context, error) {
	localBrowser.Lock()
	defer localBrowser.Unlock()
	if localBrowser.ctx != nil && localBrowser.ctx.Err() == nil && localBrowser.path == path {
		return localBrowser.ctx, nil
	}
	if localBrowser.cancel != nil {
		localBrowser.cancel()
	}
	allocator, cancelAllocator := chromeAllocator(path)
	browserCtx, cancelBrowser := chromedp.NewContext(allocator)
	cancel := func() { cancelBrowser(); cancelAllocator() }
	// The first run starts the browser. It mustn't end with the check, so it only waits for it.
	started := make(chan error, 1)
	go func() { started <- chromedp.Do(browserCtx) }()
	select {
	case err := <-started:
		if err != nil {
			cancel()
			return nil, fmt.Errorf("start %s: %w", path, err)
		}
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
	localBrowser.path, localBrowser.ctx, localBrowser.cancel = path, browserCtx, cancel
	return browserCtx, nil
}

// StopBrowser closes the local browser, if one runs.
func StopBrowser() {
	localBrowser.Lock()
	defer localBrowser.Unlock()
	if localBrowser.cancel != nil {
		localBrowser.cancel()
		localBrowser.ctx, localBrowser.cancel = nil, nil
	}
}

// TestChrome starts the executable and returns its version, for the settings page.
func TestChrome(ctx context.Context, setting string) (string, error) {
	path, err := chromePath(setting)
	if err != nil {
		return "", err
	}
	allocator, cancelAllocator := chromeAllocator(path)
	defer cancelAllocator()
	browserCtx, cancelBrowser := chromedp.NewContext(allocator)
	defer cancelBrowser()
	stop := context.AfterFunc(ctx, cancelBrowser)
	defer stop()
	return browserVersion(browserCtx)
}

// TestRemoteBrowser connects to a remote browser and returns its version.
func TestRemoteBrowser(ctx context.Context, address string) (string, error) {
	if err := validRemoteBrowserURL(address); err != nil {
		return "", err
	}
	allocator, cancelAllocator := remote.NewAllocator(ctx, address)
	defer cancelAllocator()
	browserCtx, cancelBrowser := chromedp.NewContext(allocator)
	defer cancelBrowser()
	return browserVersion(browserCtx)
}

func browserVersion(ctx context.Context) (string, error) {
	if err := chromedp.Do(ctx); err != nil {
		return "", err
	}
	version, err := cdp.Call(ctx, chromedp.FromContext(ctx).Browser, browser.GetVersion, cdp.Empty{})
	if err != nil {
		return "", err
	}
	return version.Product, nil
}

// ValidateRemoteBrowser checks a remote browser's settings before they're saved.
func ValidateRemoteBrowser(b RemoteBrowser) error {
	if strings.TrimSpace(b.Name) == "" {
		return errors.New("name is required")
	}
	return validRemoteBrowserURL(b.URL)
}

func validRemoteBrowserURL(address string) error {
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil || u.Host == "" || !slices.Contains([]string{"ws", "wss", "http", "https"}, u.Scheme) {
		return errors.New("the URL must be a ws://, wss://, http:// or https:// address of the browser's DevTools Protocol")
	}
	return nil
}

// runRealBrowser loads the page in Chrome, keeps a screenshot, and is UP for a status from 200
// to 399, as the Node.js backend was.
func (c *Checker) runRealBrowser(ctx context.Context, m monitor.Monitor) monitor.Result {
	if c.browsers == nil {
		return monitor.Result{Msg: "Real browser monitors aren't available"}
	}
	u, err := url.Parse(m.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return monitor.Result{Msg: "Invalid url protocol, only http and https are allowed."}
	}

	var parent context.Context
	if m.RemoteBrowserID > 0 {
		remoteBrowser, err := c.browsers.RemoteBrowser(ctx, m.RemoteBrowserID)
		if err != nil {
			return monitor.Result{Msg: "Remote browser not found"}
		}
		allocator, cancel := remote.NewAllocator(ctx, remoteBrowser.URL)
		defer cancel()
		parent = allocator
	} else {
		setting, err := c.browsers.ChromeExecutable(ctx)
		if err != nil {
			return monitor.Result{Msg: err.Error()}
		}
		path, err := chromePath(setting)
		if err != nil {
			return monitor.Result{Msg: err.Error()}
		}
		if parent, err = sharedChrome(ctx, path); err != nil {
			return monitor.Result{Msg: err.Error()}
		}
	}

	// A context of its own, like Playwright's newContext, so that checks share no cookies. Closing
	// the tab doesn't close a remote browser.
	tab, cancelTab := chromedp.NewContext(parent, chromedp.WithNewBrowserContext())
	defer cancelTab()
	stop := context.AfterFunc(ctx, cancelTab)
	defer stop()

	start := time.Now()
	if err := chromedp.Do(tab, chromedp.EmulateViewport(viewportWidth, viewportHeight)); err != nil {
		if m.RemoteBrowserID == 0 && ctx.Err() == nil {
			// The tab couldn't open, so the local browser may have died. The next check starts another.
			StopBrowser()
		}
		return monitor.Result{Msg: browserError(ctx, err, m)}
	}
	response, err := chromedp.Run(tab, chromedp.NavigateResponse(m.URL))
	if err != nil {
		return monitor.Result{Msg: browserError(ctx, err, m)}
	}
	elapsed := elapsedMs(start)
	if m.ScreenshotDelay > 0 {
		select {
		case <-time.After(time.Duration(m.ScreenshotDelay) * time.Millisecond):
		case <-ctx.Done():
			return monitor.Result{Msg: browserError(ctx, ctx.Err(), m)}
		}
	}
	png, err := chromedp.Run(tab, chromedp.CaptureScreenshot())
	if err != nil {
		return monitor.Result{Msg: browserError(ctx, err, m)}
	}
	if err := c.browsers.SaveScreenshot(ctx, m.ID, png); err != nil {
		return monitor.Result{Msg: "save the screenshot: " + err.Error()}
	}

	if response == nil {
		return monitor.Result{Msg: "The page sent no response"}
	}
	status := int(response.Status)
	if status < 200 || status >= 400 {
		return monitor.Result{Msg: strconv.Itoa(status)}
	}
	ping := elapsed
	if response.Timing != nil && response.Timing.ReceiveHeadersEnd > 0 {
		// The time to the document's response, as Playwright's timing gave.
		ping = response.Timing.ReceiveHeadersEnd
	}
	return monitor.Result{OK: true, Ping: &ping, Msg: strconv.Itoa(status)}
}

func browserError(ctx context.Context, err error, m monitor.Monitor) string {
	if ctx.Err() != nil {
		return fmt.Sprintf("timeout of %dms exceeded", m.EffectiveTimeout().Milliseconds())
	}
	return err.Error()
}
